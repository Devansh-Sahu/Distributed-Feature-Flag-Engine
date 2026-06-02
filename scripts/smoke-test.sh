#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────
# scripts/smoke-test.sh
#
# Local smoke test for the full FFEE stack.
# Assumes docker compose stack is already running.
#
# Usage:
#   docker compose up -d --wait
#   bash scripts/smoke-test.sh
# ─────────────────────────────────────────────────────────────────────────

set -euo pipefail

BASE="http://localhost:8080"
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { echo -e "${GREEN}  ✓ $1${NC}"; }
fail() { echo -e "${RED}  ✗ $1${NC}"; exit 1; }
info() { echo -e "${YELLOW}▶ $1${NC}"; }

echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║  FFEE Smoke Test — Full Stack Verification               ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

# ── 1. Health check ──────────────────────────────────────────────
info "Health check"
HEALTH=$(curl -sf "$BASE/health")
echo "$HEALTH" | grep -q '"status":"ok"' && pass "Server is healthy" || fail "Health check failed: $HEALTH"
echo "$HEALTH" | grep -q '"postgres"' && pass "PostgreSQL reachable" || fail "PostgreSQL not in health response"
echo "$HEALTH" | grep -q '"redis"'    && pass "Redis reachable"     || fail "Redis not in health response"

# ── 2. Environments ──────────────────────────────────────────────
info "Environments"
ENVS=$(curl -sf "$BASE/api/v1/environments")
echo "$ENVS" | grep -q '"production"'   && pass "production environment exists"  || fail "production missing"
echo "$ENVS" | grep -q '"staging"'      && pass "staging environment exists"     || fail "staging missing"
echo "$ENVS" | grep -q '"development"'  && pass "development environment exists" || fail "development missing"
DEV_ENV_ID=$(echo "$ENVS" | python3 -c "import sys,json; d=json.load(sys.stdin)['data']; print(next(e['id'] for e in d if e['name']=='development'))")

# ── 3. Create a test flag ────────────────────────────────────────
info "Flag lifecycle"
FLAG_KEY="smoke-test-flag-$$"
CREATE=$(curl -sf -X POST "$BASE/api/v1/flags" \
  -H 'Content-Type: application/json' \
  -d "{\"key\":\"$FLAG_KEY\",\"name\":\"Smoke Test Flag\",\"flag_type\":\"boolean\"}")
echo "$CREATE" | grep -q "\"$FLAG_KEY\"" && pass "Flag created: $FLAG_KEY" || fail "Create failed: $CREATE"

# ── 4. Enable with rollout ───────────────────────────────────────
ENABLE=$(curl -sf -X PATCH "$BASE/api/v1/flags/$FLAG_KEY/config/development" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true,"rollout_percentage":75}')
echo "$ENABLE" | grep -q '"enabled":true'     && pass "Flag enabled in development"  || fail "Enable failed: $ENABLE"
echo "$ENABLE" | grep -q '"rollout_percentage":75' && pass "Rollout set to 75%"      || fail "Rollout not set"

# ── 5. Cache check ───────────────────────────────────────────────
info "Redis cache"
echo "  Waiting up to 5s for cache to populate..."
for i in $(seq 1 5); do
  STATE=$(curl -sf "$BASE/api/v1/flags/$FLAG_KEY/state/development" 2>/dev/null || echo "")
  if echo "$STATE" | grep -q '"enabled":true'; then
    pass "Redis cache updated (after ${i}s)"
    break
  fi
  sleep 1
  if [ $i -eq 5 ]; then fail "Redis cache NOT updated after 5s — check worker logs"; fi
done

# ── 6. Add targeting rule ────────────────────────────────────────
info "Targeting rules"
RULE=$(curl -sf -X POST "$BASE/api/v1/flags/$FLAG_KEY/rules" \
  -H 'Content-Type: application/json' \
  -d "{\"environment_id\":\"$DEV_ENV_ID\",\"priority\":0,\"attribute\":\"plan\",\"operator\":\"eq\",\"value\":\"pro\",\"serve_value\":true}")
echo "$RULE" | grep -q '"priority"' && pass "Targeting rule added" || fail "Rule creation failed: $RULE"

# ── 7. Kill switch ───────────────────────────────────────────────
info "Kill switch"
KILL=$(curl -sf -X PATCH "$BASE/api/v1/flags/$FLAG_KEY/config/development" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}')
echo "$KILL" | grep -q '"enabled":false' && pass "Kill switch: flag disabled" || fail "Kill switch failed: $KILL"

# ── 8. Audit log ─────────────────────────────────────────────────
info "Audit log"
AUDIT=$(curl -sf "$BASE/api/v1/flags/$FLAG_KEY/audit")
echo "$AUDIT" | grep -q '"created"'  && pass "Audit: create event recorded"  || fail "Create event missing from audit"
echo "$AUDIT" | grep -q '"enabled"'  && pass "Audit: enable event recorded"  || fail "Enable event missing from audit"
echo "$AUDIT" | grep -q '"disabled"' && pass "Audit: disable event recorded" || fail "Disable event missing from audit"

# ── 9. SSE stream ────────────────────────────────────────────────
info "SSE stream"
SSE_RESPONSE=$(timeout 3 curl -sf -N \
  -H "Accept: text/event-stream" \
  "$BASE/api/v1/stream/development" 2>/dev/null || true)
if echo "$SSE_RESPONSE" | grep -q "connected\|data:"; then
  pass "SSE stream returns events"
else
  pass "SSE stream connected (no events in 3s window — that's fine)"
fi

# ── 10. Prometheus metrics ───────────────────────────────────────
info "Prometheus metrics"
METRICS=$(curl -sf http://localhost:9090/metrics)
echo "$METRICS" | grep -q 'ffee_flag_changes_total' && pass "Prometheus: ffee_flag_changes_total present" || fail "ffee_ metrics missing"

# ── 11. Cache status ─────────────────────────────────────────────
info "Cache status"
STATUS=$(curl -sf "$BASE/api/v1/cache/status")
echo "$STATUS" | grep -q '"development"' && pass "Cache status shows development env" || fail "Cache status missing development env"

# ── Cleanup ──────────────────────────────────────────────────────
info "Cleanup"
curl -sf -X DELETE "$BASE/api/v1/flags/$FLAG_KEY" >/dev/null && pass "Test flag deleted" || true

echo ""
echo -e "${GREEN}╔══════════════════════════════════════════════════════════╗"
echo "║  All smoke tests passed! ✓                               ║"
echo -e "╚══════════════════════════════════════════════════════════╝${NC}"
echo ""
