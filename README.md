# Feature Flag Engine (FFEE)

> A distributed feature flag evaluation engine — open-source alternative to LaunchDarkly.
> Sub-0.5ms local evaluation | 2-second kill-switch SLA | 100K evals/sec throughput

[![Phase 1](https://img.shields.io/badge/phase-1%20complete-brightgreen)](./docs/phases.md)

---

## Architecture

```
Admin UI (React) → Flag API (Go) → PostgreSQL
                                        ↓ WAL (Phase 3)
                              Debezium → Kafka → Propagation Worker
                                                        ↓
                                              Redis pub/sub
                                                        ↓
                              SDK instances (in-process eval, 0 network I/O)
```

## Quick Start (Phase 1)

### Prerequisites
- Docker Desktop (free)
- That's it. Zero other dependencies.

### 1. Start the stack

```bash
# From the feature-flag-engine directory:
docker-compose up -d

# Watch startup logs:
docker-compose logs -f server
```

### 2. Verify everything is healthy

```bash
curl http://localhost:8080/health
# Expected:
# {"status":"ok","version":"1.0.0","components":{"postgres":{"status":"ok","latency":"2ms"},"redis":{"status":"ok","latency":"1ms"}}}
```

### 3. Run through the API

```bash
# List environments (seeded automatically)
curl http://localhost:8080/api/v1/environments | jq .

# Create a flag
curl -s -X POST http://localhost:8080/api/v1/flags \
  -H "Content-Type: application/json" \
  -d '{"key":"new-checkout-flow","name":"New Checkout Flow","description":"Redesigned checkout","flag_type":"boolean"}' | jq .

# List flags
curl http://localhost:8080/api/v1/flags | jq .

# Enable the flag in production (rollout 50%)
curl -s -X PATCH "http://localhost:8080/api/v1/flags/new-checkout-flow/config/production" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"rollout_percentage":50}' | jq .

# Add a targeting rule: users in India get true regardless of rollout
curl -s -X POST "http://localhost:8080/api/v1/flags/new-checkout-flow/rules" \
  -H "Content-Type: application/json" \
  -d '{
    "environment_id": "<PASTE_PROD_ENV_ID_FROM_LIST_RESPONSE>",
    "priority": 0,
    "attribute": "user.country",
    "operator": "in",
    "value": ["IN","US"],
    "serve_value": true
  }' | jq .

# Kill-switch: disable the flag globally
curl -s -X PATCH "http://localhost:8080/api/v1/flags/new-checkout-flow/config/production" \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}' | jq .

# View audit log
curl "http://localhost:8080/api/v1/flags/new-checkout-flow/audit" | jq .

# View Prometheus metrics
curl http://localhost:9090/metrics | grep ffee_
```

### 4. Open Grafana Dashboard
- URL: http://localhost:3001
- Login: admin / admin

### 5. Stop the stack

```bash
docker-compose down
# To also delete all data:
docker-compose down -v
```

---

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check with component status |
| GET | `/api/v1/flags` | List all flags with configs |
| POST | `/api/v1/flags` | Create a flag |
| GET | `/api/v1/flags/{key}` | Get a flag by key |
| PATCH | `/api/v1/flags/{key}` | Update flag metadata |
| DELETE | `/api/v1/flags/{key}` | Delete a flag |
| PATCH | `/api/v1/flags/{key}/config/{envName}` | Update flag config for an environment (enable/disable/rollout) |
| POST | `/api/v1/flags/{key}/rules` | Add a targeting rule |
| DELETE | `/api/v1/flags/{key}/rules/{ruleID}` | Delete a targeting rule |
| GET | `/api/v1/flags/{key}/audit` | Get audit log |
| GET | `/api/v1/environments` | List environments |
| POST | `/api/v1/environments` | Create an environment |
| GET | `/api/v1/environments/{name}` | Get an environment |
| DELETE | `/api/v1/environments/{name}` | Delete an environment |

---

## Build Phases

| Phase | Status | Description |
|-------|--------|-------------|
| 1 | ✅ Complete | PostgreSQL schema + Go API server + Docker Compose |
| 2 | ⬜ Todo | Redis caching layer + flag state materialization |
| 3 | ⬜ Todo | Kafka + Debezium CDC pipeline |
| 4 | ⬜ Todo | Go SDK: local eval + consistent hashing |
| 5 | ⬜ Todo | Targeting rules engine |
| 6 | ⬜ Todo | Kill-switch guarantee + propagation SLA |
| 7 | ⬜ Todo | Prometheus metrics + Grafana dashboard |
| 8 | ⬜ Todo | React Admin UI |
| 9 | ⬜ Todo | GitHub Actions CI pipeline |
| 10 | ⬜ Todo | Load test: prove 100K evals/sec |

---

## Key Architectural Decisions

### Why WAL + Debezium instead of dual-write?
Dual-write (write to DB and Kafka simultaneously) has a failure window: the DB write can succeed while the Kafka write fails, causing the two to diverge permanently. WAL-based CDC treats the DB as the single source of truth — Debezium reads the already-committed change directly from the transaction log. No data loss, no divergence.

### Why local in-process evaluation?
A centralized eval service would add a network round-trip (0.5–5ms) to every evaluation. With 100K evaluations/second across thousands of application instances, that's enormous load and latency. Local eval — SDK holds a copy of flag state in memory and evaluates with zero network I/O — gives sub-0.5ms latency and works even when Redis/Kafka are down.

### Why consistent hashing with hash(userID + flagKey) % 100?
If you only hash userID: a user in bucket 30 (30 < 50% rollout → true) gets `true` for ALL flags at 50% rollout. Flags become correlated across your user population. By hashing `userID + flagKey`, each flag independently bucketes users — a user gets different buckets for different flags, which is what you want for independent experiments.

---

## Resume Bullet (earned after Phase 10)
> "Designed and implemented a distributed feature flag evaluation engine in Go — sub-0.5ms local evaluation via Redis-cached SDK, flag change propagation via PostgreSQL WAL → Kafka CDC pipeline with 2-second kill-switch SLA, consistent hashing for deterministic user bucketing, and 100K evaluations/second load-tested throughput. Full Docker Compose stack. Open-sourced on GitHub."
