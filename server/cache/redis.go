package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// RedisCache handles reading and writing flag state to Redis.
//
// KEY STRUCTURE (critical design decision — explain this in interviews):
//
//   ffee:state:{envName}   → Redis HASH
//     field: {flagKey}
//     value: JSON-serialized FlagState
//
// WHY a HASH per environment instead of one key per flag?
//
// Option A (one key per flag+env): ffee:state:production:new-checkout-flow → FlagState JSON
//   - SDK startup: N Redis calls (one per flag). With 1000 flags, that's 1000 RTTs.
//   - Update: 1 HSET call.
//
// Option B (hash per env): ffee:state:production → HASH[flagKey → FlagState JSON]  ← WE USE THIS
//   - SDK startup: 1 HGETALL call loads ALL flags for the environment. Always O(1) RTTs.
//   - Update: 1 HSET call updates just the changed flag.
//   - Memory: slightly higher (one hash object), but Redis hashes are extremely efficient
//     for small-to-medium sets (< 128 fields use ziplist encoding, near-zero overhead).
//
// With 10,000 flags and 3 environments, you have 3 hash keys in Redis.
// SDK startup = 3 Redis calls total. This is the right tradeoff.
//
// PUB/SUB CHANNEL: ffee:updates:{envName}
//   Published on every flag state change. Payload = full FlagState JSON.
//   SDK instances subscribed to this channel update their local in-memory copy.

const (
	// stateKeyPrefix is the prefix for the per-environment hash keys.
	stateKeyPrefix = "ffee:state:"
	// updatesChannelPrefix is the pub/sub channel prefix.
	updatesChannelPrefix = "ffee:updates:"
)

// RedisCache wraps a Redis client and provides typed operations for flag state.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache creates a new RedisCache.
func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// StateKey returns the Redis HASH key for an environment's flag state.
// e.g. "ffee:state:production"
func StateKey(envName string) string {
	return stateKeyPrefix + envName
}

// UpdatesChannel returns the Redis pub/sub channel name for an environment.
// e.g. "ffee:updates:production"
func UpdatesChannel(envName string) string {
	return updatesChannelPrefix + envName
}

// SetFlagState writes a single flag's state into the environment hash.
// Also publishes to the pub/sub channel so connected SDK instances update immediately.
//
// WHY HSET + PUBLISH in the same function?
// Atomicity: we want both to happen together. If we published before writing,
// an SDK might receive the event and re-read the old value from Redis.
// If we wrote but didn't publish, SDKs would lag until their next poll (Phase 3).
// HSET first, PUBLISH second is the correct order.
func (c *RedisCache) SetFlagState(ctx context.Context, envName string, state FlagState) error {
	state.MaterializedAt = time.Now().UTC()

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal flag state: %w", err)
	}

	pipe := c.client.Pipeline()
	pipe.HSet(ctx, StateKey(envName), state.FlagKey, data)
	pipe.Publish(ctx, UpdatesChannel(envName), data)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline (hset+publish): %w", err)
	}

	log.Debug().
		Str("env", envName).
		Str("flag", state.FlagKey).
		Bool("enabled", state.Enabled).
		Int("rollout_pct", state.RolloutPercentage).
		Msg("flag state materialized to Redis")

	return nil
}

// GetFlagState retrieves a single flag's state from Redis.
// Returns (nil, nil) if the flag is not in cache (cache miss — caller should read from DB).
func (c *RedisCache) GetFlagState(ctx context.Context, envName, flagKey string) (*FlagState, error) {
	data, err := c.client.HGet(ctx, StateKey(envName), flagKey).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("redis HGET: %w", err)
	}

	var state FlagState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal flag state: %w", err)
	}
	return &state, nil
}

// GetAllFlagStates retrieves all flag states for an environment in one Redis call.
// This is the SDK bootstrap operation — called once on SDK startup.
// Returns an empty map (not nil) on cache miss.
func (c *RedisCache) GetAllFlagStates(ctx context.Context, envName string) (map[string]FlagState, error) {
	raw, err := c.client.HGetAll(ctx, StateKey(envName)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis HGETALL: %w", err)
	}

	states := make(map[string]FlagState, len(raw))
	for flagKey, data := range raw {
		var state FlagState
		if err := json.Unmarshal([]byte(data), &state); err != nil {
			log.Warn().Err(err).Str("flag", flagKey).Msg("failed to unmarshal flag state from Redis, skipping")
			continue
		}
		states[flagKey] = state
	}
	return states, nil
}

// DeleteFlagState removes a flag from the environment hash.
// Called when a flag is deleted via the API.
func (c *RedisCache) DeleteFlagState(ctx context.Context, envName, flagKey string) error {
	err := c.client.HDel(ctx, StateKey(envName), flagKey).Err()
	if err != nil {
		return fmt.Errorf("redis HDEL: %w", err)
	}
	return nil
}

// FlagCount returns the number of flags cached for an environment.
// Used by the health/debug endpoint.
func (c *RedisCache) FlagCount(ctx context.Context, envName string) (int64, error) {
	return c.client.HLen(ctx, StateKey(envName)).Result()
}

// Subscribe returns a Redis pub/sub subscription for an environment's updates channel.
// The caller is responsible for calling sub.Close() when done.
func (c *RedisCache) Subscribe(ctx context.Context, envNames ...string) *redis.PubSub {
	channels := make([]string, len(envNames))
	for i, env := range envNames {
		channels[i] = UpdatesChannel(env)
	}
	return c.client.Subscribe(ctx, channels...)
}
