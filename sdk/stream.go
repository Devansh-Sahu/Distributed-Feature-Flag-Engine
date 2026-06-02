package ffee

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────
// SSE Stream client
//
// The SDK connects to GET /api/v1/stream/{envName} and keeps the connection
// open indefinitely. The server sends an SSE event every time a flag changes.
//
// SSE wire format (text/event-stream):
//
//   event: connected
//   data: {"env":"production","ts":1717000000000}
//
//   data: {"flag_key":"new-checkout-flow","enabled":true,...}
//
//   event: ping
//   data: {"ts":1717000030000}
//
// Rules:
//   - Each event ends with a blank line (\n\n)
//   - Lines starting with "data:" carry the payload
//   - Lines starting with "event:" name the event (optional)
//   - Lines starting with ":" are comments (ignored)
//
// On disconnect: the SDK waits with exponential backoff and reconnects.
// On reconnect: no re-bootstrap is needed — the SSE stream carries the FULL
// FlagState, so any missed events are applied when they arrive.
// ─────────────────────────────────────────────────────────────────────────

// runStream is the SSE client loop. It runs in a goroutine for the lifetime
// of the Client and reconnects automatically on failure.
func (c *Client) runStream(ctx context.Context) {
	backoff := 500 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		err := c.connectStream(ctx)
		if ctx.Err() != nil {
			return // clean shutdown, not an error
		}

		if err != nil {
			// Log-free: we don't take a logger dependency in the SDK.
			// In a real product you'd emit a metric or call an error hook here.
			_ = err
		}

		// Exponential backoff before reconnecting
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectStream opens one SSE connection and reads events until the connection
// closes or the context is cancelled.
func (c *Client) connectStream(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v1/stream/%s", c.serverURL, c.environment)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Use a client with no timeout — SSE is a persistent connection.
	// The context carries the cancellation signal instead.
	streamClient := &http.Client{Timeout: 0}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE: server returned %d", resp.StatusCode)
	}

	// Reset backoff on successful connection
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "data:"):
			// Accumulate data lines (multi-line data is allowed by SSE spec)
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			dataLines = append(dataLines, data)

		case line == "":
			// Blank line = end of event, dispatch accumulated data
			if len(dataLines) > 0 {
				payload := strings.Join(dataLines, "\n")
				c.handleSSEPayload(payload)
				dataLines = dataLines[:0]
			}

		case strings.HasPrefix(line, "event:"):
			// Named events (connected, ping) — we handle them via the data field
		case strings.HasPrefix(line, ":"):
			// SSE comment, ignore
		}

		if ctx.Err() != nil {
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE scanner: %w", err)
	}
	return nil
}

// handleSSEPayload processes a single SSE data payload.
// The payload is a full FlagState JSON (or a ping/connected event).
func (c *Client) handleSSEPayload(payload string) {
	// Skip ping/connected events (they don't carry flag data)
	if !strings.Contains(payload, "flag_key") {
		return
	}

	var state FlagState
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		return
	}
	if state.FlagKey == "" {
		return
	}

	c.updateFlag(state)
}
