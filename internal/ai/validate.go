package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Validator is implemented by a task's typed output struct: after the LLM completion is
// unmarshalled, Validate() enforces the semantic contract (required fields, ranges, bounds). It is
// the stdlib, struct-based alternative to a general JSON-Schema engine (ADR-0026: no new Go dep).
type Validator interface{ Validate() error }

// ErrSchema is the sentinel for a completion that does not satisfy its typed output contract. The
// cascade treats it as a deterministic "escalate" signal — never a hard success, and never a
// model-self-confidence judgement.
var ErrSchema = errors.New("llm output failed schema validation")

// ValidateInto extracts the first balanced JSON object from raw (tolerating ```json code fences or
// a prose lead-in the model wrapped around it), unmarshals it into out, and runs out.Validate()
// when out implements Validator. Any failure wraps ErrSchema so callers can errors.Is(err, ErrSchema).
func ValidateInto(raw []byte, out any) error {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return fmt.Errorf("%w: no JSON object in completion", ErrSchema)
	}
	if err := json.Unmarshal(obj, out); err != nil {
		return fmt.Errorf("%w: %v", ErrSchema, err)
	}
	if v, ok := out.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrSchema, err)
		}
	}
	return nil
}

// extractJSONObject returns the first balanced top-level {...} object in b, ignoring anything the
// model wrapped around it. It is brace-depth aware AND string/escape aware, so braces inside string
// literals never miscount. ok=false when no balanced object is present.
func extractJSONObject(b []byte) ([]byte, bool) {
	start := bytes.IndexByte(b, '{')
	if start < 0 {
		return nil, false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(b); i++ {
		c := b[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return b[start : i+1], true
			}
		}
	}
	return nil, false
}
