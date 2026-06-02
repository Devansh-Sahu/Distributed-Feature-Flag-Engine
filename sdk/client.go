// Package ffee provides a Go SDK for the Distributed Feature Flag Evaluation Engine.
//
// # Quick start
//
//	client, err := ffee.NewClient("http://localhost:8080", "production")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
//	ctx := ffee.UserContext{UserID: "user-123", Attributes: map[string]any{"plan": "pro"}}
//	enabled := client.BoolVariation("new-checkout-flow", ctx, false)
//
// # Architecture
//
// The client maintains an in-memory copy of all flag states for one environment.
// Flag evaluation is pure in-process — zero network I/O on the hot path.
//
//	Server (Redis cache)
//	    ↓ HTTP GET /api/v1/state/{env}          ← one-time bootstrap
//	SDK in-memory map[flagKey]FlagState
//	    ↓ SSE /api/v1/stream/{env}              ← live updates, persistent connection
//	    ↑ flag changes arrive in < 100ms
//
// Evaluation latency: < 1µs (pure in-memory map lookup + rule matching).
package ffee

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Client is the FFEE SDK client. It is safe for concurrent use.
// Create one per application (it is expensive to create, cheap to use).
type Client struct {
	serverURL   string
	environment string
	httpClient  *http.Client

	mu    sync.RWMutex
	flags map[string]FlagState // in-memory flag store, keyed by flag_key

	streamCancel context.CancelFunc
	closed       atomic.Bool

	// Callbacks registered via OnFlagUpdate
	updateMu  sync.RWMutex
	callbacks []func(FlagState)
}

// Option configures the Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// NewClient creates a new SDK client, bootstraps all flag states from the server,
// and starts the SSE stream for live updates.
//
// serverURL: base URL of the FFEE server, e.g. "http://localhost:8080"
// environment: environment name, e.g. "production", "staging", "development"
func NewClient(serverURL, environment string, opts ...Option) (*Client, error) {
	c := &Client{
		serverURL:   serverURL,
		environment: environment,
		flags:       make(map[string]FlagState),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, o := range opts {
		o(c)
	}

	// Bootstrap: load all flag states in one call
	if err := c.bootstrap(); err != nil {
		return nil, fmt.Errorf("ffee: bootstrap failed: %w", err)
	}

	// Start the SSE stream for live updates in the background
	ctx, cancel := context.WithCancel(context.Background())
	c.streamCancel = cancel
	go c.runStream(ctx)

	return c, nil
}

// bootstrap fetches all flag states for the environment via a single HTTP call.
// The server returns a Redis HGETALL result: map[flagKey → FlagState].
func (c *Client) bootstrap() error {
	url := fmt.Sprintf("%s/api/v1/state/%s", c.serverURL, c.environment)
	resp, err := c.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}

	var apiResp struct {
		Success bool                  `json:"success"`
		Data    map[string]FlagState  `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	c.mu.Lock()
	for k, v := range apiResp.Data {
		sortTargetingRules(v.TargetingRules)
		c.flags[k] = v
	}
	c.mu.Unlock()

	return nil
}

// Close shuts down the SSE stream and releases resources.
// Always defer client.Close() after NewClient.
func (c *Client) Close() {
	if c.closed.CompareAndSwap(false, true) {
		if c.streamCancel != nil {
			c.streamCancel()
		}
	}
}

// ── Typed evaluation methods ──────────────────────────────────────────────

// BoolVariation returns the boolean value of a flag for the given user.
// Returns defaultValue if the flag is not found, is disabled, or the stored
// value cannot be converted to bool.
func (c *Client) BoolVariation(flagKey string, ctx UserContext, defaultValue bool) bool {
	raw, ok := c.evaluate(flagKey, ctx)
	if !ok {
		return defaultValue
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return defaultValue
	}
	return v
}

// StringVariation returns the string value of a flag.
func (c *Client) StringVariation(flagKey string, ctx UserContext, defaultValue string) string {
	raw, ok := c.evaluate(flagKey, ctx)
	if !ok {
		return defaultValue
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return defaultValue
	}
	return v
}

// Float64Variation returns the numeric value of a flag.
func (c *Client) Float64Variation(flagKey string, ctx UserContext, defaultValue float64) float64 {
	raw, ok := c.evaluate(flagKey, ctx)
	if !ok {
		return defaultValue
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return defaultValue
	}
	return v
}

// JSONVariation unmarshals the flag value into dest (must be a pointer).
func (c *Client) JSONVariation(flagKey string, ctx UserContext, dest interface{}) error {
	raw, ok := c.evaluate(flagKey, ctx)
	if !ok {
		return fmt.Errorf("flag %q not found or disabled", flagKey)
	}
	return json.Unmarshal(raw, dest)
}

// FlagExists reports whether a flag with the given key exists and is loaded.
func (c *Client) FlagExists(flagKey string) bool {
	c.mu.RLock()
	_, ok := c.flags[flagKey]
	c.mu.RUnlock()
	return ok
}

// AllFlags returns a snapshot of all flag states. For debugging only.
func (c *Client) AllFlags() map[string]FlagState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]FlagState, len(c.flags))
	for k, v := range c.flags {
		result[k] = v
	}
	return result
}

// OnFlagUpdate registers a callback that fires whenever a flag is updated.
// The callback runs in the SSE goroutine — keep it fast (< 1ms).
func (c *Client) OnFlagUpdate(fn func(FlagState)) {
	c.updateMu.Lock()
	c.callbacks = append(c.callbacks, fn)
	c.updateMu.Unlock()
}

// ── Internal ──────────────────────────────────────────────────────────────

// evaluate is the core evaluation path. Returns the raw JSON value and true
// if the flag is enabled and the user is in the rollout. Returns nil, false otherwise.
func (c *Client) evaluate(flagKey string, ctx UserContext) (json.RawMessage, bool) {
	c.mu.RLock()
	state, ok := c.flags[flagKey]
	c.mu.RUnlock()

	if !ok || !state.Enabled {
		return nil, false
	}

	// Check targeting rules first (evaluated in priority order, lowest first)
	for _, rule := range state.TargetingRules {
		if matchesRule(rule, ctx) {
			return rule.ServeValue, true
		}
	}

	// No targeting rule matched — apply rollout percentage
	switch {
	case state.RolloutPercentage >= 100:
		return state.DefaultValue, true
	case state.RolloutPercentage <= 0:
		return nil, false
	default:
		bucket := hashUserBucket(flagKey, ctx.UserID)
		if bucket < state.RolloutPercentage {
			return state.DefaultValue, true
		}
		return nil, false
	}
}

// updateFlag atomically replaces a flag in the in-memory store.
func (c *Client) updateFlag(state FlagState) {
	sortTargetingRules(state.TargetingRules)
	c.mu.Lock()
	c.flags[state.FlagKey] = state
	c.mu.Unlock()

	// Fire registered callbacks
	c.updateMu.RLock()
	cbs := c.callbacks
	c.updateMu.RUnlock()
	for _, fn := range cbs {
		fn(state)
	}
}

// sortTargetingRules sorts a list of rules in ascending priority order (lower number = first).
func sortTargetingRules(rules []TargetingRule) {
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})
}
