// Demo: Distributed Feature Flag SDK in action.
//
// This program shows:
//  1. SDK bootstrap (all flags loaded in one HTTP call)
//  2. Flag evaluation: BoolVariation, StringVariation with targeting rules
//  3. Rollout: same user always gets same bucket (consistent hashing)
//  4. Live updates: flag change in another terminal → this program
//     detects it within < 2 seconds (no restart required)
//
// Run:
//
//	docker compose up -d          # start the stack
//	go run examples/demo/main.go  # run this demo (requires local Go)
//
// In another terminal, toggle a flag and watch this program react:
//
//	curl -X PATCH http://localhost:8080/api/v1/flags/new-checkout-flow/config/production \
//	  -H 'Content-Type: application/json' \
//	  -d '{"enabled":true}'
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	ffee "github.com/devansh/feature-flag-engine/sdk"
)

func main() {
	serverURL := getenv("FFEE_SERVER_URL", "http://localhost:8080")
	environment := getenv("FFEE_ENVIRONMENT", "production")

	fmt.Printf("╔══════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  FFEE Go SDK Demo — Distributed Feature Flag Engine      ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════╝\n\n")
	fmt.Printf("  Server:      %s\n", serverURL)
	fmt.Printf("  Environment: %s\n\n", environment)

	// ── 1. Create the SDK client ─────────────────────────────────────────
	// This call:
	//   a) Fetches ALL flag states from Redis via GET /api/v1/state/production
	//   b) Opens a persistent SSE connection to GET /api/v1/stream/production
	//   c) Returns. All future flag evaluations are in-process (zero network).
	fmt.Println("▶ Bootstrapping SDK client...")
	client, err := ffee.NewClient(serverURL, environment)
	if err != nil {
		log.Fatalf("Failed to create FFEE client: %v\n", err)
	}
	defer client.Close()

	allFlags := client.AllFlags()
	fmt.Printf("✓ Bootstrapped %d flag(s) from Redis\n\n", len(allFlags))

	// ── 2. Register a live-update callback ──────────────────────────────
	// This fires when the SSE stream delivers a flag change event.
	client.OnFlagUpdate(func(state ffee.FlagState) {
		fmt.Printf("\n🔔 LIVE UPDATE received!\n")
		fmt.Printf("   Flag:    %s\n", state.FlagKey)
		fmt.Printf("   Enabled: %v\n", state.Enabled)
		fmt.Printf("   Rollout: %d%%\n", state.RolloutPercentage)
	})

	// ── 3. Evaluate flags for different users ────────────────────────────
	// Define some test users with different attributes
	users := []ffee.UserContext{
		{
			UserID: "user-001",
			Attributes: map[string]any{
				"plan":    "free",
				"country": "US",
			},
		},
		{
			UserID: "user-002",
			Attributes: map[string]any{
				"plan":    "pro",
				"country": "IN",
			},
		},
		{
			UserID: "user-003",
			Attributes: map[string]any{
				"plan":    "enterprise",
				"country": "GB",
			},
		},
	}

	fmt.Println("▶ Evaluating flags for test users:")
	fmt.Println("────────────────────────────────────────────────────────────")

	for _, user := range users {
		fmt.Printf("\n  User: %s  (plan=%v, country=%v)\n",
			user.UserID, user.Attributes["plan"], user.Attributes["country"])

		// BoolVariation — the most common call
		enabled := client.BoolVariation("new-checkout-flow", user, false)
		fmt.Printf("    new-checkout-flow  → %v\n", enabled)
	}

	// ── 4. Demonstrate rollout consistency ──────────────────────────────
	fmt.Printf("\n▶ Rollout bucket consistency check (same user → same bucket always):\n")
	fmt.Println("────────────────────────────────────────────────────────────")
	testUser := ffee.UserContext{UserID: "user-042"}
	results := make(map[bool]int)
	for i := 0; i < 10; i++ {
		v := client.BoolVariation("new-checkout-flow", testUser, false)
		results[v]++
	}
	fmt.Printf("  user-042 evaluated 10 times: always=%v (consistent: %v)\n",
		func() bool {
			for _, count := range results {
				return count == 10
			}
			return false
		}(),
		len(results) == 1,
	)

	// ── 5. Live update loop ──────────────────────────────────────────────
	fmt.Printf("\n▶ Listening for live flag updates via SSE...\n")
	fmt.Printf("  Change a flag in another terminal:\n\n")
	fmt.Printf("  curl -X PATCH http://localhost:8080/api/v1/flags/new-checkout-flow/config/production \\\n")
	fmt.Printf("    -H 'Content-Type: application/json' \\\n")
	fmt.Printf("    -d '{\"enabled\":true}'\n\n")
	fmt.Printf("  (Ctrl+C to exit)\n\n")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Re-evaluate every 5 seconds to show live updates propagating
		enabled := client.BoolVariation("new-checkout-flow", users[0], false)
		fmt.Printf("  [%s] new-checkout-flow for %s → %v\n",
			time.Now().Format("15:04:05"), users[0].UserID, enabled)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
