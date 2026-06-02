package consumer

import "encoding/json"

// ─────────────────────────────────────────────────────────────────────────
// Debezium message format
//
// When Debezium reads from the PostgreSQL WAL and produces to Kafka,
// each message looks like this (with schemas.enable=false):
//
//  {
//    "before": { ...old row values... } | null,
//    "after":  { ...new row values... } | null,   ← null on DELETE
//    "source": { "table": "flag_configs", ... },
//    "op":     "r" | "c" | "u" | "d",
//    "ts_ms":  1717000000000
//  }
//
// op values:
//   r = read   (snapshot initial load — treat same as insert)
//   c = create (INSERT)
//   u = update (UPDATE)
//   d = delete (DELETE) — "after" is null
//
// WHY not parse the full Debezium schema envelope?
// We set value.converter.schemas.enable=false in the connector config,
// so Debezium strips the schema and sends only the payload. This cuts
// message size by ~60% and simplifies consumer code significantly.
// ─────────────────────────────────────────────────────────────────────────

// DebeziumOp represents the type of WAL change.
type DebeziumOp string

const (
	OpRead   DebeziumOp = "r" // initial snapshot read
	OpCreate DebeziumOp = "c" // INSERT
	OpUpdate DebeziumOp = "u" // UPDATE
	OpDelete DebeziumOp = "d" // DELETE
)

// DebeziumEnvelope is the outer Kafka message payload from Debezium.
type DebeziumEnvelope struct {
	Before json.RawMessage `json:"before"` // null for inserts and snapshot reads
	After  json.RawMessage `json:"after"`  // null for deletes
	Source DebeziumSource  `json:"source"`
	Op     DebeziumOp      `json:"op"`
	TsMs   int64           `json:"ts_ms"`
}

// DebeziumSource contains metadata about where the event came from.
type DebeziumSource struct {
	Table string `json:"table"`
	DB    string `json:"db"`
}

// FlagConfigRow is the after/before payload for the flag_configs table.
// Field names must match Postgres column names exactly (Debezium uses them as JSON keys).
type FlagConfigRow struct {
	ID                string          `json:"id"`
	FlagID            string          `json:"flag_id"`
	EnvironmentID     string          `json:"environment_id"`
	Enabled           bool            `json:"enabled"`
	DefaultValue      json.RawMessage `json:"default_value"`
	RolloutPercentage int             `json:"rollout_percentage"`
	UpdatedAt         interface{}     `json:"updated_at"` // Debezium sends as microseconds epoch; we ignore it
}

// TargetingRuleRow is the after/before payload for the targeting_rules table.
type TargetingRuleRow struct {
	ID           string          `json:"id"`
	FlagConfigID string          `json:"flag_config_id"`
	Priority     int             `json:"priority"`
	Attribute    string          `json:"attribute"`
	Operator     string          `json:"operator"`
	Value        json.RawMessage `json:"value"`
	ServeValue   json.RawMessage `json:"serve_value"`
}
