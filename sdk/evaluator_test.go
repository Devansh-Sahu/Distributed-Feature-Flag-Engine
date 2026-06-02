package ffee

// ─────────────────────────────────────────────────────────────────────────
// SDK unit tests
//
// WHY unit tests matter for the SDK specifically:
//
// The SDK's evaluate() function is on the hot path: it's called hundreds of
// thousands of times per second across every application instance. A subtle
// bug in targeting rule matching or rollout hashing would silently serve
// wrong values to users — with no error logged.
//
// Unit tests here catch:
//   1. Targeting rule operator logic (eq, neq, in, not_in, gt, gte, …)
//   2. Rollout bucket determinism (same user always gets same bucket)
//   3. Rollout bucket independence (flag A and flag B bucket differently)
//   4. Rule precedence (lower priority number wins)
//   5. Default value fall-through when flag is disabled or not found
//   6. Typed variation methods (Bool, String, Float64, JSON)
// ─────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"testing"
)

// ── Helpers ───────────────────────────────────────────────────────────────

// makeClient builds a Client with a pre-populated flag map (no server needed).
func makeClient(flags map[string]FlagState) *Client {
	for k, v := range flags {
		sortTargetingRules(v.TargetingRules)
		flags[k] = v
	}
	c := &Client{flags: flags}
	return c
}

// rawJSON converts a Go value to json.RawMessage for use in FlagState fields.
func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// flagEnabled builds a simple enabled boolean flag.
func flagEnabled(rollout int, rules ...TargetingRule) FlagState {
	return FlagState{
		FlagKey:           "test-flag",
		FlagType:          "boolean",
		Enabled:           true,
		DefaultValue:      rawJSON(true),
		RolloutPercentage: rollout,
		TargetingRules:    rules,
	}
}

// ── BoolVariation ────────────────────────────────────────────────────────

func TestBoolVariation_FlagNotFound(t *testing.T) {
	c := makeClient(map[string]FlagState{})
	got := c.BoolVariation("missing-flag", UserContext{UserID: "u1"}, false)
	if got != false {
		t.Errorf("expected default false, got %v", got)
	}
}

func TestBoolVariation_FlagDisabled(t *testing.T) {
	c := makeClient(map[string]FlagState{
		"my-flag": {
			FlagKey: "my-flag", Enabled: false,
			DefaultValue: rawJSON(true), RolloutPercentage: 100,
		},
	})
	got := c.BoolVariation("my-flag", UserContext{UserID: "u1"}, false)
	if got != false {
		t.Errorf("disabled flag should return default, got %v", got)
	}
}

func TestBoolVariation_100PercentRollout(t *testing.T) {
	c := makeClient(map[string]FlagState{"f": flagEnabled(100)})
	users := []string{"alice", "bob", "charlie", "david", "u-999", ""}
	for _, u := range users {
		got := c.BoolVariation("f", UserContext{UserID: u}, false)
		if !got {
			t.Errorf("user %q: 100%% rollout should always return true, got false", u)
		}
	}
}

func TestBoolVariation_ZeroPercentRollout(t *testing.T) {
	c := makeClient(map[string]FlagState{"f": flagEnabled(0)})
	users := []string{"alice", "bob", "charlie", "u-999"}
	for _, u := range users {
		// Test with default value = false
		got := c.BoolVariation("f", UserContext{UserID: u}, false)
		if got {
			t.Errorf("user %q: 0%% rollout should always return default (false), got true", u)
		}
		// Test with default value = true
		got = c.BoolVariation("f", UserContext{UserID: u}, true)
		if !got {
			t.Errorf("user %q: 0%% rollout should always return default (true), got false", u)
		}
	}
}

func TestBoolVariation_CustomDefault(t *testing.T) {
	c := makeClient(map[string]FlagState{})
	if got := c.BoolVariation("x", UserContext{UserID: "u"}, true); !got {
		t.Error("expected custom default true, got false")
	}
}

// ── Rollout hashing: determinism and independence ─────────────────────────

