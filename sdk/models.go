package ffee

import "encoding/json"

// FlagState is the complete, denormalized state of a flag for one environment.
// This is what the server stores in Redis and what the SDK holds in memory.
type FlagState struct {
	FlagKey           string              `json:"flag_key"`
	FlagType          string              `json:"flag_type"` // "boolean", "string", "number", "json"
	Enabled           bool                `json:"enabled"`
	DefaultValue      json.RawMessage     `json:"default_value"`
	RolloutPercentage int                 `json:"rollout_percentage"`
	TargetingRules    []TargetingRule     `json:"targeting_rules"`
	MaterializedAt    string              `json:"materialized_at"`
	UpdatedAt         string              `json:"updated_at"`
}

// TargetingRule is a single attribute-based rule.
// Rules are evaluated in ascending priority order (lower number = first).
type TargetingRule struct {
	Priority   int             `json:"priority"`
	Attribute  string          `json:"attribute"`   // e.g. "user.country", "plan"
	Operator   string          `json:"operator"`    // eq, neq, in, not_in, gt, lt, gte, lte, contains, starts_with
	Value      json.RawMessage `json:"value"`       // the rule's comparison value
	ServeValue json.RawMessage `json:"serve_value"` // value returned when this rule matches
}

// UserContext carries the user identity and attributes used for targeting rule
// evaluation and rollout bucket assignment.
//
// Example:
//
//	ctx := ffee.UserContext{
//	    UserID: "user-42",
//	    Attributes: map[string]any{
//	        "plan":         "pro",
//	        "user.country": "IN",
//	        "beta_tester":  true,
//	    },
//	}
type UserContext struct {
	// UserID is used as the seed for consistent percentage rollout.
	// The same UserID always lands in the same rollout bucket.
	// Required if RolloutPercentage < 100.
	UserID string

	// Attributes are arbitrary key-value pairs used by targeting rules.
	// Keys should match the "attribute" field in your targeting rules.
	Attributes map[string]any
}
