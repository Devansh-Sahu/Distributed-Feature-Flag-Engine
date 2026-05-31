package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/cache"
	"github.com/devansh/feature-flag-engine/server/models"
)

// CacheHandler handles cache-related endpoints.
// This is a NEW handler for Phase 2 — it reads from Redis (hot path) and
// exposes a benchmark endpoint that proves cache vs DB latency difference.
type CacheHandler struct {
	DB           *pgxpool.Pool
	Cache        *cache.RedisCache
	Materializer *cache.Materializer
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/flags/{key}/state/{envName}
// Returns the materialized FlagState from Redis.
// This is what the Go SDK will call on startup (Phase 4).
// Hot path: Redis read only, no Postgres.
// ─────────────────────────────────────────────────────────────────────
func (h *CacheHandler) GetFlagState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	envName := chi.URLParam(r, "envName")

	start := time.Now()
	state, err := h.Cache.GetFlagState(ctx, envName, key)
	redisLatency := time.Since(start)

	if err != nil {
		log.Error().Err(err).Str("flag", key).Str("env", envName).Msg("get flag state: redis error")
		writeError(w, http.StatusInternalServerError, "failed to read flag state from cache")
		return
	}

	if state == nil {
		// Cache miss: flag not in Redis yet (e.g. server restarted mid-flight)
		// Fall back to Postgres and re-materialize
		log.Warn().Str("flag", key).Str("env", envName).Msg("cache miss: re-materializing from DB")
		if err := h.Materializer.MaterializeFlag(ctx, key, envName); err != nil {
			writeError(w, http.StatusNotFound, "flag not found in cache or database")
			return
		}
		state, err = h.Cache.GetFlagState(ctx, envName, key)
		if err != nil || state == nil {
			writeError(w, http.StatusNotFound, "flag not found")
			return
		}
	}

	// Expose Redis read latency in response header — useful for SDK instrumentation
	w.Header().Set("X-Cache-Latency-Us", formatMicros(redisLatency))
	w.Header().Set("X-Cache-Hit", "true")
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: state})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/flags/{key}/state-bulk/{envName}
// Returns ALL flag states for an environment in one Redis HGETALL.
// This is the SDK bootstrap endpoint — called once when SDK starts.
// ─────────────────────────────────────────────────────────────────────
func (h *CacheHandler) GetAllFlagStates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	envName := chi.URLParam(r, "envName")

	start := time.Now()
	states, err := h.Cache.GetAllFlagStates(ctx, envName)
	redisLatency := time.Since(start)

	if err != nil {
		log.Error().Err(err).Str("env", envName).Msg("get all flag states: redis error")
		writeError(w, http.StatusInternalServerError, "failed to read flag states from cache")
		return
	}

	w.Header().Set("X-Cache-Latency-Us", formatMicros(redisLatency))
	w.Header().Set("X-Flag-Count", formatInt(len(states)))
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: states})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/benchmark/{key}?env=production&iterations=1000
//
// Benchmarks Redis (cache) vs Postgres (DB) read latency for a flag.
// Runs `iterations` reads against each and reports p50/p99 latencies.
//
// This is the "prove the cache matters" endpoint.
// Expected results on local hardware:
//   - Redis:    p50 ~200µs, p99 ~500µs
//   - Postgres: p50 ~2ms,   p99 ~10ms
//   - Speedup:  ~10-50x
// ─────────────────────────────────────────────────────────────────────
func (h *CacheHandler) Benchmark(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	envName := r.URL.Query().Get("env")
	if envName == "" {
		envName = "production"
	}
	iterations := 100
	if v := r.URL.Query().Get("iterations"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 && n <= 10000 {
			iterations = n
		}
	}

	// ── Redis benchmark ──────────────────────────────────────────────
	redisLatencies := make([]time.Duration, iterations)
	for i := range redisLatencies {
		start := time.Now()
		_, _ = h.Cache.GetFlagState(ctx, envName, key)
		redisLatencies[i] = time.Since(start)
	}

	// ── Postgres benchmark ───────────────────────────────────────────
	pgLatencies := make([]time.Duration, iterations)
	for i := range pgLatencies {
		start := time.Now()
		_, _ = h.queryFlagStateFromDB(ctx, key, envName)
		pgLatencies[i] = time.Since(start)
	}

	result := map[string]interface{}{
		"flag_key":   key,
		"env":        envName,
		"iterations": iterations,
		"redis": map[string]interface{}{
			"p50_us":  p50Micros(redisLatencies),
			"p99_us":  p99Micros(redisLatencies),
			"mean_us": meanMicros(redisLatencies),
		},
		"postgres": map[string]interface{}{
			"p50_us":  p50Micros(pgLatencies),
			"p99_us":  p99Micros(pgLatencies),
			"mean_us": meanMicros(pgLatencies),
		},
		"speedup_p50": float64(p50Micros(pgLatencies)) / float64(max(p50Micros(redisLatencies), 1)),
		"speedup_p99": float64(p99Micros(pgLatencies)) / float64(max(p99Micros(redisLatencies), 1)),
	}

	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: result})
}