func TestHashUserBucket_Deterministic(t *testing.T) {
	// Same inputs must ALWAYS produce same bucket — this is the contract
	// that makes rollout stable ("a user never flips between on and off").
	for _, tc := range []struct{ flag, user string }{
		{"flag-a", "user-123"},
		{"flag-b", "alice@example.com"},
		{"checkout", ""},
	} {
		b1 := hashUserBucket(tc.flag, tc.user)
		b2 := hashUserBucket(tc.flag, tc.user)
		b3 := hashUserBucket(tc.flag, tc.user)
		if b1 != b2 || b2 != b3 {
			t.Errorf("hashUserBucket(%q, %q) is not deterministic: %d %d %d",
				tc.flag, tc.user, b1, b2, b3)
		}
	}
}

func TestHashUserBucket_InRange(t *testing.T) {
	// Bucket must be in [0, 100) — the rollout percentage comparison uses <
	for _, user := range []string{"a", "b", "c", "user-0", "user-99999", "🚀"} {
		b := hashUserBucket("my-flag", user)
		if b < 0 || b >= 100 {
			t.Errorf("hashUserBucket returned %d — out of [0,100) for user %q", b, user)
		}
	}
}

func TestHashUserBucket_FlagIndependence(t *testing.T) {
	// The SAME user must potentially get DIFFERENT buckets for different flags.
	// If all flags gave the same bucket, a user at bucket 30 would ALWAYS see
	// every feature with rollout > 30%, making independent flag control impossible.
	//
	// We can't guarantee every pair of flags produces different buckets
	// (collisions are possible), but across a large enough set of flags,
	// the distribution should vary.
	user := "test-user-independence"
	buckets := make(map[int]int)
	for i := 0; i < 100; i++ {
		flag := "flag-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
		buckets[hashUserBucket(flag, user)]++
	}
	// With 100 different flags, we expect at least 10 distinct buckets
	// (FNV should distribute well; in practice we see 60-80 distinct values).
	if len(buckets) < 10 {
		t.Errorf("poor distribution: only %d distinct buckets across 100 flags", len(buckets))
	}
}

func TestRollout_SameUserSameBucket_AcrossEvaluations(t *testing.T) {
	// Verifies the end-to-end property: same user always gets same answer.
	c := makeClient(map[string]FlagState{"feature": flagEnabled(50)})
	user := UserContext{UserID: "stable-user-42"}
	var first *bool
	for i := 0; i < 20; i++ {
		got := c.BoolVariation("feature", user, false)
		if first == nil {
			first = &got
		} else if got != *first {
			t.Errorf("evaluation #%d returned %v but first returned %v — not stable!", i, got, *first)
		}
	}
}

// ── Targeting rules ───────────────────────────────────────────────────────

func TestTargetingRule_Eq(t *testing.T) {
	rule := TargetingRule{
		Priority:   0,
		Attribute:  "plan",
		Operator:   "eq",
		Value:      rawJSON("pro"),
		ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	// Matching user
	got := c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "pro"}}, false)
	if !got {
		t.Error("eq rule: pro user should get true")
	}

	// Non-matching user — rollout is 0% so falls through to default false
	got = c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "free"}}, false)
	if got {
		t.Error("eq rule: free user should not match, rollout 0% → default false")
	}
}

func TestTargetingRule_Neq(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "plan", Operator: "neq",
		Value: rawJSON("free"), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "pro"}}, false) {
		t.Error("neq rule: pro != free → should return true")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "free"}}, false) {
		t.Error("neq rule: free == free → should not match")
	}
}

func TestTargetingRule_In(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "country", Operator: "in",
		Value: rawJSON([]string{"IN", "US", "GB"}), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	for _, country := range []string{"IN", "US", "GB"} {
		got := c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"country": country}}, false)
		if !got {
			t.Errorf("in rule: %q should be in [IN,US,GB]", country)
		}
	}
	for _, country := range []string{"DE", "FR", "AU"} {
		got := c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"country": country}}, false)
		if got {
			t.Errorf("in rule: %q should NOT be in [IN,US,GB]", country)
		}
	}
}

func TestTargetingRule_NotIn(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "country", Operator: "not_in",
		Value: rawJSON([]string{"CN", "RU"}), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	// IN should match not_in (they are not in the blocked list)
	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"country": "IN"}}, false) {
		t.Error("not_in: IN is not in [CN,RU] → should return true")
	}
	// CN should not match
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"country": "CN"}}, false) {
		t.Error("not_in: CN is in [CN,RU] → should not match")
	}
}

