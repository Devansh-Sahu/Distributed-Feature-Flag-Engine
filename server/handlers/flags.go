package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	appmetrics "github.com/devansh/feature-flag-engine/server/metrics"
	"github.com/devansh/feature-flag-engine/server/models"
)

// FlagHandler handles all /api/v1/flags routes.
// It holds a reference to the DB pool only — no service layer in Phase 1.
// Phase 2 will introduce a service layer when caching logic is needed.
type FlagHandler struct {
	DB *pgxpool.Pool
}

// ── Helper: write JSON response ────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Error().Err(err).Msg("failed to encode JSON response")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, models.APIResponse{Success: false, Error: msg})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/flags
// Returns all flags with their configs and targeting rules per environment.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) ListFlags(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	timer := time.Now()

	flags, err := h.queryFlags(ctx)
	if err != nil {
		log.Error().Err(err).Msg("list flags: query flags")
		writeError(w, http.StatusInternalServerError, "failed to list flags")
		return
	}

	// For each flag, fetch configs and targeting rules.
	// N+1 is intentional here: this admin endpoint is read-rarely,
	// not on the hot evaluation path. Simple > clever for Phase 1.
	result := make([]models.FlagWithConfig, 0, len(flags))
	for _, f := range flags {
		configs, err := h.queryFlagConfigs(ctx, f.ID)
		if err != nil {
			log.Error().Err(err).Str("flag_id", f.ID).Msg("list flags: query configs")
			writeError(w, http.StatusInternalServerError, "failed to load flag configs")
			return
		}
		result = append(result, models.FlagWithConfig{
			Flag:    f,
			Configs: configs,
		})
	}

	appmetrics.DBQueryDuration.WithLabelValues("list_flags").Observe(time.Since(timer).Seconds())
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: result})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/flags/{key}
// Returns a single flag by its string key (e.g. "new-checkout-flow").
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) GetFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")

	flag, err := h.queryFlagByKey(ctx, key)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "flag not found")
			return
		}
		log.Error().Err(err).Str("key", key).Msg("get flag: query")
		writeError(w, http.StatusInternalServerError, "failed to get flag")
		return
	}

	configs, err := h.queryFlagConfigs(ctx, flag.ID)
	if err != nil {
		log.Error().Err(err).Str("flag_id", flag.ID).Msg("get flag: query configs")
		writeError(w, http.StatusInternalServerError, "failed to load flag configs")
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data: models.FlagWithConfig{
			Flag:    flag,
			Configs: configs,
		},
	})
}

// ─────────────────────────────────────────────────────────────────────
// POST /api/v1/flags
// Creates a new flag and auto-creates a FlagConfig for every environment.
// WHY a transaction? Because if the INSERT into flag_configs fails,
// we'd have an orphan flag with no config — impossible to manage via the API.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) CreateFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req models.CreateFlagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if errs := req.Validate(); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Error:   strings.Join(errs, "; "),
		})
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx) // no-op if already committed

	// Insert the flag definition
	var flag models.Flag
	err = tx.QueryRow(ctx, `
		INSERT INTO flags (key, name, description, flag_type)
		VALUES ($1, $2, $3, $4)
		RETURNING id, key, name, description, flag_type, created_at, updated_at
	`, req.Key, req.Name, req.Description, req.FlagType).Scan(
		&flag.ID, &flag.Key, &flag.Name, &flag.Description,
		&flag.FlagType, &flag.CreatedAt, &flag.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			writeError(w, http.StatusConflict, "a flag with this key already exists")
			return
		}
		log.Error().Err(err).Msg("create flag: insert flag")
		writeError(w, http.StatusInternalServerError, "failed to create flag")
		return
	}

	// Fetch all environment IDs so we can bootstrap a FlagConfig for each
	envRows, err := tx.Query(ctx, `SELECT id FROM environments`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load environments")
		return
	}
	var envIDs []string
	for envRows.Next() {
		var eid string
		if err := envRows.Scan(&eid); err != nil {
			envRows.Close()
			writeError(w, http.StatusInternalServerError, "failed to scan environment")
			return
		}
		envIDs = append(envIDs, eid)
	}
	envRows.Close()
	if err := envRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read environments")
		return
	}

	// Default value depends on type: boolean flags default to false,
	// others to null until explicitly configured.
	defaultVal := json.RawMessage(`false`)
	if req.FlagType != models.FlagTypeBoolean {
		defaultVal = json.RawMessage(`null`)
	}

	for _, envID := range envIDs {
		_, err := tx.Exec(ctx, `
			INSERT INTO flag_configs (flag_id, environment_id, enabled, default_value, rollout_percentage)
			VALUES ($1, $2, false, $3, 0)
			ON CONFLICT (flag_id, environment_id) DO NOTHING
		`, flag.ID, envID, defaultVal)
		if err != nil {
			log.Error().Err(err).Str("env_id", envID).Msg("create flag: bootstrap config")
			writeError(w, http.StatusInternalServerError, "failed to bootstrap flag configs")
			return
		}
	}

	// Append to audit log inside the same transaction
	newVal, _ := json.Marshal(flag)
	_, err = tx.Exec(ctx, `
		INSERT INTO flag_audit_log (flag_key, action, changed_by, new_value)
		VALUES ($1, 'created', 'api', $2)
	`, flag.Key, newVal)
	if err != nil {
		log.Warn().Err(err).Msg("create flag: write audit log (non-fatal, continuing)")
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	appmetrics.FlagChangesTotal.WithLabelValues("created", flag.Key).Inc()
	log.Info().Str("key", flag.Key).Str("id", flag.ID).Msg("flag created")
	writeJSON(w, http.StatusCreated, models.APIResponse{Success: true, Data: flag})
}

