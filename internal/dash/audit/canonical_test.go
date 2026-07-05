package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 7, 2, 11, 6, 0, 0, time.UTC)

func TestCanonicalByteLayout(t *testing.T) {
	r := record{
		TenantID:  "acme",
		Seq:       1,
		CreatedAt: fixedTime,
		Entry: Entry{
			Action:      "login",
			ActorUserID: "6f6f6c2e-6f70-4552-a001-9f2d3c4b5a69",
			ActorRole:   "tenant_admin",
			IP:          "203.0.113.7",
		},
	}
	got, err := canonicalize(r)
	if err != nil {
		t.Fatal(err)
	}
	// Keys MUST be lexicographically sorted; created_at RFC3339 UTC; empty snapshots => null.
	want := `{"action":"login",` +
		`"actor_role":"tenant_admin",` +
		`"actor_user_id":"6f6f6c2e-6f70-4552-a001-9f2d3c4b5a69",` +
		`"after":null,` +
		`"before":null,` +
		`"created_at":"2026-07-02T11:06:00Z",` +
		`"ip":"203.0.113.7",` +
		`"object_id":"",` +
		`"object_kind":"",` +
		`"seq":1,` +
		`"tenant_id":"acme"}`
	if string(got) != want {
		t.Errorf("canonical layout mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestCanonicalDeterministicNestedKeys(t *testing.T) {
	// Two before-snapshots with differently ordered keys must canonicalize identically.
	e1 := Entry{Action: "provider_pause", Before: json.RawMessage(`{"b":2,"a":1,"nested":{"y":9,"x":8}}`)}
	e2 := Entry{Action: "provider_pause", Before: json.RawMessage(`{"nested":{"x":8,"y":9},"a":1,"b":2}`)}
	r1 := record{TenantID: "t", Seq: 5, CreatedAt: fixedTime, Entry: e1}
	r2 := record{TenantID: "t", Seq: 5, CreatedAt: fixedTime, Entry: e2}
	c1, err := canonicalize(r1)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := canonicalize(r2)
	if err != nil {
		t.Fatal(err)
	}
	if string(c1) != string(c2) {
		t.Errorf("nested key order changed canonical bytes:\n%s\n%s", c1, c2)
	}
	// nested object keys must appear sorted in the output
	wantFragment := `"before":{"a":1,"b":2,"nested":{"x":8,"y":9}}`
	if !containsSub(string(c1), wantFragment) {
		t.Errorf("nested keys not sorted; got %s", c1)
	}
}

func TestCanonicalLargeIntPreserved(t *testing.T) {
	// json.Number path: a 64-bit id must not be reformatted through float64.
	e := Entry{Action: "x", After: json.RawMessage(`{"id":9007199254740993}`)}
	c, err := canonicalize(record{TenantID: "t", Seq: 1, CreatedAt: fixedTime, Entry: e})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSub(string(c), `"after":{"id":9007199254740993}`) {
		t.Errorf("large int not preserved: %s", c)
	}
}

func TestHashLinkage(t *testing.T) {
	genesis := make([]byte, 32)

	r1 := record{TenantID: "acme", Seq: 1, CreatedAt: fixedTime,
		Entry: Entry{Action: "login", ActorRole: "tenant_admin"}}
	c1, err := canonicalize(r1)
	if err != nil {
		t.Fatal(err)
	}
	h1 := computeHash(genesis, c1)

	// Independent recomputation: sha256(prev || canonical) — the hand-check of the math.
	sum1 := sha256.Sum256(append(append([]byte{}, genesis...), c1...))
	if hex.EncodeToString(h1) != hex.EncodeToString(sum1[:]) {
		t.Fatalf("hash != sha256(prev||canon): %x vs %x", h1, sum1)
	}

	// Second link chains off h1.
	r2 := record{TenantID: "acme", Seq: 2, CreatedAt: fixedTime.Add(time.Minute),
		Entry: Entry{Action: "provider_pause", ObjectKind: "provider", ObjectID: "hunter", ActorRole: "operator"}}
	c2, err := canonicalize(r2)
	if err != nil {
		t.Fatal(err)
	}
	h2 := computeHash(h1, c2)
	sum2 := sha256.Sum256(append(append([]byte{}, h1...), c2...))
	if hex.EncodeToString(h2) != hex.EncodeToString(sum2[:]) {
		t.Fatalf("chained hash mismatch: %x vs %x", h2, sum2)
	}

	// Determinism: recomputing the exact same inputs yields the exact same hash.
	if hex.EncodeToString(computeHash(genesis, c1)) != hex.EncodeToString(h1) {
		t.Fatal("computeHash is not deterministic")
	}

	// Tamper sensitivity: flipping any field breaks the hash.
	r1b := r1
	r1b.Entry.Action = "logout"
	c1b, _ := canonicalize(r1b)
	if hex.EncodeToString(computeHash(genesis, c1b)) == hex.EncodeToString(h1) {
		t.Fatal("hash unchanged after tampering action")
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