// ─────────────────────────────────────────────────────────────────────
// GET /api/v1/cache/status
// Returns cache metadata: flag counts per environment, key sizes.
// ─────────────────────────────────────────────────────────────────────
func (h *CacheHandler) CacheStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all environment names
	rows, err := h.DB.Query(ctx, `SELECT name FROM environments ORDER BY name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query environments")
		return
	}
	defer rows.Close()

	type envStatus struct {
		Name          string `json:"name"`
		CachedFlags   int64  `json:"cached_flags"`
		RedisKey      string `json:"redis_key"`
		UpdateChannel string `json:"update_channel"`
	}

	var statuses []envStatus
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		count, _ := h.Cache.FlagCount(ctx, name)
		statuses = append(statuses, envStatus{
			Name:          name,
			CachedFlags:   count,
			RedisKey:      cache.StateKey(name),
			UpdateChannel: cache.UpdatesChannel(name),
		})
	}

	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: statuses})
}

// ─────────────────────────────────────────────────────────────────────
// Private helpers
// ─────────────────────────────────────────────────────────────────────

// queryFlagStateFromDB does a direct Postgres read for benchmark comparison.
// Deliberately NOT using the cache — this measures raw DB latency.
func (h *CacheHandler) queryFlagStateFromDB(ctx context.Context, flagKey, envName string) (*cache.FlagState, error) {
	var state cache.FlagState
	var configID string // scanned but not used further; only needed for future rule lookup

	err := h.DB.QueryRow(ctx, `
		SELECT f.key, f.flag_type, fc.id, fc.enabled, fc.default_value, fc.rollout_percentage, fc.updated_at
		FROM flag_configs fc
		JOIN flags f       ON f.id  = fc.flag_id
		JOIN environments e ON e.id = fc.environment_id
		WHERE f.key = $1 AND e.name = $2
	`, flagKey, envName).Scan(
		&state.FlagKey, &state.FlagType, &configID,
		&state.Enabled, &state.DefaultValue, &state.RolloutPercentage, &state.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	_ = configID // suppress unused warning — kept for schema consistency
	return &state, err
}

// Percentile and stat helpers

func p50Micros(d []time.Duration) int64 {
	return percentileMicros(d, 50)
}

func p99Micros(d []time.Duration) int64 {
	return percentileMicros(d, 99)
}

func meanMicros(d []time.Duration) int64 {
	if len(d) == 0 {
		return 0
	}
	var sum time.Duration
	for _, v := range d {
		sum += v
	}
	return sum.Microseconds() / int64(len(d))
}

func percentileMicros(d []time.Duration, pct int) int64 {
	if len(d) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(d))
	copy(sorted, d)
	sortDurations(sorted)
	idx := (pct * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx].Microseconds()
}

func sortDurations(d []time.Duration) {
	// Simple insertion sort — fine for <= 10000 elements
	for i := 1; i < len(d); i++ {
		key := d[i]
		j := i - 1
		for j >= 0 && d[j] > key {
			d[j+1] = d[j]
			j--
		}
		d[j+1] = key
	}
}

func formatMicros(d time.Duration) string {
	return formatInt64(d.Microseconds()) + "µs"
}

func formatInt(n int) string {
	return formatInt64(int64(n))
}

func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("parseInt: empty string")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("parseInt: not a number: %q", string(c))
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
