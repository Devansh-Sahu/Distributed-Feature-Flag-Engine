-- ─────────────────────────────────────────────────────────────────
-- Migration 001: Initial schema for Feature Flag Engine
-- Run by: postgres docker-entrypoint-initdb.d on first boot
-- ─────────────────────────────────────────────────────────────────

-- Required for UUID generation (built-in in PG 16)
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── Environments ──────────────────────────────────────────────────
-- Examples: "production", "staging", "development"
-- Every flag has independent configuration per environment
CREATE TABLE IF NOT EXISTS environments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL UNIQUE,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed three standard environments automatically
INSERT INTO environments (name, description) VALUES
    ('production',   'Live production environment'),
    ('staging',      'Pre-production staging environment'),
    ('development',  'Local development environment')
ON CONFLICT (name) DO NOTHING;

-- ── Flag definitions ──────────────────────────────────────────────
-- The flag itself is environment-agnostic; config is per-environment
CREATE TABLE IF NOT EXISTS flags (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         VARCHAR(200) NOT NULL UNIQUE,   -- e.g. "new-checkout-flow"
    name        VARCHAR(200) NOT NULL,
    description TEXT,
    flag_type   VARCHAR(20)  NOT NULL DEFAULT 'boolean'
                    CHECK (flag_type IN ('boolean','string','number','json')),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ── Per-environment flag configuration ────────────────────────────
-- Each (flag, environment) pair has its own enabled state, default
-- value, and rollout percentage. This is the row that Debezium will
-- watch via WAL in Phase 3.
CREATE TABLE IF NOT EXISTS flag_configs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flag_id             UUID        NOT NULL REFERENCES flags(id)        ON DELETE CASCADE,
    environment_id      UUID        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    enabled             BOOLEAN     NOT NULL DEFAULT false,
    default_value       JSONB       NOT NULL DEFAULT 'false',
    rollout_percentage  INTEGER     NOT NULL DEFAULT 0
                            CHECK (rollout_percentage >= 0 AND rollout_percentage <= 100),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(flag_id, environment_id)
);

-- ── Targeting rules ───────────────────────────────────────────────
-- Attribute-based rules evaluated in priority order (lower = first)
-- Example: { attribute: "user.country", operator: "in", value: ["IN","US"] }
CREATE TABLE IF NOT EXISTS targeting_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flag_config_id  UUID        NOT NULL REFERENCES flag_configs(id) ON DELETE CASCADE,
    priority        INTEGER     NOT NULL DEFAULT 0,  -- lower number = evaluated first
    attribute       VARCHAR(200) NOT NULL,            -- e.g. "user.country"
    operator        VARCHAR(20)  NOT NULL             -- eq,neq,in,not_in,gt,lt,gte,lte,contains,starts_with
                        CHECK (operator IN ('eq','neq','in','not_in','gt','lt','gte','lte','contains','starts_with')),
    value           JSONB        NOT NULL,            -- e.g. ["IN","US"] or "pro"
    serve_value     JSONB        NOT NULL,            -- value returned when this rule matches
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ── Audit log ─────────────────────────────────────────────────────
-- Immutable append-only record of every flag change.
-- Also the WAL source-of-truth for Debezium CDC in Phase 3.
CREATE TABLE IF NOT EXISTS flag_audit_log (
    id              BIGSERIAL    PRIMARY KEY,
    flag_key        VARCHAR(200) NOT NULL,
    environment_id  UUID,
    action          VARCHAR(50)  NOT NULL,  -- created,updated,enabled,disabled,rule_added,rule_removed
    changed_by      VARCHAR(200),           -- user/service that made the change
    old_value       JSONB,
    new_value       JSONB,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ── Indexes ───────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_flag_configs_flag_env
    ON flag_configs(flag_id, environment_id);

CREATE INDEX IF NOT EXISTS idx_targeting_rules_config
    ON targeting_rules(flag_config_id, priority);

CREATE INDEX IF NOT EXISTS idx_audit_log_flag_key
    ON flag_audit_log(flag_key, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_flags_key
    ON flags(key);

-- ── Auto-update updated_at trigger ────────────────────────────────
-- This fires on every UPDATE so updated_at is always accurate.
-- The WAL diff for Debezium will always include updated_at as a
-- sentinel field that guarantees the row shows up in the WAL stream.
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER trg_flags_updated_at
    BEFORE UPDATE ON flags
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE OR REPLACE TRIGGER trg_flag_configs_updated_at
    BEFORE UPDATE ON flag_configs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ── WAL publication (needed for Debezium in Phase 3) ──────────────
-- Creating now so Debezium can use it without schema changes later
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_publication WHERE pubname = 'ffee_publication'
    ) THEN
        EXECUTE 'CREATE PUBLICATION ffee_publication FOR TABLE flags, flag_configs, targeting_rules';
    END IF;
END $$;

-- ── Replication slot (needed for Debezium in Phase 3) ─────────────
-- Pre-creating it here means Phase 3 needs zero schema migrations
-- NOTE: Only one slot per name is allowed; this is idempotent.
-- The slot keeps WAL from being garbage-collected until Debezium
-- consumes it — be careful not to leave Debezium disconnected for
-- long periods on a busy cluster (WAL bloat risk).
SELECT pg_create_logical_replication_slot('ffee_debezium_slot', 'pgoutput')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_replication_slots WHERE slot_name = 'ffee_debezium_slot'
);
