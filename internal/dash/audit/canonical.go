package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// record is the exact document that gets hashed: the Entry plus the three context-derived
// fields (tenant_id, seq, created_at). tenant_id and created_at come from the request
// Principal / clock, never from Entry, so they cannot be spoofed by a caller.
type record struct {
	TenantID  string
	Seq       int64
	CreatedAt time.Time
	Entry     Entry
}

// canonicalize produces the canonical JSON byte layout hashed by the chain (doc 05 §8.1):
//
//	UTF-8, object keys sorted lexicographically (byte order), no insignificant whitespace,
//	created_at as an RFC 3339 UTC string, before/after inlined as their own JSON (also
//	recursively key-sorted), and NULL text columns emitted as "".
//
// The top-level key order is therefore: action, actor_role, actor_user_id, after, before,
// created_at, ip, object_id, object_kind, seq, tenant_id.
func canonicalize(r record) ([]byte, error) {
	before, err := decodeRaw(r.Entry.Before)
	if err != nil {
		return nil, fmt.Errorf("audit: canonical before: %w", err)
	}
	after, err := decodeRaw(r.Entry.After)
	if err != nil {
		return nil, fmt.Errorf("audit: canonical after: %w", err)
	}
	doc := map[string]any{
		"tenant_id":     r.TenantID,
		"seq":           r.Seq,
		"actor_user_id": r.Entry.ActorUserID,
		"actor_role":    r.Entry.ActorRole,
		"action":        r.Entry.Action,
		"object_kind":   r.Entry.ObjectKind,
		"object_id":     r.Entry.ObjectID,
		"before":        before,
		"after":         after,
		"ip":            r.Entry.IP,
		// microsecond precision matches Postgres timestamptz, so a DB round-trip re-hashes
		// identically (Append truncates the stored value to match).
		"created_at": r.CreatedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
	}
	var b bytes.Buffer
	if err := writeCanonical(&b, doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// computeHash is the chain link: sha256(prev_hash || canonical_json). prevHash is 32 bytes
// (32 zero bytes at genesis, seq 1).
func computeHash(prevHash, canonical []byte) []byte {
	h := sha256.New()
	h.Write(prevHash)
	h.Write(canonical)
	return h.Sum(nil)
}

// decodeRaw parses a before/after snapshot into a generic value (numbers preserved verbatim
// via json.Number so re-encoding is lossless). Empty/absent snapshots become null.
func decodeRaw(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// writeCanonical serializes v deterministically: objects with lexicographically sorted keys,
// no whitespace, json.Number and strings passed through with standard JSON escaping.
func writeCanonical(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		s, err := json.Marshal(x)
		if err != nil {
			return err
		}
		b.Write(s)
	case json.Number:
		b.WriteString(x.String())
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		s, err := json.Marshal(x)
		if err != nil {
			return err
		}
		b.Write(s)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // byte-order = lexicographic by UTF-8 code unit
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			ks, err := json.Marshal(k)
			if err != nil {
				return err
			}
			b.Write(ks)
			b.WriteByte(':')
			if err := writeCanonical(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("audit: uncanonicalizable type %T", v)
	}
	return nil
}
