package crm

import "testing"

// PushKey is the G2 idempotency anchor: deterministic, distinct on every field, and NUL-delimited so no
// field boundary can be forged by concatenation (ADR-0030).
func TestPushKey(t *testing.T) {
	base := PushKey("acme", "conn1", "acme.com", 1, "v1")

	if base != PushKey("acme", "conn1", "acme.com", 1, "v1") {
		t.Fatal("PushKey must be deterministic")
	}
	if len(base) != 64 {
		t.Fatalf("expected a 64-hex-char sha256 digest, got %d", len(base))
	}

	cases := map[string]string{
		"tenant":  PushKey("globex", "conn1", "acme.com", 1, "v1"),
		"conn":    PushKey("acme", "conn2", "acme.com", 1, "v1"),
		"record":  PushKey("acme", "conn1", "other.com", 1, "v1"),
		"mapver":  PushKey("acme", "conn1", "acme.com", 2, "v1"),
		"dossver": PushKey("acme", "conn1", "acme.com", 1, "v2"),
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("PushKey must differ when %s differs", name)
		}
	}

	// Field boundaries must not be forgeable by shifting characters across the delimiter.
	if PushKey("a", "bc", "d", 1, "v") == PushKey("ab", "c", "d", 1, "v") {
		t.Fatal("NUL delimiter must prevent boundary forgery")
	}
}
