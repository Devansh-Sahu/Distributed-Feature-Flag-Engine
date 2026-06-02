package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/worker/consumer"
)

func main() {
	// ── Logging ────────────────────────────────────────────────────
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	if os.Getenv("ENV") == "development" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	}
	level, _ := zerolog.ParseLevel(getenv("LOG_LEVEL", "info"))
	zerolog.SetGlobalLevel(level)

	log.Info().Msg("ffee-worker starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── PostgreSQL ──────────────────────────────────────────────────
	dbURL := requireenv("DATABASE_URL")
	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid DATABASE_URL")
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create Postgres pool")
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to Postgres")
	}
	log.Info().Msg("connected to PostgreSQL")

	// ── Redis ───────────────────────────────────────────────────────
	redisAddr := getenv("REDIS_ADDR", "localhost:6379")
	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
	})
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		log.Fatal().Err(err).Str("addr", redisAddr).Msg("failed to connect to Redis")
	}
	defer rdb.Close()
	log.Info().Str("addr", redisAddr).Msg("connected to Redis")

	// ── Kafka ───────────────────────────────────────────────────────
	kafkaBrokers := strings.Split(requireenv("KAFKA_BROKERS"), ",")
	groupID := getenv("KAFKA_GROUP_ID", "ffee-worker")
	flagConfigTopic := getenv("KAFKA_TOPIC_FLAG_CONFIGS", "ffee.public.flag_configs")
	targetingRuleTopic := getenv("KAFKA_TOPIC_TARGETING_RULES", "ffee.public.targeting_rules")

	log.Info().
		Strs("brokers", kafkaBrokers).
		Str("group", groupID).
		Str("flag_configs_topic", flagConfigTopic).
		Str("targeting_rules_topic", targetingRuleTopic).
		Msg("Kafka config loaded")

	// ── Wire up processor and consumer ─────────────────────────────
	processor := consumer.NewProcessor(pool, rdb)
	kafkaConsumer := consumer.NewKafkaConsumer(
		kafkaBrokers,
		groupID,
		flagConfigTopic,
		targetingRuleTopic,
		processor,
	)
	defer kafkaConsumer.Close()

	// ── Health HTTP server ──────────────────────────────────────────
	// Simple health endpoint on :9095 so docker-compose healthcheck works
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]string{"status": "ok", "service": "ffee-worker"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})
	go func() {
		log.Info().Str("addr", ":9095").Msg("worker health server listening")
		if err := http.ListenAndServe(":9095", nil); err != nil && err != http.ErrServerClosed {
			log.Warn().Err(err).Msg("health server error")
		}
	}()

	// ── Start consumer (blocks until context cancelled) ─────────────
	go kafkaConsumer.Run(ctx)

	// ── Graceful shutdown ───────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info().Str("signal", sig.String()).Msg("worker: shutting down")
	cancel()
	time.Sleep(2 * time.Second) // allow in-flight messages to finish
	log.Info().Msg("worker: exited cleanly")
}

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func requireenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "required env var %s is not set\n", key)
		os.Exit(1)
	}
	return v
}
