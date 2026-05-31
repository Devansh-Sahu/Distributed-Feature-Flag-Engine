module github.com/devansh/feature-flag-engine/server

go 1.22

// Direct dependencies only — indirect deps are resolved by `go mod tidy`
// inside the Docker builder layer. We don't pin indirect deps manually
// because pseudo-version commit hashes go stale as modules rewrite history.
require (
	github.com/go-chi/chi/v5 v5.0.12
	github.com/go-chi/cors v1.2.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus/client_golang v1.19.1
	github.com/redis/go-redis/v9 v9.5.3
	github.com/rs/zerolog v1.33.0
)
