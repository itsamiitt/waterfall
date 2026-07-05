package keys

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransitionTarget(t *testing.T) {
	ok := func(action, from, wantTo string) {
		to, ok := transitionTarget(action, from)
		if !ok || to != wantTo {
			t.Errorf("%s from %s = (%q,%v), want (%q,true)", action, from, to, ok, wantTo)
		}
	}
	bad := func(action, from string) {
		if _, ok := transitionTarget(action, from); ok {
			t.Errorf("%s from %s unexpectedly allowed", action, from)
		}
	}

	// Legal manual transitions (doc 07 §9).
	ok("enable", StatusPaused, StatusActive)
	ok("enable", StatusDisabled, StatusActive)
	ok("disable", StatusActive, StatusDisabled)
	ok("rotate", StatusActive, StatusRotating)
	ok("rotate", StatusPaused, StatusRotating)
	ok("archive", StatusRotating, StatusArchived)
	ok("archive", StatusActive, StatusArchived)

	// Illegal ones must fail closed -> 409 at the HTTP layer.
	bad("enable", StatusActive)   // already active
	bad("enable", StatusArchived) // terminal
	bad("rotate", StatusDisabled) // only active|paused may rotate
	bad("rotate", StatusArchived)
	bad("archive", StatusArchived) // terminal
	bad("disable", StatusArchived)
	bad("bogus", StatusActive)
}

func TestValidStrategies(t *testing.T) {
	for _, s := range []string{"round_robin", "least_used", "weighted", "credit_based",
		"region_based", "lowest_latency", "highest_success", "ai_routing", "random", "priority",
		"failover", "overflow"} {
		if !validStrategies[s] {
			t.Errorf("strategy %q should be valid", s)
		}
	}
	if len(validStrategies) != 12 {
		t.Fatalf("strategy catalog size = %d, want 12", len(validStrategies))
	}
	if validStrategies["not_a_strategy"] {
		t.Fatal("unknown strategy accepted")
	}
}

func TestValidParams(t *testing.T) {
	if err := validParams(""); err != nil {
		t.Errorf("empty params should be valid: %v", err)
	}
	if err := validParams(`{"reband_interval_s":1}`); err != nil {
		t.Errorf("object params should be valid: %v", err)
	}
	if err := validParams(`not json`); err == nil {
		t.Error("invalid JSON should be rejected")
	}
	if err := validParams(`[1,2,3]`); err == nil {
		t.Error("non-object JSON should be rejected")
	}
}

func TestLast4(t *testing.T) {
	if last4("hk_live_9a8b7c6d5e4f3a2b1c0d") != "1c0d" {
		t.Fatalf("last4 wrong: %q", last4("hk_live_9a8b7c6d5e4f3a2b1c0d"))
	}
	if last4("abc") != "abc" {
		t.Fatalf("short last4 wrong")
	}
}

// TestKeyDTO_NeverLeaksSecret is the DTO-side guard for doc 05 §7.3: a serialized key response
// carries secret_last4 but no plaintext/ciphertext field.
func TestKeyDTO_NeverLeaksSecret(t *testing.T) {
	k := Key{
		ID: "id1", ProviderID: "hunter", Label: "prod", Status: StatusActive,
		SecretEnvelopeID: "env1", SecretLast4: "1c0d", Weight: 100,
	}
	b, err := json.Marshal(toKeyDTO(k, "3fa9c1d2"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"secret_last4":"1c0d"`) {
		t.Fatalf("missing secret_last4: %s", out)
	}
	if !strings.Contains(out, `"fingerprint_prefix":"3fa9c1d2"`) {
		t.Fatalf("missing fingerprint_prefix: %s", out)
	}
	for _, banned := range []string{`"secret"`, `"plaintext"`, `"ciphertext"`, `"dek`, `"nonce"`} {
		if strings.Contains(out, banned) {
			t.Fatalf("DTO leaked %s: %s", banned, out)
		}
	}
}

func TestParsePGArray(t *testing.T) {
	s := `{"prod","us-east","a,b"}`
	got := parsePGArray(&s)
	if len(got) != 3 || got[0] != "prod" || got[2] != "a,b" {
		t.Fatalf("parsePGArray = %#v", got)
	}
	empty := `{}`
	if parsePGArray(&empty) != nil {
		t.Fatal("empty array should parse to nil")
	}
	if parsePGArray(nil) != nil {
		t.Fatal("nil should parse to nil")
	}
}

func TestEncodePGArray(t *testing.T) {
	if encodePGArray(nil) != nil {
		t.Fatal("nil slice should encode to SQL NULL")
	}
	got := encodePGArray([]string{"prod", `weird"quote`})
	want := `{"prod","weird\"quote"}`
	if got != want {
		t.Fatalf("encodePGArray = %q, want %q", got, want)
	}
}
