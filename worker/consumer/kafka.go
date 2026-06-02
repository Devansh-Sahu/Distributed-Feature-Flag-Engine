package consumer

import (
	"context"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/rs/zerolog/log"
)

// ─────────────────────────────────────────────────────────────────────────
// KafkaConsumer manages two Kafka readers:
//   1. One for the flag_configs topic
//   2. One for the targeting_rules topic
//
// WHY kafka-go over sarama or confluent-kafka-go?
//   - kafka-go: pure Go, no CGO, ~8MB binary. Works in distroless/scratch.
//   - sarama: pure Go but complex API, lacks context cancellation.
//   - confluent-kafka-go: wraps librdkafka (C), requires CGO. Binary is 40MB+.
//
// For a microservice like this worker, kafka-go hits the sweet spot:
// simple API, pure Go, production-grade.
//
// CONSUMER GROUPS AND OFFSETS:
// kafka-go commits offsets after every successful message process.
// If the worker crashes mid-batch, it will re-process from the last
// committed offset — guaranteed at-least-once delivery.
// Our processor is IDEMPOTENT (re-materializing the same flag state is safe),
// so at-least-once is the correct guarantee for this workload.
// ─────────────────────────────────────────────────────────────────────────

// KafkaConsumer holds the readers for each topic.
type KafkaConsumer struct {
	flagConfigReader    *kafka.Reader
	targetingRuleReader *kafka.Reader
	processor           *Processor
}

// NewKafkaConsumer creates readers for both topics.
func NewKafkaConsumer(brokers []string, groupID, flagConfigTopic, targetingRuleTopic string, processor *Processor) *KafkaConsumer {
	readerConfig := func(topic string) kafka.ReaderConfig {
		return kafka.ReaderConfig{
			Brokers:     brokers,
			Topic:       topic,
			GroupID:     groupID,
			MinBytes:    1,       // fetch as soon as 1 byte is available
			MaxBytes:    10e6,    // 10MB max per fetch
			MaxWait:     1 * time.Second,
			StartOffset: kafka.FirstOffset, // on first run, consume from the beginning
			// CommitInterval: 0 means commit after every message (safest for our workload)
			CommitInterval: 0,
			// Dialer with timeout prevents hanging indefinitely on Kafka unavailability
			Dialer: &kafka.Dialer{
				Timeout:   10 * time.Second,
				DualStack: true,
			},
		}
	}

	return &KafkaConsumer{
		flagConfigReader:    kafka.NewReader(readerConfig(flagConfigTopic)),
		targetingRuleReader: kafka.NewReader(readerConfig(targetingRuleTopic)),
		processor:           processor,
	}
}

// Run starts both consumer loops concurrently.
// It blocks until the context is cancelled.
// If either consumer encounters an unrecoverable error, it logs and backs off.
func (c *KafkaConsumer) Run(ctx context.Context) {
	log.Info().Msg("worker: starting Kafka consumer loops")

	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		consumeLoop(ctx, c.flagConfigReader, "flag_configs", c.processor.HandleFlagConfig)
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		consumeLoop(ctx, c.targetingRuleReader, "targeting_rules", c.processor.HandleTargetingRule)
	}()

	// Wait for both loops to exit (context cancelled)
	<-done
	<-done
	log.Info().Msg("worker: all consumer loops stopped")
}

// Close gracefully shuts down the Kafka readers.
func (c *KafkaConsumer) Close() {
	c.flagConfigReader.Close()
	c.targetingRuleReader.Close()
}

// consumeLoop is the inner read loop for a single topic.
// It reads messages one-by-one, calls the handler, and commits the offset.
// On handler failure it logs but continues (the message will not be re-read
// because we still commit — this prevents poison-pill messages from blocking
// the entire pipeline forever).
func consumeLoop(
	ctx context.Context,
	reader *kafka.Reader,
	topicLabel string,
	handler func(context.Context, []byte) error,
) {
	log.Info().Str("topic", topicLabel).Msg("worker: consumer loop started")

	for {
		// FetchMessage blocks until a message arrives, context is cancelled,
		// or the connection fails.
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Clean shutdown via context cancel
				log.Info().Str("topic", topicLabel).Msg("worker: context cancelled, stopping consumer")
				return
			}
			// Network error or broker unavailable — log and retry
			log.Error().Err(err).Str("topic", topicLabel).Msg("worker: fetch error, retrying in 2s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		lag := time.Since(msg.Time)
		log.Debug().
			Str("topic", topicLabel).
			Int64("offset", msg.Offset).
			Dur("lag", lag).
			Int("value_size", len(msg.Value)).
			Msg("worker: message received")

		// Process the message
		if err := handler(ctx, msg.Value); err != nil {
			// Non-fatal: log the error but commit the offset anyway.
			// WHY commit on error? Because our handler errors are typically:
			//   - Malformed message (will always fail — infinite retry is wrong)
			//   - Transient DB/Redis error (logged, worker will retry next update)
			// A dead-letter queue (DLQ) is the production solution for persistent
			// failures, but for this project scope, logging is sufficient.
			log.Error().Err(err).
				Str("topic", topicLabel).
				Int64("offset", msg.Offset).
				Msg("worker: handler error (committing offset anyway)")
		}

		// Commit the offset — tells Kafka we've processed this message.
		// kafka-go CommitMessages is synchronous and blocks until confirmed.
		if err := reader.CommitMessages(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Str("topic", topicLabel).Int64("offset", msg.Offset).
				Msg("worker: failed to commit offset")
		}
	}
}
