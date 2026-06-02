package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/models"
)

// Materializer reads flag state from PostgreSQL and writes it to Redis.
// It runs at server startup (warm cache) and is called on-demand for
// individual flags when a LISTEN/NOTIFY event is received.
type Materializer struct {
	db    *pgxpool.Pool
	cache *RedisCache
}

// NewMaterializer creates a new Materializer.
func NewMaterializer(db *pgxpool.Pool, cache *RedisCache) *Materializer {
	return &Materializer{db: db, cache: cache}
}

// MaterializeAll reads ALL flags across ALL environments from Postgres and
// writes them to Redis. Called once at server startup.
//
// Performance note: this does N+1 queries intentionally (one query per flag config
// to fetch its targeting rules). On startup latency doesn't matter; correctness does.
// With 10,000 flags × 3 envs = 30,000 Redis HSET operations.
// At 100µs per HSET with pipelining, that's ~3 seconds for a fully warmed cache.
// Acceptable for startup; not acceptable for hot-path reads.
func (m *Materializer) MaterializeAll(ctx context.Context) error {
	start := time.Now()

	// Step 1: load all environments
	envRows, err := m.db.Query(ctx, `SELECT id, name FROM environments ORDER BY name`)
	if err != nil {
		return fmt.Errorf("query environments: %w", err)
	}
	type envRow struct{ id, name string }
	var envs []envRow
	for envRows.Next() {
		var e envRow
		if err := envRows.Scan(&e.id, &e.name); err != nil {
			envRows.Close()
			return fmt.Errorf("scan environment: %w", err)
		}
		envs = append(envs, e)
	}
	envRows.Close()

	total := 0
	for _, env := range envs {
		count, err := m.materializeEnvironment(ctx, env.id, env.name)
		if err != nil {
			log.Error().Err(err).Str("env", env.name).Msg("materializer: failed to materialize environment")
			// Continue with other environments rather than failing everything
			continue
		}
		total += count
		log.Info().
			Str("env", env.name).
			Int("flag_count", count).
			Msg("materializer: environment warmed")
	}

	log.Info().
		Int("total_flags", total).
		Dur("elapsed", time.Since(start)).
		Msg("materializer: Redis cache fully warmed")

	return nil
}

// materializeEnvironment materializes all flags for a single environment.
// Returns the count of flags materialized.
func (m *Materializer) materializeEnvironment(ctx context.Context, envID, envName string) (int, error) {
	// One query to get all flag configs + flag metadata for this environment
	rows, err := m.db.Query(ctx, `
		SELECT
			f.key           AS flag_key,
			f.flag_type,
			fc.id           AS config_id,
			fc.enabled,
			fc.default_value,
			fc.rollout_percentage,
			fc.updated_at
		FROM flag_configs fc
		JOIN flags f ON f.id = fc.flag_id
		WHERE fc.environment_id = $1
		ORDER BY f.key
	`, envID)
	if err != nil {
		return 0, fmt.Errorf("query flag configs for env %s: %w", envName, err)
	}
	defer rows.Close()

	type configRow struct {
		flagKey           string
		flagType          models.FlagType
		configID          string
		enabled           bool
		defaultValue      json.RawMessage
		rolloutPercentage int
		updatedAt         time.Time
	}

	var configs []configRow
	for rows.Next() {
		var r configRow
		if err := rows.Scan(
			&r.flagKey, &r.flagType, &r.configID,
			&r.enabled, &r.defaultValue, &r.rolloutPercentage, &r.updatedAt,
		); err != nil {
			return 0, fmt.Errorf("scan config row: %w", err)
		}
		configs = append(configs, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// For each config, fetch targeting rules and write to Redis
	for _, cfg := range configs {
		rules, err := m.queryTargetingRules(ctx, cfg.configID)
		if err != nil {
			log.Warn().Err(err).Str("flag", cfg.flagKey).Msg("materializer: failed to fetch rules, using empty")
			rules = []TargetingRuleState{}
		}

		state := FlagState{
			FlagKey:           cfg.flagKey,
			FlagType:          cfg.flagType,
			Enabled:           cfg.enabled,
			DefaultValue:      cfg.defaultValue,
			RolloutPercentage: cfg.rolloutPercentage,
			TargetingRules:    rules,
			UpdatedAt:         cfg.updatedAt,
		}

		if err := m.cache.SetFlagState(ctx, envName, state); err != nil {
			log.Warn().Err(err).Str("flag", cfg.flagKey).Str("env", envName).
				Msg("materializer: failed to write flag state to Redis, skipping")
		}
	}

	return len(configs), nil
}

// MaterializeFlag re-materializes a single flag for a single environment.
// Called when a LISTEN/NOTIFY event arrives — we only re-read the changed flag,
// not everything. This keeps invalidation latency low (one DB round-trip).
func (m *Materializer) MaterializeFlag(ctx context.Context, flagKey, envName string) error {
	start := time.Now()

	// Single query: join flags + flag_configs + environments
	var state FlagState
	var configID string

	err := m.db.QueryRow(ctx, `
		SELECT
			f.key, f.flag_type,
			fc.id, fc.enabled, fc.default_value, fc.rollout_percentage, fc.updated_at
		FROM flag_configs fc
		JOIN flags f       ON f.id  = fc.flag_id
		JOIN environments e ON e.id = fc.environment_id
		WHERE f.key = $1 AND e.name = $2
	`, flagKey, envName).Scan(
		&state.FlagKey, &state.FlagType,
		&configID, &state.Enabled, &state.DefaultValue,
		&state.RolloutPercentage, &state.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("query flag config for %s/%s: %w", flagKey, envName, err)
	}

	// Fetch targeting rules for this config
	rules, err := m.queryTargetingRules(ctx, configID)
	if err != nil {
		log.Warn().Err(err).Str("flag", flagKey).Msg("materializeFlag: failed to fetch rules, using empty")
		rules = []TargetingRuleState{}
	}
	state.TargetingRules = rules

	if err := m.cache.SetFlagState(ctx, envName, state); err != nil {
		return fmt.Errorf("write to Redis: %w", err)
	}

	log.Info().
		Str("flag", flagKey).
		Str("env", envName).
		Bool("enabled", state.Enabled).
		Dur("latency", time.Since(start)).
		Msg("materializer: single flag re-materialized")

	return nil
}

// queryTargetingRules fetches and converts targeting rules for a flag config.
func (m *Materializer) queryTargetingRules(ctx context.Context, configID string) ([]TargetingRuleState, error) {
	rows, err := m.db.Query(ctx, `
		SELECT priority, attribute, operator, value, serve_value
		FROM targeting_rules
		WHERE flag_config_id = $1
		ORDER BY priority ASC
	`, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []TargetingRuleState
	for rows.Next() {
		var r TargetingRuleState
		if err := rows.Scan(&r.Priority, &r.Attribute, &r.Operator, &r.Value, &r.ServeValue); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	if rules == nil {
		rules = []TargetingRuleState{}
	}
	return rules, rows.Err()
}
