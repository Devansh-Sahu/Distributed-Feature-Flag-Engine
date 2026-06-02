package notifier

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/cache"
)

// PGListener listens on a PostgreSQL LISTEN/NOTIFY channel and triggers
// cache invalidation when flag configs change.
//
// WHY a dedicated connection (not pgxpool)?
// pgxpool manages a pool of connections, automatically recycling them.
// LISTEN is connection-scoped: if the connection is returned to the pool
// and reused by another goroutine, that goroutine inherits your LISTEN
// registrations and receives your notifications. This causes subtle bugs.
// LISTEN/NOTIFY requires a dedicated, long-lived connection that is NEVER
// shared. We use pgx.Connect (single connection) for exactly this reason.
type PGListener struct {
	connString  string
	materializer *cache.Materializer
}

// NewPGListener creates a new PGListener.
func NewPGListener(connString string, materializer *cache.Materializer) *PGListener {
	return &PGListener{
		connString:   connString,
		materializer: materializer,
	}
}

// Listen blocks, listening for flag_config_changed notifications from Postgres.
// On each notification, it triggers a targeted re-materialization of the changed flag.
// It reconnects automatically if the connection drops.
//
// Call this in a goroutine: go listener.Listen(ctx)
// Cancel the context to stop.
func (l *PGListener) Listen(ctx context.Context) {
	log.Info().Msg("pg-listener: starting")

	for {
		if err := l.listenLoop(ctx); err != nil {
			if ctx.Err() != nil {
				log.Info().Msg("pg-listener: context cancelled, stopping")
				return
			}
			// Connection dropped — wait a second and reconnect
			// WHY 1 second? Enough time for Postgres to recover from a transient
			// blip without hammering it with reconnect attempts.
			log.Warn().Err(err).Msg("pg-listener: connection lost, reconnecting in 1s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				// reconnect
			}
		}
	}
}

// listenLoop establishes one connection, sends LISTEN, and processes notifications
// until the connection fails or the context is cancelled.
func (l *PGListener) listenLoop(ctx context.Context) error {
	// Single dedicated connection — NOT from the pool
	conn, err := pgx.Connect(ctx, l.connString)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	// Register our LISTEN on the channel created by migration 002
	if _, err := conn.Exec(ctx, "LISTEN flag_config_changed"); err != nil {
		return err
	}
	log.Info().Msg("pg-listener: LISTEN registered on 'flag_config_changed'")

	for {
		// WaitForNotification blocks until a notification arrives or context is done.
		// Under the hood, this is a long-poll on the Postgres wire protocol.
		// There is zero CPU usage while waiting — the goroutine is parked by the runtime.
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return err // connection error — trigger reconnect
		}

		l.handleNotification(ctx, notification.Payload)
	}
}

// handleNotification processes a single NOTIFY payload.
// It parses the JSON, then triggers a targeted re-materialization.
func (l *PGListener) handleNotification(ctx context.Context, payload string) {
	var p cache.NotifyPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		log.Warn().Str("payload", payload).Err(err).Msg("pg-listener: failed to parse notification payload")
		return
	}

	if p.FlagKey == "" || p.EnvName == "" {
		log.Warn().Str("payload", payload).Msg("pg-listener: notification missing flag_key or env_name, skipping")
		return
	}

	log.Debug().
		Str("flag", p.FlagKey).
		Str("env", p.EnvName).
		Str("reason", p.Reason).
		Msg("pg-listener: received flag change notification")

	// Re-materialize this specific flag+env from Postgres → Redis → pub/sub.
	// We use a short timeout context so a slow DB doesn't block the listener goroutine.
	// If it times out, the next Kafka message (Phase 3) will catch the miss anyway.
	materCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := l.materializer.MaterializeFlag(materCtx, p.FlagKey, p.EnvName); err != nil {
		log.Error().Err(err).
			Str("flag", p.FlagKey).
			Str("env", p.EnvName).
			Msg("pg-listener: failed to re-materialize flag")
		// Non-fatal: the flag state in Redis might be stale until the next update.
		// Phase 3 (Kafka) adds durability: even if this fails, the Kafka consumer
		// will catch up and re-materialize on reconnect.
	}
}
