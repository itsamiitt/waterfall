package configver

import (
	"crypto/sha256"
	"encoding/json"
)

// hashPayload computes the tamper-evidence payload_hash pinned at validate and re-checked at
// publish (doc 07 §6). It canonicalizes the JSON first — unmarshal into the generic tree then
// re-marshal, which sorts object keys deterministically (encoding/json) — so semantically equal
// payloads that differ only in key order or whitespace hash identically. Any edit to the payload
// changes the hash, which is exactly why a PATCH after validate must clear the pin and re-validate.
func hashPayload(payload json.RawMessage) ([]byte, error) {
	canon, err := canonicalize(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canon)
	return sum[:], nil
}

// canonicalize returns the deterministic JSON encoding of raw (sorted object keys, no
// insignificant whitespace). It fails only when raw is not valid JSON.
func canonicalize(raw json.RawMessage) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// isJSONObject reports whether raw decodes to a JSON object (the required top-level shape of a
// config payload).
func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil
}
