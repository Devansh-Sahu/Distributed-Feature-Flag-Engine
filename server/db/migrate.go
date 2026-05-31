package db

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// RunMigration executes a specific SQL migration file by name (without .sql extension).
// It is idempotent: the migration SQL uses IF NOT EXISTS, CREATE OR REPLACE, etc.,
// so running it multiple times is safe.
//
// WHY embed? At build time, Go embeds the SQL files into the binary.
// The final Docker image doesn't need to mount a migrations volume.
// The SQL files are always in sync with the binary — no version drift.
func RunMigration(ctx context.Context, pool *pgxpool.Pool, name string) error {
	filename := "migrations/" + name + ".sql"
	data, err := migrationFS.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", filename, err)
	}

	sql := strings.TrimSpace(string(data))
	if sql == "" {
		return nil
	}

	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("execute migration %s: %w", name, err)
	}

	log.Info().Str("migration", name).Msg("migration applied successfully")
	return nil
}