func TestTargetingRule_NumericGt(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "age", Operator: "gt",
		Value: rawJSON(18.0), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"age": 25.0}}, false) {
		t.Error("gt: 25 > 18 → should match")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"age": 18.0}}, false) {
		t.Error("gt: 18 == 18, not >, should not match")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"age": 10.0}}, false) {
		t.Error("gt: 10 < 18 → should not match")
	}
}

func TestTargetingRule_Gte(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "score", Operator: "gte",
		Value: rawJSON(100.0), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"score": 100.0}}, false) {
		t.Error("gte: 100 >= 100 → should match")
	}
	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"score": 150.0}}, false) {
		t.Error("gte: 150 >= 100 → should match")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"score": 99.0}}, false) {
		t.Error("gte: 99 < 100 → should not match")
	}
}

func TestTargetingRule_Contains(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "email", Operator: "contains",
		Value: rawJSON("@company.com"), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"email": "dev@company.com"}}, false) {
		t.Error("contains: company email should match")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"email": "user@gmail.com"}}, false) {
		t.Error("contains: gmail should not match")
	}
}

func TestTargetingRule_StartsWith(t *testing.T) {
	rule := TargetingRule{
		Priority: 0, Attribute: "plan", Operator: "starts_with",
		Value: rawJSON("enterprise"), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	if !c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "enterprise_plus"}}, false) {
		t.Error("starts_with: enterprise_plus starts with 'enterprise' → match")
	}
	if c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "pro"}}, false) {
		t.Error("starts_with: pro does not start with 'enterprise' → no match")
	}
}

func TestTargetingRule_MissingAttribute(t *testing.T) {
	// If the user doesn't have the attribute at all, the rule must NOT match
	rule := TargetingRule{
		Priority: 0, Attribute: "plan", Operator: "eq",
		Value: rawJSON("pro"), ServeValue: rawJSON(true),
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rule)})

	// User has NO attributes
	got := c.BoolVariation("f", UserContext{UserID: "u"}, false)
	if got {
		t.Error("missing attribute: rule should NOT match when attribute is absent")
	}
}

func TestTargetingRule_Priority(t *testing.T) {
	// Rule with priority=0 fires first, rule with priority=1 fires second.
	// A user matching priority=0 should NEVER see priority=1's serve_value.
	rules := []TargetingRule{
		{Priority: 1, Attribute: "plan", Operator: "eq", Value: rawJSON("pro"), ServeValue: rawJSON(false)},
		{Priority: 0, Attribute: "plan", Operator: "eq", Value: rawJSON("pro"), ServeValue: rawJSON(true)},
	}
	c := makeClient(map[string]FlagState{"f": flagEnabled(0, rules...)})

	got := c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "pro"}}, false)
	if !got {
		t.Error("priority 0 rule (→ true) should win over priority 1 rule (→ false)")
	}
}

func TestTargetingRule_ServeValue_FalseExplicit(t *testing.T) {
	// A rule can explicitly serve `false` — this is how you create exceptions:
	// e.g. "if plan == 'beta', show false even if global rollout is 100%"
	rule := TargetingRule{
		Priority: 0, Attribute: "plan", Operator: "eq",
		Value: rawJSON("beta"), ServeValue: rawJSON(false),
	}
	c := makeClient(map[string]FlagState{
		"f": {
			FlagKey: "f", Enabled: true,
			DefaultValue: rawJSON(true), RolloutPercentage: 100,
			TargetingRules: []TargetingRule{rule},
		},
	})

	// Beta user should get the explicit false serve_value
	got := c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "beta"}}, true)
	if got {
		t.Error("beta user should get explicit false from targeting rule")
	}

	// Non-beta user should get 100% rollout = true
	got = c.BoolVariation("f", UserContext{UserID: "u", Attributes: map[string]any{"plan": "pro"}}, false)
	if !got {
		t.Error("pro user should get 100% rollout = true")
	}
}

// ── Typed variation methods ───────────────────────────────────────────────

