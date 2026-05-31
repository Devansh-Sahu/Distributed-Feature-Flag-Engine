-- Migration 002: Postgres LISTEN/NOTIFY trigger for flag cache invalidation
--
-- WHY LISTEN/NOTIFY instead of application-level dual-write?
-- Dual-write: app writes to DB, then writes to Redis. If the Redis write fails,
-- the two systems diverge silently. You never know which one is stale.
--
-- LISTEN/NOTIFY: Postgres sends the notification as part of the committed
-- transaction. If the transaction commits, the notification is guaranteed to fire.
-- If the transaction rolls back, no notification is sent. Zero divergence window.
--
-- This is our Phase 2 cache invalidation strategy. In Phase 3, we'll replace
-- this with Debezium CDC → Kafka, which provides the same guarantee but also
-- works when the server is down (Kafka buffers events; NOTIFY does not).

-- ── Trigger function ──────────────────────────────────────────────
-- Fires after any INSERT or UPDATE on flag_configs.
-- Sends a NOTIFY on channel 'flag_config_changed' with a JSON payload
-- containing enough info for the Go listener to re-materialize that flag.
CREATE OR REPLACE FUNCTION notify_flag_config_changed()
RETURNS TRIGGER AS $$
DECLARE
    flag_key_val VARCHAR(200);
    env_name_val VARCHAR(100);
    payload      JSON;
BEGIN
    -- Resolve the flag key and environment name for the changed config row
    SELECT f.key, e.name
    INTO flag_key_val, env_name_val
    FROM flags f
    JOIN environments e ON e.id = NEW.environment_id
    WHERE f.id = NEW.flag_id;

    -- Build the notification payload
    payload := json_build_object(
        'flag_key',           flag_key_val,
        'env_name',           env_name_val,
        'flag_config_id',     NEW.id,
        'flag_id',            NEW.flag_id,
        'environment_id',     NEW.environment_id,
        'enabled',            NEW.enabled,
        'rollout_percentage', NEW.rollout_percentage,
        'updated_at',         NEW.updated_at
    );

    -- pg_notify payload is limited to 8000 bytes — our payload is ~200 bytes, fine.
    -- The Go listener uses this payload to know WHICH flag changed, then does a
    -- full re-read from DB to get the complete state (including targeting rules).
    PERFORM pg_notify('flag_config_changed', payload::text);

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ── Attach trigger to flag_configs ────────────────────────────────
-- AFTER ensures the notification fires only when the row is committed.
-- FOR EACH ROW means one notification per changed row (not per statement).
DROP TRIGGER IF EXISTS trg_flag_config_notify ON flag_configs;
CREATE TRIGGER trg_flag_config_notify
    AFTER INSERT OR UPDATE ON flag_configs
    FOR EACH ROW EXECUTE FUNCTION notify_flag_config_changed();

-- ── Also trigger on targeting_rules changes ───────────────────────
-- Targeting rules affect flag evaluation, so a rule change must also
-- invalidate the cached flag state.
CREATE OR REPLACE FUNCTION notify_targeting_rule_changed()
RETURNS TRIGGER AS $$
DECLARE
    flag_key_val VARCHAR(200);
    env_name_val VARCHAR(100);
    config_id    UUID;
    payload      JSON;
BEGIN
    -- For DELETE, use OLD; for INSERT/UPDATE, use NEW
    IF TG_OP = 'DELETE' THEN
        config_id := OLD.flag_config_id;
    ELSE
        config_id := NEW.flag_config_id;
    END IF;

    SELECT f.key, e.name
    INTO flag_key_val, env_name_val
    FROM flag_configs fc
    JOIN flags f       ON f.id  = fc.flag_id
    JOIN environments e ON e.id = fc.environment_id
    WHERE fc.id = config_id;

    payload := json_build_object(
        'flag_key', flag_key_val,
        'env_name', env_name_val,
        'reason',   'targeting_rule_changed'
    );

    PERFORM pg_notify('flag_config_changed', payload::text);

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_targeting_rule_notify ON targeting_rules;
CREATE TRIGGER trg_targeting_rule_notify
    AFTER INSERT OR UPDATE OR DELETE ON targeting_rules
    FOR EACH ROW EXECUTE FUNCTION notify_targeting_rule_changed();
