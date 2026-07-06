package secrets

// Secret wraps secret plaintext so it cannot leak through logs, panics, or accidental JSON
// serialization: String() and MarshalJSON() both return "[REDACTED]" (doc 05 §7.3). Call
// Bytes() only at the point of use (seal, egress injection) and zero the result when done.
type Secret struct {
	b []byte
}

// NewSecret wraps b. It does not copy — the caller retains ownership of the backing array.
func NewSecret(b []byte) Secret { return Secret{b: b} }

// Bytes returns the underlying plaintext. This is the single deliberate exit point; do not
// pass the result anywhere it might be logged or serialized.
func (s Secret) Bytes() []byte { return s.b }

// String redacts. Value receiver so both Secret and *Secret are safe under fmt.
func (Secret) String() string { return "[REDACTED]" }

// GoString redacts under the %#v verb as well.
func (Secret) GoString() string { return "[REDACTED]" }

// MarshalJSON redacts, so a Secret embedded in a response or audit snapshot never serializes
// its plaintext.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
