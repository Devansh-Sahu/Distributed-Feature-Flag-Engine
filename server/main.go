package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/devansh/feature-flag-engine/server/cache"
	"github.com/devansh/feature-flag-engine/server/config"
	"github.com/devansh/feature-flag-engine/server/db"
	"github.com/devansh/feature-flag-engine/server/handlers"
	appmiddleware "github.com/devansh/feature-flag-engine/server/middleware"
	"github.com/devansh/feature-flag-engine/server/notifier"
	// Import metrics for side-effect of registering all Prometheus metrics
	_ "github.com/devansh/feature-flag-engine/server/metrics"
)

func main() {
	// ── Structured logging ────────────────────────────────────────────
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env == "development" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	} else {
		log.Logger = log.Output(os.Stdout)
	}

	level, _ := zerolog.ParseLevel(cfg.LogLevel)
	zerolog.SetGlobalLevel(level)

	log.Info().Str("env", cfg.Env).Str("port", cfg.ServerPort).Msg("starting feature flag engine server")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── PostgreSQL ────────────────────────────────────────────────────
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pool.Close()

	// ── Run Phase 2 migration (idempotent — safe to run on every start) ──
	if err := db.RunMigration(ctx, pool, "002_notify_trigger"); err != nil {
		log.Fatal().Err(err).Msg("failed to run migration 002")
	}

	// ── Redis ─────────────────────────────────────────────────────────
	redisClient := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
		MinIdleConns: 5,
	})
	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to Redis")
	}
	defer redisClient.Close()
	log.Info().Str("addr", cfg.RedisAddr).Msg("connected to Redis")

	// ── Cache layer ───────────────────────────────────────────────────
	redisCache := cache.NewRedisCache(redisClient)
	materializer := cache.NewMaterializer(pool, redisCache)

	// Warm the Redis cache on startup: read all flags from Postgres, write to Redis.
	// This happens synchronously before we accept traffic so the cache is never
	// cold when the first request arrives.
	log.Info().Msg("warming Redis cache from PostgreSQL...")
	if err := materializer.MaterializeAll(ctx); err != nil {
		// Non-fatal: the server will still work, it'll just have cold-cache reads
		log.Warn().Err(err).Msg("cache warm-up failed (non-fatal, continuing)")
	}

	// ── Postgres LISTEN/NOTIFY → Cache invalidation ───────────────────
	// Start in a goroutine. This goroutine stays alive for the server's lifetime,
	// auto-reconnecting if the Postgres connection drops.
	pgListener := notifier.NewPGListener(cfg.DatabaseURL, materializer)
	go pgListener.Listen(ctx)

	// ── Handlers ──────────────────────────────────────────────────────
	flagHandler := &handlers.FlagHandler{DB: pool}
	envHandler := &handlers.EnvironmentHandler{DB: pool}
	healthHandler := &handlers.HealthHandler{DB: pool, Redis: redisClient}
	cacheHandler := &handlers.CacheHandler{
		DB:           pool,
		Cache:        redisCache,
		Materializer: materializer,
	}
	// Phase 4: SSE stream handler for SDK live updates
	streamHandler := &handlers.StreamHandler{Cache: redisCache}

	// ── Router ────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(appmiddleware.Recoverer)
	r.Use(appmiddleware.Logger)
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.StripSlashes)
	r.Use(chimiddleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3000", "http://localhost:5173", "http://localhost:4173"},
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link", "X-Request-ID", "X-Cache-Latency-Us", "X-Cache-Hit", "X-Flag-Count"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ── Routes ────────────────────────────────────────────────────────
	r.Get("/health", healthHandler.ServeHTTP)

	r.Route("/api/v1", func(r chi.Router) {
		// Flags CRUD (unchanged from Phase 1)
		r.Route("/flags", func(r chi.Router) {
			r.Get("/", flagHandler.ListFlags)
			r.Post("/", flagHandler.CreateFlag)
			r.Route("/{key}", func(r chi.Router) {
				r.Get("/", flagHandler.GetFlag)
				r.Patch("/", flagHandler.UpdateFlag)
				r.Delete("/", flagHandler.DeleteFlag)
				r.Patch("/config/{envName}", flagHandler.UpdateFlagConfig)
				r.Post("/rules", flagHandler.CreateTargetingRule)
				r.Delete("/rules/{ruleID}", flagHandler.DeleteTargetingRule)
				r.Get("/audit", flagHandler.GetAuditLog)

				// Phase 2: Cache-side reads (used by SDK in Phase 4)
				r.Get("/state/{envName}", cacheHandler.GetFlagState)
			})
		})

		// Phase 2: Bulk state endpoint for SDK bootstrap
		r.Get("/state/{envName}", cacheHandler.GetAllFlagStates)

		// Phase 2: Benchmark — proves cache vs DB latency
		r.Get("/benchmark/{key}", cacheHandler.Benchmark)

		// Phase 2: Cache status
		r.Get("/cache/status", cacheHandler.CacheStatus)

		// Phase 4: SSE stream — SDK connects here for live flag updates
		// NOTE: This route bypasses the 30s timeout middleware (SSE is long-lived)
		r.With(func(next http.Handler) http.Handler {
			// Remove the timeout middleware for SSE connections
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r)
			})
		}).Get("/stream/{envName}", streamHandler.StreamFlagUpdates)

		// Environments CRUD
		r.Route("/environments", func(r chi.Router) {
			r.Get("/", envHandler.ListEnvironments)
			r.Post("/", envHandler.CreateEnvironment)
			r.Get("/{name}", envHandler.GetEnvironment)
			r.Delete("/{name}", envHandler.DeleteEnvironment)
		})
	})

	// ── API HTTP Server ────────────────────────────────────────────────
	apiServer := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: r,
		// WriteTimeout must be 0 for SSE — otherwise the server will close
		// the SSE connection after WriteTimeout seconds, disconnecting all SDKs.
		WriteTimeout: 0,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  600 * time.Second, // 10 minutes for SSE keep-alive
	}

	// ── Prometheus Metrics Server ──────────────────────────────────────
	metricsRouter := chi.NewRouter()
	metricsRouter.Handle("/metrics", promhttp.Handler())
	metricsRouter.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	metricsServer := &http.Server{
		Addr:         ":" + cfg.MetricsPort,
		Handler:      metricsRouter,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		log.Info().Str("addr", apiServer.Addr).Msg("API server starting")
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("API server failed")
		}
	}()
	go func() {
		log.Info().Str("addr", metricsServer.Addr).Msg("metrics server starting")
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("metrics server failed")
		}
	}()

	// ── Graceful shutdown ──────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info().Str("signal", sig.String()).Msg("shutdown signal received")

	cancel() // stop the PG listener goroutine

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	apiServer.Shutdown(shutdownCtx)
	metricsServer.Shutdown(shutdownCtx)

	log.Info().Msg("server exited cleanly")
}
