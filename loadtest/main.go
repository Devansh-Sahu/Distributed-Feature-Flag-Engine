package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	ffee "github.com/devansh/feature-flag-engine/sdk"
)

var (
	evalCount uint64
	startTime time.Time
	client    *ffee.Client
)

// Response represents the evaluation result returned to k6.
type EvalResponse struct {
	Key    string `json:"key"`
	UserID string `json:"user_id"`
	Value  bool   `json:"value"`
}

// StatsResponse represents server statistics.
type StatsResponse struct {
	UptimeSeconds float64 `json:"uptime_seconds"`
	Evaluations   uint64  `json:"evaluations"`
	EvalsPerSec   float64 `json:"evals_per_sec"`
}

func main() {
	startTime = time.Now()
	rand.Seed(time.Now().UnixNano())

	serverURL := getenv("FFEE_SERVER_URL", "http://localhost:8080")
	environment := getenv("FFEE_ENVIRONMENT", "production")
	port := getenv("PORT", "8085")

	log.Printf("Starting Loadtest App Server...")
	log.Printf("Connecting to FFEE Server at: %s", serverURL)
	log.Printf("Target Environment: %s", environment)

	// Initialize the FFEE Client (zero-network lookup during evaluations)
	var err error
	client, err = ffee.NewClient(serverURL, environment)
	if err != nil {
		log.Fatalf("Failed to initialize FFEE client: %v", err)
	}
	defer client.Close()

	log.Println("✓ FFEE SDK Client initialized and subscribed to SSE stream.")

	mux := http.NewServeMux()
	mux.HandleFunc("/evaluate", handleEvaluate)
	mux.HandleFunc("/stats", handleStats)
	mux.HandleFunc("/health", handleHealth)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("Loadtest Mock App running on port %s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleEvaluate(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		key = "new-checkout-flow"
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = fmt.Sprintf("user-%d", rand.Intn(1000000))
	}

	plan := r.URL.Query().Get("plan")
	if plan == "" {
		plans := []string{"free", "pro", "enterprise"}
		plan = plans[rand.Intn(len(plans))]
	}

	country := r.URL.Query().Get("country")
	if country == "" {
		countries := []string{"US", "IN", "GB", "CA", "DE"}
		country = countries[rand.Intn(len(countries))]
	}

	// Prepare user context
	userCtx := ffee.UserContext{
		UserID: userID,
		Attributes: map[string]any{
			"plan":    plan,
			"country": country,
		},
	}

	// Perform in-memory SDK evaluation (< 0.5ms)
	val := client.BoolVariation(key, userCtx, false)

	atomic.AddUint64(&evalCount, 1)

	// High-performance JSON response write
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	// Avoid encoding overhead for hot path by writing pre-formatted output
	fmt.Fprintf(w, `{"key":"%s","user_id":"%s","value":%t}`, key, userID, val)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(startTime).Seconds()
	count := atomic.LoadUint64(&evalCount)

	stats := StatsResponse{
		UptimeSeconds: uptime,
		Evaluations:   count,
		EvalsPerSec:   float64(count) / uptime,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