// ─────────────────────────────────────────────────────────────────────
// PATCH /api/v1/flags/{key}
// Updates flag metadata: name and/or description. Not the config.
// Uses a dynamic SET clause — only fields present in JSON body are updated.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) UpdateFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")

	var req models.UpdateFlagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	setClauses := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argIdx := 1

	if req.Name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, *req.Name)
		argIdx++
	}
	if req.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *req.Description)
		argIdx++
	}

	if len(setClauses) == 1 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, key)
	query := fmt.Sprintf(
		"UPDATE flags SET %s WHERE key = $%d RETURNING id, key, name, description, flag_type, created_at, updated_at",
		strings.Join(setClauses, ", "),
		argIdx,
	)

	var flag models.Flag
	err := h.DB.QueryRow(ctx, query, args...).Scan(
		&flag.ID, &flag.Key, &flag.Name, &flag.Description,
		&flag.FlagType, &flag.CreatedAt, &flag.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "flag not found")
			return
		}
		log.Error().Err(err).Str("key", key).Msg("update flag")
		writeError(w, http.StatusInternalServerError, "failed to update flag")
		return
	}

	appmetrics.FlagChangesTotal.WithLabelValues("updated", flag.Key).Inc()
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: flag})
}

// ─────────────────────────────────────────────────────────────────────
// DELETE /api/v1/flags/{key}
// Hard-deletes a flag. Cascades to flag_configs and targeting_rules.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) DeleteFlag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")

	result, err := h.DB.Exec(ctx, `DELETE FROM flags WHERE key = $1`, key)
	if err != nil {
		log.Error().Err(err).Str("key", key).Msg("delete flag")
		writeError(w, http.StatusInternalServerError, "failed to delete flag")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "flag not found")
		return
	}

	appmetrics.FlagChangesTotal.WithLabelValues("deleted", key).Inc()
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    map[string]string{"message": "flag deleted"},
	})
}

