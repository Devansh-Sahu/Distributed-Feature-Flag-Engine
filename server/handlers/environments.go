package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/models"
)

// EnvironmentHandler handles /api/v1/environments routes.
type EnvironmentHandler struct {
	DB *pgxpool.Pool
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/environments
// ─────────────────────────────────────────────────────────────────────
func (h *EnvironmentHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.DB.Query(ctx, `
		SELECT id, name, description, created_at
		FROM environments
		ORDER BY name
	`)
	if err != nil {
		log.Error().Err(err).Msg("list environments")
		writeError(w, http.StatusInternalServerError, "failed to list environments")
		return
	}
	defer rows.Close()

	var envs []models.Environment
	for rows.Next() {
		var e models.Environment
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &e.CreatedAt); err != nil {
			log.Error().Err(err).Msg("scan environment row")
			continue
		}
		envs = append(envs, e)
	}

	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: envs})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/environments/{name}
// ─────────────────────────────────────────────────────────────────────
func (h *EnvironmentHandler) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	var env models.Environment
	err := h.DB.QueryRow(ctx, `
		SELECT id, name, description, created_at
		FROM environments WHERE name = $1
	`, name).Scan(&env.ID, &env.Name, &env.Description, &env.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		log.Error().Err(err).Str("name", name).Msg("get environment")
		writeError(w, http.StatusInternalServerError, "failed to get environment")
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: env})
}

// ─────────────────────────────────────────────────────────────────────
// POST /api/v1/environments
// Creates a new environment and bootstraps a FlagConfig for every existing flag.
// WHY? Consistency: every flag always has a config in every environment.
// ─────────────────────────────────────────────────────────────────────
func (h *EnvironmentHandler) CreateEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx)

	var env models.Environment
	err = tx.QueryRow(ctx, `
		INSERT INTO environments (name, description)
		VALUES ($1, $2)
		RETURNING id, name, description, created_at
	`, req.Name, req.Description).Scan(&env.ID, &env.Name, &env.Description, &env.CreatedAt)
	if err != nil {
		if containsErr(err, "duplicate key", "23505") {
			writeError(w, http.StatusConflict, "an environment with this name already exists")
			return
		}
		log.Error().Err(err).Msg("create environment: insert")
		writeError(w, http.StatusInternalServerError, "failed to create environment")
		return
	}

	// Bootstrap a FlagConfig for every existing flag in this new environment.
	// Without this, existing flags would have no config for the new env
	// and the SDK would silently fall back to defaults for all flags.
	_, err = tx.Exec(ctx, `
		INSERT INTO flag_configs (flag_id, environment_id, enabled, default_value, rollout_percentage)
		SELECT id, $1, false, 'false'::jsonb, 0
		FROM flags
		ON CONFLICT (flag_id, environment_id) DO NOTHING
	`, env.ID)
	if err != nil {
		log.Error().Err(err).Msg("create environment: bootstrap flag configs")
		writeError(w, http.StatusInternalServerError, "failed to bootstrap flag configs for new environment")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	log.Info().Str("name", env.Name).Str("id", env.ID).Msg("environment created")
	writeJSON(w, http.StatusCreated, models.APIResponse{Success: true, Data: env})
}

// ─────────────────────────────────────────────────────────────────────
// DELETE /api/v1/environments/{name}
// Deletes an environment. Cannot delete the three seed environments.
// ─────────────────────────────────────────────────────────────────────
func (h *EnvironmentHandler) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	// Protect seed environments
	protected := map[string]bool{"production": true, "staging": true, "development": true}
	if protected[name] {
		writeError(w, http.StatusForbidden, "cannot delete a seed environment (production, staging, development)")
		return
	}

	result, err := h.DB.Exec(ctx, `DELETE FROM environments WHERE name = $1`, name)
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("delete environment")
		writeError(w, http.StatusInternalServerError, "failed to delete environment")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data:    map[string]string{"message": "environment deleted"},
	})
}

// containsErr checks if an error message contains any of the given substrings.
func containsErr(err error, substrings ...string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range substrings {
		found := false
		for i := 0; i <= len(msg)-len(s); i++ {
			if msg[i:i+len(s)] == s {
				found = true
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}
