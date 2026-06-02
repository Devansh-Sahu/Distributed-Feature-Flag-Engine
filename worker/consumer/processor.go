package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// ─────────────────────────────────────────────────────────────────────────
// Processor: the brain of the worker.
//
// It receives a Debezium envelope, figures out which flag changed, fetches
// the complete flag state from Postgres (denormalized), and writes it to
// Redis as a HASH field — ready for SDK reads.
//
// WHY fetch from Postgres instead of reconstructing from the Debezium payload?
//
// The Debezium message for flag_configs contains the raw row: flag_id, env_id,
// enabled, rollout_percentage. But the SDK needs a denormalized FlagState that
// also includes: flag_key, env_name, flag_type, and ALL targeting rules.
//
// The flag_key comes from the flags table. The env_name comes from environments.
// The targeting rules come from targeting_rules. Joining all of this in the
// consumer would require either:
//   a) Keeping a local join cache (complex, stale-prone)
//   b) Fetching from Postgres (simple, always correct)
//
// We choose (b). The DB fetch happens ONCE per event on the write path.
// The SDK reads from Redis (zero DB). This is the right tradeoff.
// ─────────────────────────────────────────────────────────────────────────

// FlagState is the materialized, SDK-ready representation.
// This is written to Redis as: HSET ffee:state:{envName} {flagKey} <json>
type FlagState struct {
	FlagKey           string              `json:"flag_key"`
	FlagType          string              `json:"flag_type"`
	Enabled           bool                `json:"enabled"`
	DefaultValue      json.RawMessage     `json:"default_value"`
	RolloutPercentage int                 `json:"rollout_percentage"`
	TargetingRules    []TargetingRuleJSON `json:"targeting_rules"`
	MaterializedAt    time.Time           `json:"materialized_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

// TargetingRuleJSON is the SDK-ready form of a targeting rule.
type TargetingRuleJSON struct {
	Priority   int             `json:"priority"`
	Attribute  string          `json:"attribute"`
	Operator   string          `json:"operator"`
	Value      json.RawMessage `json:"value"`
	ServeValue json.RawMessage `json:"serve_value"`
}

// Processor handles Debezium events and writes to Redis.
type Processor struct {
	db    *pgxpool.Pool
	redis *redis.Client
}

// NewProcessor creates a new Processor.
func NewProcessor(db *pgxpool.Pool, rdb *redis.Client) *Processor {
	return &Processor{db: db, redis: rdb}
}

// HandleFlagConfig processes a Debezium event from the flag_configs topic.
func (p *Processor) HandleFlagConfig(ctx context.Context, msg []byte) error {
	var envelope DebeziumEnvelope
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return fmt.Errorf("unmarshal debezium envelope: %w", err)
	}

	switch envelope.Op {
	case OpDelete:
		// On DELETE, "after" is null. Parse "before" to know which flag was deleted.
		return p.handleFlagConfigDelete(ctx, envelope.Before)

	case OpRead, OpCreate, OpUpdate:
		return p.handleFlagConfigUpsert(ctx, envelope.After)

	default:
		log.Warn().Str("op", string(envelope.Op)).Msg("worker: unknown Debezium op, skipping")
		return nil
	}
}

// HandleTargetingRule processes a Debezium event from the targeting_rules topic.
// A targeting rule change requires re-materializing its parent flag_config.
func (p *Processor) HandleTargetingRule(ctx context.Context, msg []byte) error {
	var envelope DebeziumEnvelope
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return fmt.Errorf("unmarshal debezium envelope: %w", err)
	}

	// For rules, we always re-materialize the parent flag regardless of op type.
	// For DELETE, use "before" to get the flag_config_id.
	payload := envelope.After
	if envelope.Op == OpDelete {
		payload = envelope.Before
	}
	if payload == nil {
		return nil
	}

	var rule TargetingRuleRow
	if err := json.Unmarshal(payload, &rule); err != nil {
		return fmt.Errorf("unmarshal targeting rule: %w", err)
	}
	if rule.FlagConfigID == "" {
		return nil
	}

	// Resolve the flag_config_id → flag_key + env_name, then re-materialize.
	return p.materializeByConfigID(ctx, rule.FlagConfigID)
}

// ── Private helpers ───────────────────────────────────────────────────────

func (p *Processor) handleFlagConfigUpsert(ctx context.Context, payload json.RawMessage) error {
	if payload == nil {
		return nil
	}
	var row FlagConfigRow
	if err := json.Unmarshal(payload, &row); err != nil {
		return fmt.Errorf("unmarshal flag_config row: %w", err)
	}
	return p.materializeByConfigID(ctx, row.ID)
}

func (p *Processor) handleFlagConfigDelete(ctx context.Context, payload json.RawMessage) error {
	if payload == nil {
		return nil
	}
	var row FlagConfigRow
	if err := json.Unmarshal(payload, &row); err != nil {
		return fmt.Errorf("unmarshal deleted flag_config: %w", err)
	}

	// Resolve flag_key and env_name from IDs before the row is gone.
	// Note: on CASCADE DELETE from flags table, the flag is already deleted
	// from Postgres. We do a best-effort HDEL from Redis.
	flagKey, envName, err := p.resolveFlagAndEnv(ctx, row.FlagID, row.EnvironmentID)
	if err != nil {
		log.Warn().Err(err).Str("flag_id", row.FlagID).Msg("worker: could not resolve deleted flag, skipping Redis cleanup")
		return nil
	}

	key := "ffee:state:" + envName
	if err := p.redis.HDel(ctx, key, flagKey).Err(); err != nil {
		return fmt.Errorf("redis HDEL: %w", err)
	}
	// Notify SDKs that this flag was deleted
	deleteMsg, _ := json.Marshal(map[string]interface{}{
		"flag_key": flagKey,
		"deleted":  true,
	})
	p.redis.Publish(ctx, "ffee:updates:"+envName, deleteMsg)

	log.Info().Str("flag", flagKey).Str("env", envName).Msg("worker: flag config deleted from Redis")
	return nil
}

// materializeByConfigID is the core materialization path.
// Given a flag_config.id, it fetches the full denormalized state from Postgres
// and writes it to Redis in a single pipeline (HSET + PUBLISH).
func (p *Processor) materializeByConfigID(ctx context.Context, configID string) error {
	start := time.Now()

	// Fetch the complete flag state in one query (flag + config + environment)
	var state FlagState
	var envName string
	err := p.db.QueryRow(ctx, `
		SELECT
			f.key, f.flag_type,
			fc.enabled, fc.default_value, fc.rollout_percentage, fc.updated_at,
			e.name
		FROM flag_configs fc
		JOIN flags f        ON f.id  = fc.flag_id
		JOIN environments e ON e.id  = fc.environment_id
		WHERE fc.id = $1
	`, configID).Scan(
		&state.FlagKey, &state.FlagType,
		&state.Enabled, &state.DefaultValue,
		&state.RolloutPercentage, &state.UpdatedAt,
		&envName,
	)
	if err == pgx.ErrNoRows {
		// Flag config was deleted before we could read it — safe to ignore
		log.Debug().Str("config_id", configID).Msg("worker: flag_config not found (already deleted?)")
		return nil
	}
	if err != nil {
		return fmt.Errorf("query flag config %s: %w", configID, err)
	}
	state.MaterializedAt = time.Now().UTC()

	// Step 2: Fetch targeting rules
	rows, err := p.db.Query(ctx, `
		SELECT priority, attribute, operator, value, serve_value
		FROM targeting_rules
		WHERE flag_config_id = $1
		ORDER BY priority ASC
	`, configID)
	if err != nil {
		return fmt.Errorf("query targeting rules for config %s: %w", configID, err)
	}
	defer rows.Close()

	state.TargetingRules = []TargetingRuleJSON{}
	for rows.Next() {
		var r TargetingRuleJSON
		if err := rows.Scan(&r.Priority, &r.Attribute, &r.Operator, &r.Value, &r.ServeValue); err != nil {
			return fmt.Errorf("scan targeting rule: %w", err)
		}
		state.TargetingRules = append(state.TargetingRules, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Step 3: Serialize and write to Redis atomically (HSET + PUBLISH in one pipeline)
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal flag state: %w", err)
	}

	pipe := p.redis.Pipeline()
	pipe.HSet(ctx, "ffee:state:"+envName, state.FlagKey, data)
	pipe.Publish(ctx, "ffee:updates:"+envName, data)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis pipeline: %w", err)
	}

	log.Info().
		Str("flag", state.FlagKey).
		Str("env", envName).
		Bool("enabled", state.Enabled).
		Int("rules", len(state.TargetingRules)).
		Dur("latency", time.Since(start)).
		Msg("worker: flag materialized to Redis via Kafka CDC")

	return nil
}

// resolveFlagAndEnv looks up flag_key and env_name by their IDs.
// Used for DELETE events where the row may already be gone.
func (p *Processor) resolveFlagAndEnv(ctx context.Context, flagID, envID string) (flagKey, envName string, err error) {
	err = p.db.QueryRow(ctx,
		`SELECT f.key, e.name FROM flags f, environments e WHERE f.id = $1 AND e.id = $2`,
		flagID, envID,
	).Scan(&flagKey, &envName)
	return
}
