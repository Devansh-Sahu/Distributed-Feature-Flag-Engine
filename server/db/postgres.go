package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// NewPool creates a PostgreSQL connection pool.
// We use pgxpool (not database/sql) because:
//   - pgxpool gives us native PostgreSQL types (UUID, JSONB) without scanning hacks
//   - Built-in connection pooling with health-check and backoff
//   - Supports pgx v5 named args and structured scanning
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	// Pool sizing: 10 min, 25 max is a good start for a flag API.
	// Flag reads are short; writes are rare. This avoids connection exhaustion
	// on Postgres which defaults to max_connections=100.
	cfg.MinConns = 5
	cfg.MaxConns = 25
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	// Health check: if a connection sits idle for 30s, ping it before returning
	// from the pool. This prevents "broken pipe" errors after network blips.
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Verify connectivity immediately — fail fast on startup if DB is unreachable
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	log.Info().
		Str("host", cfg.ConnConfig.Host).
		Int32("max_conns", cfg.MaxConns).
		Msg("connected to PostgreSQL")

	return pool, nil
}
