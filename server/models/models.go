package models

import (
	"encoding/json"
	"time"
)

// ── Environment ────────────────────────────────────────────────────

type Environment struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// ── Flag ───────────────────────────────────────────────────────────

type FlagType string

const (
	FlagTypeBoolean FlagType = "boolean"
	FlagTypeString  FlagType = "string"
	FlagTypeNumber  FlagType = "number"
	FlagTypeJSON    FlagType = "json"
)

type Flag struct {
	ID          string    `json:"id" db:"id"`
	Key         string    `json:"key" db:"key"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	FlagType    FlagType  `json:"flag_type" db:"flag_type"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// CreateFlagRequest is the JSON body for POST /api/v1/flags
type CreateFlagRequest struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	FlagType    FlagType `json:"flag_type"`
}

func (r *CreateFlagRequest) Validate() []string {
	var errs []string
	if r.Key == "" {
		errs = append(errs, "key is required")
	}
	if r.Name == "" {
		errs = append(errs, "name is required")
	}
	if r.FlagType == "" {
		r.FlagType = FlagTypeBoolean // default
	}
	validTypes := map[FlagType]bool{
		FlagTypeBoolean: true,
		FlagTypeString:  true,
		FlagTypeNumber:  true,
		FlagTypeJSON:    true,
	}
	if !validTypes[r.FlagType] {
		errs = append(errs, "flag_type must be one of: boolean, string, number, json")
	}
	return errs
}

// UpdateFlagRequest is the JSON body for PATCH /api/v1/flags/{key}
type UpdateFlagRequest struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	FlagType    FlagType `json:"flag_type,omitempty"`
}

// ── FlagConfig ─────────────────────────────────────────────────────

// FlagConfig is the per-environment configuration for a flag.
// This is the HOT ROW — it's watched by Debezium in Phase 3.
type FlagConfig struct {
	ID                string          `json:"id" db:"id"`
	FlagID            string          `json:"flag_id" db:"flag_id"`
	EnvironmentID     string          `json:"environment_id" db:"environment_id"`
	Enabled           bool            `json:"enabled" db:"enabled"`
	DefaultValue      json.RawMessage `json:"default_value" db:"default_value"`
	RolloutPercentage int             `json:"rollout_percentage" db:"rollout_percentage"`
	UpdatedAt         time.Time       `json:"updated_at" db:"updated_at"`
}

// UpdateFlagConfigRequest is the body for PATCH /api/v1/flags/{key}/config/{env}
type UpdateFlagConfigRequest struct {
	Enabled           *bool           `json:"enabled,omitempty"`
	DefaultValue      json.RawMessage `json:"default_value,omitempty"`
	RolloutPercentage *int            `json:"rollout_percentage,omitempty"`
}

// FlagWithConfig is the denormalized view returned by GET /api/v1/flags
// It joins flag + all its configs + targeting rules into one response.
type FlagWithConfig struct {
	Flag
	Configs []FlagConfigWithRules `json:"configs"`
}

type FlagConfigWithRules struct {
	FlagConfig
	EnvironmentName string          `json:"environment_name"`
	TargetingRules  []TargetingRule `json:"targeting_rules"`
}

// ── TargetingRule ──────────────────────────────────────────────────

type Operator string

const (
	OperatorEq         Operator = "eq"
	OperatorNeq        Operator = "neq"
	OperatorIn         Operator = "in"
	OperatorNotIn      Operator = "not_in"
	OperatorGt         Operator = "gt"
	OperatorLt         Operator = "lt"
	OperatorGte        Operator = "gte"
	OperatorLte        Operator = "lte"
	OperatorContains   Operator = "contains"
	OperatorStartsWith Operator = "starts_with"
)

type TargetingRule struct {
	ID            string          `json:"id" db:"id"`
	FlagConfigID  string          `json:"flag_config_id" db:"flag_config_id"`
	Priority      int             `json:"priority" db:"priority"`
	Attribute     string          `json:"attribute" db:"attribute"`
	Operator      Operator        `json:"operator" db:"operator"`
	Value         json.RawMessage `json:"value" db:"value"`
	ServeValue    json.RawMessage `json:"serve_value" db:"serve_value"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
}

// CreateTargetingRuleRequest is the body for POST /api/v1/flags/{key}/rules
type CreateTargetingRuleRequest struct {
	EnvironmentID string          `json:"environment_id"`
	Priority      int             `json:"priority"`
	Attribute     string          `json:"attribute"`
	Operator      Operator        `json:"operator"`
	Value         json.RawMessage `json:"value"`
	ServeValue    json.RawMessage `json:"serve_value"`
}

func (r *CreateTargetingRuleRequest) Validate() []string {
	var errs []string
	if r.EnvironmentID == "" {
		errs = append(errs, "environment_id is required")
	}
	if r.Attribute == "" {
		errs = append(errs, "attribute is required")
	}
	validOps := map[Operator]bool{
		OperatorEq: true, OperatorNeq: true,
		OperatorIn: true, OperatorNotIn: true,
		OperatorGt: true, OperatorLt: true,
		OperatorGte: true, OperatorLte: true,
		OperatorContains: true, OperatorStartsWith: true,
	}
	if !validOps[r.Operator] {
		errs = append(errs, "operator must be one of: eq,neq,in,not_in,gt,lt,gte,lte,contains,starts_with")
	}
	if len(r.Value) == 0 {
		errs = append(errs, "value is required")
	}
	if len(r.ServeValue) == 0 {
		errs = append(errs, "serve_value is required")
	}
	return errs
}

// ── AuditLog ───────────────────────────────────────────────────────

type AuditLog struct {
	ID            int64           `json:"id" db:"id"`
	FlagKey       string          `json:"flag_key" db:"flag_key"`
	EnvironmentID *string         `json:"environment_id,omitempty" db:"environment_id"`
	Action        string          `json:"action" db:"action"`
	ChangedBy     string          `json:"changed_by" db:"changed_by"`
	OldValue      json.RawMessage `json:"old_value,omitempty" db:"old_value"`
	NewValue      json.RawMessage `json:"new_value,omitempty" db:"new_value"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
}

// ── Common API response envelopes ──────────────────────────────────

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type PaginatedResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Total   int         `json:"total"`
	Page    int         `json:"page"`
	Limit   int         `json:"limit"`
}
