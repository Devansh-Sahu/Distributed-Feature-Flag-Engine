package cache

import (
	"encoding/json"
	"time"

	"github.com/devansh/feature-flag-engine/server/models"
)

// FlagState is the materialized, SDK-ready representation of a flag in one environment.
// This is what lives in Redis and what the SDK caches in memory.
//
// Design decision: we denormalize here intentionally. The SDK should NEVER
// need to make a second lookup — one Redis HGET gives everything needed to
// evaluate a flag for any user. This is the key to sub-0.5ms evaluation.
type FlagState struct {
	// Identity
	FlagKey  string          `json:"flag_key"`
	FlagType models.FlagType `json:"flag_type"`

	// Evaluation fields
	Enabled           bool            `json:"enabled"`
	DefaultValue      json.RawMessage `json:"default_value"`
	RolloutPercentage int             `json:"rollout_percentage"`

	// Targeting rules, ordered by priority ascending (lower priority number = evaluated first).
	// Stored denormalized so the SDK evaluates with zero DB/Redis lookups.
	TargetingRules []TargetingRuleState `json:"targeting_rules"`

	// For debugging/observability: when was this state last written to Redis?
	MaterializedAt time.Time `json:"materialized_at"`
	// When was the underlying flag_config row last updated in Postgres?
	UpdatedAt time.Time `json:"updated_at"`
}

// TargetingRuleState is the SDK-ready form of a targeting rule.
// Mirrors models.TargetingRule but without DB-specific fields.
type TargetingRuleState struct {
	Priority   int              `json:"priority"`
	Attribute  string           `json:"attribute"`
	Operator   models.Operator  `json:"operator"`
	Value      json.RawMessage  `json:"value"`
	ServeValue json.RawMessage  `json:"serve_value"`
}

// NotifyPayload is the JSON payload sent via Postgres LISTEN/NOTIFY
// on the 'flag_config_changed' channel.
type NotifyPayload struct {
	FlagKey       string `json:"flag_key"`
	EnvName       string `json:"env_name"`
	FlagConfigID  string `json:"flag_config_id"`
	Reason        string `json:"reason"` // "targeting_rule_changed" for rule events
}
