package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// All metrics are registered with promauto which auto-registers them
// with the default Prometheus registry on package init.
//
// Naming convention: ffee_<subsystem>_<name>_<unit>
// This follows Prometheus naming best practices and makes Grafana queries
// readable: ffee_flag_evaluations_total, not just evaluations_total.

var (
	// ── HTTP API metrics ─────────────────────────────────────────

	// HTTPRequestsTotal counts every HTTP request by method, path and status.
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ffee_http_requests_total",
			Help: "Total number of HTTP requests processed by the flag API server.",
		},
		[]string{"method", "path", "status_code"},
	)

	// HTTPRequestDuration tracks API endpoint latency.
	// Use buckets tailored for a flag API: most reads should be < 10ms.
	HTTPRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ffee_http_request_duration_seconds",
			Help:    "Histogram of HTTP request durations for the flag API server.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
		[]string{"method", "path"},
	)

	// ── Flag evaluation metrics (used by SDK, pre-registered here) ──

	// FlagEvaluationsTotal counts every flag evaluation.
	// Labels allow Grafana to break down by flag, environment and result.
	FlagEvaluationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ffee_flag_evaluations_total",
			Help: "Total number of flag evaluations performed by connected SDK instances.",
		},
		[]string{"flag_key", "environment", "result"}, // result: "true","false","default","error"
	)

	// FlagEvaluationDuration tracks SDK-side eval latency in seconds.
	// Buckets are very fine-grained because we're targeting sub-0.5ms.
	FlagEvaluationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "ffee_flag_evaluation_duration_seconds",
			Help: "Histogram of flag evaluation latencies. Target: p99 < 0.0005s (0.5ms).",
			// Sub-millisecond buckets: 10µs, 50µs, 100µs, 250µs, 500µs, 1ms, 5ms
			Buckets: []float64{.00001, .00005, .0001, .00025, .0005, .001, .005},
		},
		[]string{"flag_key", "environment"},
	)

	// ── Propagation metrics ──────────────────────────────────────

	// FlagPropagationLatency tracks end-to-end propagation time:
	// from the API write to all SDK instances updating their local state.
	// Kill-switch SLA: p99 < 2 seconds.
	FlagPropagationLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "ffee_flag_propagation_latency_seconds",
			Help:    "End-to-end propagation latency: API write → SDK local state update. SLA: p99 < 2s.",
			Buckets: []float64{.05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5},
		},
	)

	// FlagChangesTotal counts every flag mutation by action type.
	FlagChangesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ffee_flag_changes_total",
			Help: "Total number of flag mutations (create, update, enable, disable).",
		},
		[]string{"action", "flag_key"},
	)

	// ── SDK instance tracking ────────────────────────────────────

	// SDKConnectedInstances tracks how many SDK instances are currently
	// subscribed to the Redis pub/sub channel.
	// This is a Gauge because it goes up AND down.
	SDKConnectedInstances = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "ffee_sdk_connected_instances",
			Help: "Number of SDK instances currently subscribed to the flag update channel.",
		},
	)

	// ── Database metrics ─────────────────────────────────────────

	// DBQueryDuration tracks database query latency.
	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ffee_db_query_duration_seconds",
			Help:    "Histogram of PostgreSQL query execution times.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5},
		},
		[]string{"query_name"},
	)
)
