package ffee

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────
// Evaluator: targeting rules + rollout percentage
//
// This is the hottest code in the SDK. Every feature flag check calls here.
// Design goals:
//   - Zero allocations on the happy path (flag enabled, no targeting rules)
//   - Pure in-process: no network, no disk, no goroutines
//   - Deterministic: same input always produces same output
//   - < 1µs for simple flags, < 5µs for flags with targeting rules
// ─────────────────────────────────────────────────────────────────────────

// matchesRule tests whether a user context satisfies a single targeting rule.
// Returns true if the rule matches and its serve_value should be returned.
func matchesRule(rule TargetingRule, ctx UserContext) bool {
	// Get the attribute value from the user context
	attrVal, exists := ctx.Attributes[rule.Attribute]
	if !exists {
		return false
	}

	switch rule.Operator {
	case "eq":
		return equalJSON(attrVal, rule.Value)

	case "neq":
		return !equalJSON(attrVal, rule.Value)

	case "in":
		// rule.Value is a JSON array: ["IN", "US", "GB"]
		// We check if attrVal is one of those values.
		var arr []json.RawMessage
		if err := json.Unmarshal(rule.Value, &arr); err != nil {
			return false
		}
		for _, item := range arr {
			if equalJSON(attrVal, item) {
				return true
			}
		}
		return false

	case "not_in":
		var arr []json.RawMessage
		if err := json.Unmarshal(rule.Value, &arr); err != nil {
			return false
		}
		for _, item := range arr {
			if equalJSON(attrVal, item) {
				return false
			}
		}
		return true

	case "gt", "gte", "lt", "lte":
		return numericCompare(rule.Operator, attrVal, rule.Value)

	case "contains":
		s, ok := attrVal.(string)
		if !ok {
			return false
		}
		var target string
		if err := json.Unmarshal(rule.Value, &target); err != nil {
			return false
		}
		return strings.Contains(s, target)

	case "starts_with":
		s, ok := attrVal.(string)
		if !ok {
			return false
		}
		var prefix string
		if err := json.Unmarshal(rule.Value, &prefix); err != nil {
			return false
		}
		return strings.HasPrefix(s, prefix)
	}

	return false
}

// hashUserBucket deterministically maps a (flagKey, userID) pair to a bucket
// in the range [0, 100). The same pair always returns the same bucket.
//
// WHY FNV-1a?
//   - Pure Go, no imports beyond stdlib
//   - Non-cryptographic — we don't need security, we need speed and distribution
//   - Excellent distribution for string keys (uniform across [0, 100))
//   - ~5ns per call on modern hardware
//
// The key insight: we hash flagKey + ":" + userID together, NOT just userID.
// This means a user can be in the rollout for flag A but not flag B — the
// buckets are INDEPENDENT per flag. Without the flagKey in the hash, all
// flags with 50% rollout would always select the same 50% of users.
func hashUserBucket(flagKey, userID string) int {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s:%s", flagKey, userID)
	return int(h.Sum32() % 100)
}

// equalJSON compares a Go value (from UserContext.Attributes) to a JSON value
// (from a targeting rule). Handles string, bool, and numeric types.
func equalJSON(goVal any, rawJSON json.RawMessage) bool {
	switch v := goVal.(type) {
	case string:
		var s string
		if err := json.Unmarshal(rawJSON, &s); err != nil {
			return false
		}
		return v == s
	case bool:
		var b bool
		if err := json.Unmarshal(rawJSON, &b); err != nil {
			return false
		}
		return v == b
	case float64:
		var f float64
		if err := json.Unmarshal(rawJSON, &f); err != nil {
			return false
		}
		return v == f
	case int:
		var f float64
		if err := json.Unmarshal(rawJSON, &f); err != nil {
			return false
		}
		return float64(v) == f
	case int64:
		var f float64
		if err := json.Unmarshal(rawJSON, &f); err != nil {
			return false
		}
		return float64(v) == f
	}
	return false
}

// numericCompare performs gt/gte/lt/lte comparison between a Go value and a JSON number.
func numericCompare(op string, goVal any, rawJSON json.RawMessage) bool {
	// Convert goVal to float64
	var a float64
	switch v := goVal.(type) {
	case float64:
		a = v
	case int:
		a = float64(v)
	case int64:
		a = float64(v)
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return false
		}
		a = f
	default:
		return false
	}

	var b float64
	if err := json.Unmarshal(rawJSON, &b); err != nil {
		return false
	}

	switch op {
	case "gt":
		return a > b
	case "gte":
		return a >= b
	case "lt":
		return a < b
	case "lte":
		return a <= b
	}
	return false
}
