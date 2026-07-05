package db

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// ErrInvalidCursor is returned by DecodeCursor for a non-empty string that is not a
// well-formed cursor (the HTTP layer maps it to 400 invalid_cursor, doc 04 §1.6). It carries
// no detail about the malformed bytes.
var ErrInvalidCursor = errors.New("dash/db: cursor is not decodable")

// Cursor is the decoded keyset position for a paginated list. K holds the last row's sort-key
// value(s) as pre-formatted strings (callers format their own column values, keeping this
// codec type-agnostic and stdlib-only); ID is the tie-break id when the sort key is not
// unique. Cursors carry no authority — a replayed cursor is still filtered by RLS under the
// caller's Principal (doc 04 §1.4).
type Cursor struct {
	K  []string `json:"k"`
	ID string   `json:"id"`
}

// EncodeCursor renders c as an opaque token: base64url (no padding) of its compact JSON. The
// JSON shape is {"k":[...],"id":"..."} — clients MUST treat it as opaque and never parse it.
func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c) // this struct cannot fail to marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reverses EncodeCursor. An empty string decodes to the zero Cursor with no
// error (the first page). Any non-empty string that is not valid base64url of a single,
// field-exact JSON cursor returns ErrInvalidCursor.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Cursor
	if err := dec.Decode(&c); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	if dec.More() { // reject trailing garbage after the object
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// ClampLimit applies the bounded-query window (doc 04 §1.4): a non-positive request defaults
// to 50, and anything above the hard cap is clamped to 200.
func ClampLimit(requested int) int {
	const def, max = 50, 200
	if requested <= 0 {
		return def
	}
	if requested > max {
		return max
	}
	return requested
}
