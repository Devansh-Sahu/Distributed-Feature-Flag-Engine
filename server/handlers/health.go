package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// HealthHandler handles GET /health
// Returns a JSON body with the status of each dependency.
// Used by Docker healthcheck and load balancers.
type HealthHandler struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

type healthResponse struct {
	Status     string                 `json:"status"` // "ok" | "degraded"
	Version    string                 `json:"version"`
	Timestamp  time.Time              `json:"timestamp"`
	Components map[string]healthCheck `json:"components"`
}

type healthCheck struct {
	Status  string `json:"status"`  // "ok" | "error"
	Latency string `json:"latency"` // e.g. "2ms"
	Error   string `json:"error,omitempty"`
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	overall := "ok"
	components := make(map[string]healthCheck)

	// ── PostgreSQL check ──────────────────────────────────────────
	pgStart := time.Now()
	pgErr := h.DB.Ping(ctx)
	pgLatency := time.Since(pgStart)
	if pgErr != nil {
		overall = "degraded"
		components["postgres"] = healthCheck{
			Status:  "error",
			Latency: pgLatency.String(),
			Error:   pgErr.Error(),
		}
	} else {
		components["postgres"] = healthCheck{
			Status:  "ok",
			Latency: pgLatency.String(),
		}
	}

	// ── Redis check ───────────────────────────────────────────────
	redisStart := time.Now()
	_, redisErr := h.Redis.Ping(ctx).Result()
	redisLatency := time.Since(redisStart)
	if redisErr != nil {
		overall = "degraded"
		components["redis"] = healthCheck{
			Status:  "error",
			Latency: redisLatency.String(),
			Error:   redisErr.Error(),
		}
	} else {
		components["redis"] = healthCheck{
			Status:  "ok",
			Latency: redisLatency.String(),
		}
	}

	status := http.StatusOK
	if overall == "degraded" {
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(healthResponse{
		Status:     overall,
		Version:    "1.0.0",
		Timestamp:  time.Now().UTC(),
		Components: components,
	})
}
