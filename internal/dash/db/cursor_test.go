package db

import (
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	cases := []Cursor{
		{},
		{K: []string{"2026-07-02T09:14:02Z"}, ID: "b-17"},
		{K: []string{"20", "hunter"}, ID: ""},
		{K: nil, ID: "solo"},
	}
	for _, want := range cases {
		enc := EncodeCursor(want)
		got, err := DecodeCursor(enc)
		if err != nil {
			t.Fatalf("DecodeCursor(%q) error: %v", enc, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
		}
	}
}

func TestDecodeCursorEmpty(t *testing.T) {
	got, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor should decode to zero value, got error %v", err)
	}
	if !reflect.DeepEqual(got, Cursor{}) {
		t.Errorf("empty cursor: got %+v want zero Cursor", got)
	}
}

func TestDecodeCursorTampered(t *testing.T) {
	valid := EncodeCursor(Cursor{K: []string{"20"}, ID: "hunter"})
	tampered := []string{
		valid + "!!", // not base64url
		"!!!!",       // not base64url
		base64.RawURLEncoding.EncodeToString([]byte("not json")),                      // valid b64, invalid JSON
		base64.RawURLEncoding.EncodeToString([]byte(`{"k":["x"],"id":"y","evil":1}`)), // unknown field
		base64.RawURLEncoding.EncodeToString([]byte(`{"k":["x"]}{"k":["y"]}`)),        // trailing garbage
	}
	for _, s := range tampered {
		if _, err := DecodeCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) = err %v, want ErrInvalidCursor", s, err)
		}
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 50}, {-1, 50}, {-1000, 50},
		{1, 1}, {49, 49}, {50, 50}, {199, 199}, {200, 200},
		{201, 200}, {10000, 200},
	}
	for _, c := range cases {
		if got := ClampLimit(c.in); got != c.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