// ─────────────────────────────────────────────────────────────────────
// PATCH /api/v1/flags/{key}/config/{envName}
// Updates enabled, default_value, and/or rollout_percentage for a flag in one env.
// This is the HOT WRITE — the row Debezium will watch via WAL in Phase 3.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) UpdateFlagConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	envName := chi.URLParam(r, "envName")

	var req models.UpdateFlagConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Resolve the flag config ID in one JOIN query
	var configID, flagKey string
	err := h.DB.QueryRow(ctx, `
		SELECT fc.id, f.key
		FROM flag_configs fc
		JOIN flags f       ON f.id  = fc.flag_id
		JOIN environments e ON e.id = fc.environment_id
		WHERE f.key = $1 AND e.name = $2
	`, key, envName).Scan(&configID, &flagKey)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "flag or environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to find flag config")
		return
	}

	setClauses := []string{"updated_at = NOW()"}
	args := []interface{}{}
	argIdx := 1

	if req.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIdx))
		args = append(args, *req.Enabled)
		argIdx++
	}
	if req.DefaultValue != nil {
		setClauses = append(setClauses, fmt.Sprintf("default_value = $%d", argIdx))
		args = append(args, req.DefaultValue)
		argIdx++
	}
	if req.RolloutPercentage != nil {
		if *req.RolloutPercentage < 0 || *req.RolloutPercentage > 100 {
			writeError(w, http.StatusBadRequest, "rollout_percentage must be between 0 and 100")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("rollout_percentage = $%d", argIdx))
		args = append(args, *req.RolloutPercentage)
		argIdx++
	}

	if len(setClauses) == 1 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, configID)
	query := fmt.Sprintf(
		"UPDATE flag_configs SET %s WHERE id = $%d RETURNING id, flag_id, environment_id, enabled, default_value, rollout_percentage, updated_at",
		strings.Join(setClauses, ", "),
		argIdx,
	)

	var config models.FlagConfig
	err = h.DB.QueryRow(ctx, query, args...).Scan(
		&config.ID, &config.FlagID, &config.EnvironmentID,
		&config.Enabled, &config.DefaultValue, &config.RolloutPercentage, &config.UpdatedAt,
	)
	if err != nil {
		log.Error().Err(err).Str("config_id", configID).Msg("update flag config")
		writeError(w, http.StatusInternalServerError, "failed to update flag config")
		return
	}

	// Determine what to call this in the audit log
	action := "updated"
	if req.Enabled != nil {
		if *req.Enabled {
			action = "enabled"
		} else {
			action = "disabled"
		}
	}

	// Write to audit log (non-fatal — don't fail the request if this fails)
	newVal, _ := json.Marshal(config)
	_, auditErr := h.DB.Exec(ctx, `
		INSERT INTO flag_audit_log (flag_key, environment_id, action, changed_by, new_value)
		VALUES ($1, $2, $3, 'api', $4)
	`, flagKey, config.EnvironmentID, action, newVal)
	if auditErr != nil {
		log.Warn().Err(auditErr).Msg("update flag config: write audit log (non-fatal)")
	}

	appmetrics.FlagChangesTotal.WithLabelValues(action, flagKey).Inc()
	log.Info().
		Str("flag", flagKey).
		Str("env", envName).
		Str("action", action).
		Msg("flag config updated")

	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: config})
}

// ─────────────────────────────────────────────────────────────────────
// POST /api/v1/flags/{key}/rules
// Adds a targeting rule to a flag for a specific environment.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) CreateTargetingRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")

	var req models.CreateTargetingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if errs := req.Validate(); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Error:   strings.Join(errs, "; "),
		})
		return
	}

	// Look up the flag_config_id for this flag + environment
	var configID string
	err := h.DB.QueryRow(ctx, `
		SELECT fc.id FROM flag_configs fc
		JOIN flags f       ON f.id  = fc.flag_id
		WHERE f.key = $1 AND fc.environment_id = $2
	`, key, req.EnvironmentID).Scan(&configID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "flag config for this environment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve flag config")
		return
	}

	var rule models.TargetingRule
	err = h.DB.QueryRow(ctx, `
		INSERT INTO targeting_rules (flag_config_id, priority, attribute, operator, value, serve_value)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, flag_config_id, priority, attribute, operator, value, serve_value, created_at
	`, configID, req.Priority, req.Attribute, req.Operator, req.Value, req.ServeValue).Scan(
		&rule.ID, &rule.FlagConfigID, &rule.Priority, &rule.Attribute,
		&rule.Operator, &rule.Value, &rule.ServeValue, &rule.CreatedAt,
	)
	if err != nil {
		log.Error().Err(err).Str("flag_key", key).Msg("create targeting rule")
		writeError(w, http.StatusInternalServerError, "failed to create targeting rule")
		return
	}

	appmetrics.FlagChangesTotal.WithLabelValues("rule_added", key).Inc()
	writeJSON(w, http.StatusCreated, models.APIResponse{Success: true, Data: rule})
}

