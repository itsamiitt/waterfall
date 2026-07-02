package pg

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// scram implements the client side of SCRAM-SHA-256 (RFC 5802 / RFC 7677) without channel
// binding — enough to authenticate against a password-auth PostgreSQL (the default
// `password_encryption = scram-sha-256`). Stdlib-only (crypto/pbkdf2 lands in Go 1.24).
type scram struct {
	password    string
	clientNonce string
	firstBare   string // "n=,r=<clientNonce>" — must match byte-for-byte in the AuthMessage
	serverSig   []byte // expected ServerSignature, checked against the server-final verifier
}

func newSCRAM(user, password string) (*scram, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return newSCRAMWithNonce(user, password, base64.RawStdEncoding.EncodeToString(b[:])), nil
}

func newSCRAMWithNonce(user, password, nonce string) *scram {
	return &scram{password: password, clientNonce: nonce, firstBare: "n=" + saslName(user) + ",r=" + nonce}
}

// saslName escapes ',' and '=' in a SCRAM username (RFC 5802 §5.1). '=' must be escaped
// first so the replacements do not compound.
func saslName(s string) string {
	s = strings.ReplaceAll(s, "=", "=3D")
	s = strings.ReplaceAll(s, ",", "=2C")
	return s
}

// clientFirst is the SCRAM client-first-message (GS2 header "n,," + the bare message).
func (s *scram) clientFirst() []byte {
	return []byte("n,," + s.firstBare)
}

// clientFinal parses the server-first-message and produces the client-final-message,
// stashing the expected ServerSignature for verify().
func (s *scram) clientFinal(serverFirst []byte) ([]byte, error) {
	attrs := parseSCRAMAttrs(string(serverFirst))
	r, salt64, iterStr := attrs["r"], attrs["s"], attrs["i"]
	if r == "" || salt64 == "" || iterStr == "" {
		return nil, errors.New("pg: malformed SCRAM server-first-message")
	}
	if !strings.HasPrefix(r, s.clientNonce) {
		return nil, errors.New("pg: SCRAM server nonce does not extend the client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(salt64)
	if err != nil {
		return nil, err
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter <= 0 {
		return nil, errors.New("pg: bad SCRAM iteration count")
	}

	saltedPassword, err := pbkdf2.Key(sha256.New, s.password, salt, iter, sha256.Size)
	if err != nil {
		return nil, err
	}
	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)

	finalNoProof := "c=biws,r=" + r // biws = base64("n,,")
	authMessage := s.firstBare + "," + string(serverFirst) + "," + finalNoProof

	clientSig := hmacSHA256(storedKey[:], []byte(authMessage))
	proof := xorBytes(clientKey, clientSig)

	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))
	s.serverSig = hmacSHA256(serverKey, []byte(authMessage))

	return []byte(finalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof)), nil
}

// verify checks the server-final-message's verifier against the expected ServerSignature —
// mutual authentication (the server proves it knows the stored key).
func (s *scram) verify(serverFinal []byte) error {
	attrs := parseSCRAMAttrs(string(serverFinal))
	v := attrs["v"]
	if v == "" {
		return errors.New("pg: SCRAM server-final-message missing verifier")
	}
	got, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return err
	}
	if !hmac.Equal(got, s.serverSig) {
		return errors.New("pg: SCRAM server signature mismatch")
	}
	return nil
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// parseSCRAMAttrs splits "k=v,k=v,..." into a map, keeping everything after the first '='
// (base64 values contain '=').
func parseSCRAMAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if i := strings.IndexByte(part, '='); i > 0 {
			out[part[:i]] = part[i+1:]
		}
	}
	return out
}
