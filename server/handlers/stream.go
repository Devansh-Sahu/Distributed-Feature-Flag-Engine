package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/cache"
	"github.com/devansh/feature-flag-engine/server/metrics"
)

// StreamHandler serves the Server-Sent Events endpoint for SDK live updates.
//
// WHY SSE instead of WebSocket?
//
// WebSocket: bidirectional, stateful, requires upgrade handshake.
//   Good for: chat, games, anything that needs client→server messages.
//
// SSE (Server-Sent Events): unidirectional server→client, built on HTTP/1.1.
//   Good for: push notifications, live feeds, flag updates.
//   Advantages over WebSocket for this use case:
//   - Works through HTTP/1.1 proxies and load balancers with no config
//   - Automatic reconnect built into the EventSource browser API
//   - Dead simple: just write "data: ...\n\n" to an HTTP response
//   - Multiplexes over HTTP/2 naturally
//   - No library needed on the SDK side — standard net/http suffices
//
// The SDK connects once, receives all flag updates in real-time.
// The server subscribes to Redis pub/sub (one subscription per SSE client)
// and forwards every message. The per-client Redis subscription is fine
// for development; Phase 7 will add a fan-out hub to share one subscription
// across all clients per environment.

type StreamHandler struct {
	Cache *cache.RedisCache
}

// StreamFlagUpdates handles GET /api/v1/stream/{envName}
//
// Flow:
//  1. SDK client opens a long-lived HTTP connection to this endpoint
//  2. Server subscribes to Redis pub/sub channel: ffee:updates:{envName}
//  3. When a flag changes: Kafka worker writes to Redis → Redis publishes
//     → this handler receives the message → writes SSE event to client
//  4. SDK receives the event and updates its in-memory map atomically
//  5. Next BoolVariation() call sees the new state — zero additional I/O
func (h *StreamHandler) StreamFlagUpdates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	envName := chi.URLParam(r, "envName")

	// Verify the client supports streaming (should always be true for SSE)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Subscribe to the Redis pub/sub channel for this environment.
	// This connection is separate from the pool — pub/sub requires a
	// dedicated connection that stays open for the duration of the stream.
	sub := h.Cache.Subscribe(ctx, envName)
	defer sub.Close()

	// Track connected SDK instances in Prometheus
	metrics.SDKConnectedInstances.Inc()
	defer metrics.SDKConnectedInstances.Dec()

	log.Info().
		Str("env", envName).
		Str("remote", r.RemoteAddr).
		Msg("SSE: SDK client connected")

	// ── SSE response headers ──────────────────────────────────────
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable nginx/proxy response buffering — critical for SSE
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Send the "connected" event immediately so the SDK knows it's live
	fmt.Fprintf(w, "event: connected\ndata: {\"env\":\"%s\",\"ts\":%d}\n\n", envName, time.Now().UnixMilli())
	flusher.Flush()

	// ── Event loop ───────────────────────────────────────────────
	// Keep-alive ticker: SSE connections are silently dropped by some
	// proxies after 60-90s of inactivity. A 30s heartbeat prevents this.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	ch := sub.Channel()
	msgCount := 0

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				// Redis pub/sub channel closed (server shutting down)
				log.Info().Str("env", envName).Msg("SSE: Redis channel closed, ending stream")
				return
			}
			// Forward the flag state JSON as an SSE data event.
			// Format: "data: <json>\n\n" — the double newline ends the event.
			fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
			flusher.Flush()
			msgCount++

			log.Debug().
				Str("env", envName).
				Int("total_sent", msgCount).
				Msg("SSE: flag update sent to SDK")

		case <-heartbeat.C:
			// Ping event — SDK ignores it but the TCP connection stays alive
			fmt.Fprintf(w, "event: ping\ndata: {\"ts\":%d}\n\n", time.Now().UnixMilli())
			flusher.Flush()

		case <-ctx.Done():
			// SDK client disconnected or server shutting down
			log.Info().
				Str("env", envName).
				Str("remote", r.RemoteAddr).
				Int("messages_sent", msgCount).
				Msg("SSE: SDK client disconnected")
			return
		}
	}
}
