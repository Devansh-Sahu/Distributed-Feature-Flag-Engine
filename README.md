# Distributed Feature Flag Evaluation Engine

> A production-grade, open-source alternative to LaunchDarkly — built from scratch in Go.  
> **Sub-0.5ms** local evaluation · **< 2-second** kill-switch propagation · **100% local** Docker stack · Zero cloud cost.

[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go)](https://go.dev)
[![Kafka](https://img.shields.io/badge/Kafka-3.6-231F20?logo=apachekafka)](https://kafka.apache.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql)](https://postgresql.org)
[![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis)](https://redis.io)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react)](https://react.dev)

---

## What this is

A fully distributed feature flag system that handles the complete lifecycle:

```
Create flag → Manage via Admin UI → Propagate changes via Kafka CDC → SDK evaluates in < 0.5ms
```

Built as a **learning project** to demonstrate distributed systems concepts used at companies like LaunchDarkly, Cloudflare, and Stripe — implemented end-to-end with production-quality code.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          Admin UI (React)                           │
│              Kill switch · Rollout slider · Targeting rules         │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ REST API
┌───────────────────────────────▼─────────────────────────────────────┐
│                      Go API Server (:8080)                          │
│          CRUD · Audit log · Prometheus metrics (:9090)              │
└───────────┬───────────────────────────────────────┬─────────────────┘
            │ Writes                                │ LISTEN/NOTIFY
┌───────────▼──────────┐               ┌────────────▼────────────────┐
│    PostgreSQL 16      │               │        Redis 7              │
│  wal_level=logical   │               │  HASH per environment       │
│  ffee_publication    │               │  ffee:state:{env}           │
│  ffee_debezium_slot  │               │  PUBLISH ffee:updates:{env} │
└───────────┬──────────┘               └────────────▲────────────────┘
            │ WAL (pgoutput)                         │ HSET + PUBLISH
┌───────────▼──────────┐               ┌────────────┴────────────────┐
│  Debezium Connect     │    Kafka      │      Go CDC Worker          │
│  (Kafka Connect)      ├──────────────►  Consumer group: ffee-worker │
│  Postgres connector   │  topics:      │  Parses Debezium envelope   │
└──────────────────────┘  flag_configs │  Materializes to Redis      │
                          target_rules └─────────────────────────────┘
                                                      │ SSE
┌─────────────────────────────────────────────────────▼───────────────┐
│                      Go SDK (sdk/)                                   │
│  Bootstrap: GET /api/v1/state/{env}  →  in-memory map[flagKey]State │
│  Updates:   SSE /api/v1/stream/{env} →  atomic map update           │
│  Evaluate:  BoolVariation(key, user, default)  →  < 0.5ms           │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Quick Start

**Prerequisites**: Docker Desktop only. No Go, Node, or other tools required to run the backend.

### 1 — Start the full stack

```bash
cd feature-flag-engine
docker compose up -d
```

First run pulls ~2GB of images (Kafka, Debezium). Subsequent starts are < 10 seconds.

Wait for all services to be healthy:
```bash
docker compose ps
```

### 2 — Verify health

```bash
# Windows PowerShell
Invoke-WebRequest -UseBasicParsing http://localhost:8080/health | Select-Object -ExpandProperty Content

# Linux / macOS
curl -s http://localhost:8080/health | jq .
```

Expected response:
```json
{
  "status": "ok",
  "version": "1.0.0",
  "components": {
    "postgres": { "status": "ok", "latency": "0.5ms" },
    "redis":    { "status": "ok", "latency": "0.6ms" }
  }
}
```

### 3 — Open the Admin UI

```bash
cd admin-ui
npm install
npm run dev
```

Open **http://localhost:3000** → full dashboard for managing flags.

### 4 — Try the kill-switch flow

```bash
# Create a flag
curl -s -X POST http://localhost:8080/api/v1/flags \
  -H "Content-Type: application/json" \
  -d '{"key":"dark-mode","name":"Dark Mode","flag_type":"boolean"}'

# Enable it in production at 50% rollout
curl -s -X PATCH http://localhost:8080/api/v1/flags/dark-mode/config/production \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"rollout_percentage":50}'

# Verify the event propagated to Redis (< 2 seconds via Kafka CDC)
curl -s http://localhost:8080/api/v1/flags/dark-mode/state/production | jq .

# Kill-switch: instantly disable for all users
curl -s -X PATCH http://localhost:8080/api/v1/flags/dark-mode/config/production \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}'
```

---

## Build Phases

| Phase | Status | What it implements |
|-------|--------|-------------------|
| **1** | ✅ Complete | PostgreSQL schema, Go REST API, Docker Compose, Prometheus metrics, Grafana |
| **2** | ✅ Complete | Redis caching layer, `HSET` per environment, startup materializer, LISTEN/NOTIFY |
| **3** | ✅ Complete | Kafka + Zookeeper + Debezium CDC, Go worker service, durable propagation |
| **4** | ✅ Complete | Go SDK: zero-dep, SSE live updates, in-memory evaluation, targeting rules |
| **5** | ✅ Complete | React Admin UI: kill switch, rollout slider, targeting rules CRUD, audit log |
| **6** | ⬜ Todo | Load test: prove 100K evals/sec with k6 |
| **7** | ⬜ Todo | GitHub Actions CI pipeline: build, test, Docker push |

---

## Services & Ports

| Service | Port | Purpose |
|---------|------|---------|
| Go API Server | `:8080` | REST API for flag management |
| Prometheus Metrics | `:9090` | Internal metrics endpoint |
| PostgreSQL | `:5432` | Primary database (WAL enabled) |
| Redis | `:6379` | Cache layer + pub/sub |
| Kafka | `:9092` | Message broker |
| Debezium Connect | `:8083` | Kafka Connect REST API |
| Kafka UI | `:8090` | Browse topics and messages |
| Prometheus | `:9091` | Metrics scraper |
| Grafana | `:3001` | Dashboards (admin/admin) |
| Admin UI (dev) | `:3000` | React admin dashboard |

---

## API Reference

### Flags

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/flags` | List all flags |
| `POST` | `/api/v1/flags` | Create a flag |
| `GET` | `/api/v1/flags/{key}` | Get a flag |
| `PATCH` | `/api/v1/flags/{key}` | Update flag metadata |
| `DELETE` | `/api/v1/flags/{key}` | Delete a flag |
| `PATCH` | `/api/v1/flags/{key}/config/{env}` | Toggle enabled, set rollout % |
| `POST` | `/api/v1/flags/{key}/rules` | Add a targeting rule |
| `DELETE` | `/api/v1/flags/{key}/rules/{ruleID}` | Delete a targeting rule |
| `GET` | `/api/v1/flags/{key}/audit` | Get audit log |

### Cache & SDK

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/state/{env}` | All flag states for SDK bootstrap (Redis HGETALL) |
| `GET` | `/api/v1/flags/{key}/state/{env}` | Single flag state from Redis |
| `GET` | `/api/v1/stream/{env}` | SSE stream for live SDK updates |
| `GET` | `/api/v1/cache/status` | Cache hit stats per environment |
| `GET` | `/api/v1/benchmark/{key}?env=production` | Redis vs Postgres latency |

### Environments

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/v1/environments` | List environments |
| `POST` | `/api/v1/environments` | Create environment |

---

## SDK Usage

```go
import ffee "github.com/devansh/feature-flag-engine/sdk"

// Initialize once at app startup
client, err := ffee.NewClient("http://localhost:8080", "production")
if err != nil { log.Fatal(err) }
defer client.Close()

// Define the user context
ctx := ffee.UserContext{
    UserID: "user-42",
    Attributes: map[string]any{
        "plan":    "pro",
        "country": "IN",
    },
}

// Evaluate a flag — pure in-memory, zero network I/O, < 0.5ms
enabled := client.BoolVariation("new-checkout-flow", ctx, false)

// Other types
price   := client.Float64Variation("checkout-discount", ctx, 0.0)
variant := client.StringVariation("button-color", ctx, "blue")

// React to live flag changes
client.OnFlagUpdate(func(state ffee.FlagState) {
    fmt.Printf("Flag %q changed: enabled=%v\n", state.FlagKey, state.Enabled)
})
```

### Evaluation algorithm

```
1. map[flagKey] lookup            O(1), RWMutex read lock
2. if !found || !enabled          → return defaultValue
3. for each rule (by priority):
   - if matchesRule(rule, user)   → return rule.serve_value
4. if rollout == 100%             → return flag value
5. if rollout == 0%               → return defaultValue
6. bucket = FNV-1a(flagKey+":"+userID) % 100
   if bucket < rollout%           → return flag value
7.                                → return defaultValue
```

### Targeting rule operators

`eq` · `neq` · `in` · `not_in` · `gt` · `gte` · `lt` · `lte` · `contains` · `starts_with`

---

## Key Design Decisions

### Why WAL + Debezium instead of dual-write?

Dual-write (write to DB and Kafka atomically) has a failure window: the DB commit can succeed while the Kafka write fails, causing permanent divergence.

**WAL-based CDC**: Debezium reads PostgreSQL's Write-Ahead Log. Every committed transaction is written to WAL before the client receives an ACK. Even if Debezium restarts for hours, it resumes from the `ffee_debezium_slot` replication slot — zero missed events, guaranteed at-least-once delivery.

### Why LISTEN/NOTIFY is insufficient for production

`LISTEN/NOTIFY` is **ephemeral**: if the Go server is down when a flag changes, the notification fires and is **immediately lost forever**. The Redis cache stays stale until the next flag update triggers re-materialization.

Debezium CDC reads from WAL, which persists until the slot position advances. The worker can restart, reconnect, and catch up — no missed events.

### Why local in-process evaluation?

A centralized evaluation service would add a network round-trip (0.5–5ms) to every SDK call. With 100K evaluations/second across thousands of application instances, that's enormous load and latency.

**Local eval**: the SDK holds a copy of flag state in memory, evaluated with zero network I/O. Even if Redis and Kafka go down, existing SDK instances continue evaluating correctly against their last known state.

### Why FNV-1a(flagKey + ":" + userID) for rollout hashing?

If you hash only `userID`: a user in bucket 30 gets `true` for **all** flags at `rollout ≥ 30%`. Flags become correlated — the same 30% of users always see the feature, regardless of which flag.

By hashing `flagKey + ":" + userID`, each flag independently assigns users to buckets. A user at 50% for flag A is not guaranteed to be at 50% for flag B.

### Why SSE instead of WebSocket for SDK updates?

SSE is **unidirectional** (server → client), which is all the SDK needs. SSE:
- Works through HTTP/1.1 proxies and load balancers with zero config
- Has automatic reconnect built into the browser `EventSource` API
- Multiplexes over HTTP/2 naturally
- Requires no library — standard `net/http` or `EventSource` suffices

---

## Prometheus Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `ffee_flag_changes_total` | Counter | Flag mutations, labeled by action and key |
| `ffee_flag_propagation_latency_seconds` | Histogram | End-to-end propagation time, p99 SLA < 2s |
| `ffee_sdk_connected_instances` | Gauge | Active SSE connections (SDK count) |
| `ffee_cache_hits_total` | Counter | Redis HGET hits |
| `ffee_cache_misses_total` | Counter | Redis misses → Postgres fallback |

View live: http://localhost:9091 · Grafana: http://localhost:3001 (admin/admin)

---

## Development

```bash
# Watch server logs
docker compose logs -f server worker

# Inspect Kafka topics
# Open http://localhost:8090 (Kafka UI)

# Check Debezium connector status
curl http://localhost:8083/connectors/ffee-postgres-connector/status | jq .

# Redis CLI
docker exec -it ffee-redis redis-cli
  > HGETALL ffee:state:production
  > SUBSCRIBE ffee:updates:production

# Full teardown (deletes all data)
docker compose down -v
```

---

## Repository Structure

```
feature-flag-engine/
├── server/                 # Go API server
│   ├── main.go
│   ├── handlers/           # HTTP handlers (flags, cache, stream, health)
│   ├── cache/              # Redis materializer + HSET/PUBLISH
│   ├── db/                 # pgxpool + migrations runner
│   ├── notifier/           # Postgres LISTEN/NOTIFY listener
│   ├── metrics/            # Prometheus metrics registry
│   ├── config/             # Environment config
│   └── middleware/         # Logger, recoverer
├── worker/                 # CDC consumer (Kafka → Redis)
│   ├── main.go
│   └── consumer/           # Debezium envelope parser + materializer
├── sdk/                    # Go SDK (zero external deps)
│   ├── client.go           # NewClient, BoolVariation, etc.
│   ├── evaluator.go        # Targeting rules + rollout hashing
│   ├── stream.go           # SSE client with reconnect
│   └── models.go           # FlagState, UserContext
├── admin-ui/               # React admin dashboard (Vite)
│   └── src/
│       ├── App.jsx          # Main app (kill switch, rollout, rules, audit)
│       ├── api/client.js    # API wrapper
│       ├── hooks/useSSE.js  # SSE live updates hook
│       └── styles/          # Design system CSS
├── debezium/
│   └── connector-config.json
├── prometheus/
│   └── prometheus.yml
├── grafana/
│   └── dashboards/
├── examples/
│   └── demo/main.go        # SDK usage demo
└── docker-compose.yml      # Full stack orchestration
```

---

## Resume Bullet

> Designed and implemented a distributed feature flag evaluation engine in Go — sub-0.5ms local evaluation via in-memory SDK backed by Redis cache, flag change propagation via PostgreSQL WAL → Debezium → Kafka CDC pipeline with < 2-second kill-switch SLA, consistent hashing (FNV-1a on `flagKey + ":" + userID`) for deterministic per-flag user bucketing, SSE-based SDK live updates, and React admin dashboard. Full Docker Compose stack (Postgres, Redis, Kafka, Debezium, Prometheus, Grafana). Open-sourced on GitHub.