func TestStringVariation(t *testing.T) {
	c := makeClient(map[string]FlagState{
		"theme": {
			FlagKey: "theme", Enabled: true,
			DefaultValue: rawJSON("dark"), RolloutPercentage: 100,
		},
	})
	got := c.StringVariation("theme", UserContext{UserID: "u"}, "light")
	if got != "dark" {
		t.Errorf("expected 'dark', got %q", got)
	}
}

func TestStringVariation_Default(t *testing.T) {
	c := makeClient(map[string]FlagState{})
	got := c.StringVariation("missing", UserContext{UserID: "u"}, "fallback")
	if got != "fallback" {
		t.Errorf("expected 'fallback', got %q", got)
	}
}

func TestFloat64Variation(t *testing.T) {
	c := makeClient(map[string]FlagState{
		"discount": {
			FlagKey: "discount", Enabled: true,
			DefaultValue: rawJSON(0.15), RolloutPercentage: 100,
		},
	})
	got := c.Float64Variation("discount", UserContext{UserID: "u"}, 0.0)
	if got != 0.15 {
		t.Errorf("expected 0.15, got %v", got)
	}
}

func TestJSONVariation(t *testing.T) {
	config := map[string]any{"maxRetries": 3, "timeout": 30}
	c := makeClient(map[string]FlagState{
		"retry-config": {
			FlagKey: "retry-config", Enabled: true,
			DefaultValue: rawJSON(config), RolloutPercentage: 100,
		},
	})
	var result map[string]any
	if err := c.JSONVariation("retry-config", UserContext{UserID: "u"}, &result); err != nil {
		t.Fatalf("JSONVariation error: %v", err)
	}
	if result["maxRetries"] != float64(3) {
		t.Errorf("expected maxRetries=3, got %v", result["maxRetries"])
	}
}

// ── OnFlagUpdate callback ─────────────────────────────────────────────────

func TestOnFlagUpdate_FiresOnUpdate(t *testing.T) {
	c := makeClient(map[string]FlagState{
		"f": {FlagKey: "f", Enabled: false, DefaultValue: rawJSON(false), RolloutPercentage: 0},
	})

	var received []FlagState
	c.OnFlagUpdate(func(s FlagState) { received = append(received, s) })

	newState := FlagState{FlagKey: "f", Enabled: true, DefaultValue: rawJSON(true), RolloutPercentage: 100}
	c.updateFlag(newState)

	if len(received) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(received))
	}
	if !received[0].Enabled {
		t.Error("callback received wrong state: Enabled should be true")
	}

	// The in-memory map should also be updated
	if !c.BoolVariation("f", UserContext{UserID: "u"}, false) {
		t.Error("after updateFlag, BoolVariation should return true")
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────
// Run with: go test -bench=. -benchmem -benchtime=5s

func BenchmarkBoolVariation_NoRules_100Pct(b *testing.B) {
	c := makeClient(map[string]FlagState{
		"bench-flag": flagEnabled(100),
	})
	ctx := UserContext{UserID: "bench-user"}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.BoolVariation("bench-flag", ctx, false)
	}
}

func BenchmarkBoolVariation_50PctRollout(b *testing.B) {
	c := makeClient(map[string]FlagState{
		"bench-flag": flagEnabled(50),
	})
	ctx := UserContext{UserID: "bench-user-stable"}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.BoolVariation("bench-flag", ctx, false)
	}
}

func BenchmarkBoolVariation_WithTargetingRules(b *testing.B) {
	rules := make([]TargetingRule, 5)
	for i := range rules {
		rules[i] = TargetingRule{
			Priority: i, Attribute: "plan", Operator: "eq",
			Value: rawJSON("tier-" + string(rune('a'+i))), ServeValue: rawJSON(true),
		}
	}
	c := makeClient(map[string]FlagState{
		"bench-flag": flagEnabled(50, rules...),
	})
	ctx := UserContext{UserID: "bench-user", Attributes: map[string]any{"plan": "pro"}}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.BoolVariation("bench-flag", ctx, false)
	}
}

func BenchmarkHashUserBucket(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		hashUserBucket("my-feature-flag", "user-abc-123")
	}
}