// ─────────────────────────────────────────────────────────────────────
// DELETE /api/v1/flags/{key}/rules/{ruleID}
// Removes a targeting rule by ID. Verifies rule belongs to this flag
// to prevent cross-flag deletion via IDOR.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) DeleteTargetingRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	ruleID := chi.URLParam(r, "ruleID")

	result, err := h.DB.Exec(ctx, `
		DELETE FROM targeting_rules tr
		USING flag_configs fc, flags f
		WHERE tr.id            = $1
		  AND tr.flag_config_id = fc.id
		  AND fc.flag_id        = f.id
		  AND f.key             = $2
	`, ruleID, key)
	if err != nil {
		log.Error().Err(err).Str("rule_id", ruleID).Msg("delete targeting rule")
		writeError(w, http.StatusInternalServerError, "failed to delete targeting rule")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "rule not found for this flag")
		return
	}

	appmetrics.FlagChangesTotal.WithLabelValues("rule_removed", key).Inc()
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    map[string]string{"message": "rule deleted"},
	})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/flags/{key}/audit
// Returns the last 100 audit log entries for a flag, newest first.
// ─────────────────────────────────────────────────────────────────────
func (h *FlagHandler) GetAuditLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")

	rows, err := h.DB.Query(ctx, `
		SELECT id, flag_key, environment_id, action, changed_by,
		       old_value, new_value, created_at
		FROM flag_audit_log
		WHERE flag_key = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, key)
	if err != nil {
		log.Error().Err(err).Str("key", key).Msg("get audit log")
		writeError(w, http.StatusInternalServerError, "failed to get audit log")
		return
	}
	defer rows.Close()

	var logs []models.AuditLog
	for rows.Next() {
		var entry models.AuditLog
		if err := rows.Scan(
			&entry.ID, &entry.FlagKey, &entry.EnvironmentID,
			&entry.Action, &entry.ChangedBy,
			&entry.OldValue, &entry.NewValue, &entry.CreatedAt,
		); err != nil {
			log.Error().Err(err).Msg("scan audit log row")
			continue
		}
		logs = append(logs, entry)
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("audit log row iteration error")
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    logs,
	})
}

// ─────────────────────────────────────────────────────────────────────
// Private query helpers — pulled out to keep handlers thin
// ─────────────────────────────────────────────────────────────────────

func (h *FlagHandler) queryFlags(ctx context.Context) ([]models.Flag, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT id, key, name, description, flag_type, created_at, updated_at
		FROM flags
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flags []models.Flag
	for rows.Next() {
		var f models.Flag
		if err := rows.Scan(
			&f.ID, &f.Key, &f.Name, &f.Description,
			&f.FlagType, &f.CreatedAt, &f.UpdatedAt,
		); err != nil {
			return nil, err
		}
		flags = append(flags, f)
	}
	return flags, rows.Err()
}

func (h *FlagHandler) queryFlagByKey(ctx context.Context, key string) (models.Flag, error) {
	var f models.Flag
	err := h.DB.QueryRow(ctx, `
		SELECT id, key, name, description, flag_type, created_at, updated_at
		FROM flags WHERE key = $1
	`, key).Scan(
		&f.ID, &f.Key, &f.Name, &f.Description,
		&f.FlagType, &f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}

func (h *FlagHandler) queryFlagConfigs(ctx context.Context, flagID string) ([]models.FlagConfigWithRules, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT fc.id, fc.flag_id, fc.environment_id, fc.enabled,
		       fc.default_value, fc.rollout_percentage, fc.updated_at,
		       e.name AS environment_name
		FROM flag_configs fc
		JOIN environments e ON e.id = fc.environment_id
		WHERE fc.flag_id = $1
		ORDER BY e.name
	`, flagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []models.FlagConfigWithRules
	for rows.Next() {
		var c models.FlagConfigWithRules
		if err := rows.Scan(
			&c.ID, &c.FlagID, &c.EnvironmentID, &c.Enabled,
			&c.DefaultValue, &c.RolloutPercentage, &c.UpdatedAt,
			&c.EnvironmentName,
		); err != nil {
			return nil, err
		}

		rules, err := h.queryTargetingRules(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		c.TargetingRules = rules
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

func (h *FlagHandler) queryTargetingRules(ctx context.Context, configID string) ([]models.TargetingRule, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT id, flag_config_id, priority, attribute, operator, value, serve_value, created_at
		FROM targeting_rules
		WHERE flag_config_id = $1
		ORDER BY priority ASC
	`, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []models.TargetingRule
	for rows.Next() {
		var rule models.TargetingRule
		if err := rows.Scan(
			&rule.ID, &rule.FlagConfigID, &rule.Priority, &rule.Attribute,
			&rule.Operator, &rule.Value, &rule.ServeValue, &rule.CreatedAt,
		); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}
