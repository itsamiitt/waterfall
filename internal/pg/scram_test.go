package pg

import (
	"strings"
	"testing"
)

// TestSCRAM_RFC7677Vector validates the SCRAM-SHA-256 client computation against the worked
// example in RFC 7677 §3 (user "user", password "pencil"), with no server involved.
func TestSCRAM_RFC7677Vector(t *testing.T) {
	sc := newSCRAMWithNonce("user", "pencil", "rOprNGfwEbeRWgbNEkqO")

	if got := string(sc.clientFirst()); got != "n,,n=user,r=rOprNGfwEbeRWgbNEkqO" {
		t.Fatalf("client-first mismatch: %q", got)
	}

	serverFirst := "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	final, err := sc.clientFinal([]byte(serverFirst))
	if err != nil {
		t.Fatalf("clientFinal: %v", err)
	}
	const wantProof = "p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	if !strings.HasSuffix(string(final), wantProof) {
		t.Fatalf("client proof mismatch:\n got %q\nwant suffix %q", final, wantProof)
	}

	// Mutual auth: the server's verifier must be accepted.
	if err := sc.verify([]byte("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")); err != nil {
		t.Fatalf("server signature should verify: %v", err)
	}
	// A tampered verifier must be rejected.
	if err := sc.verify([]byte("v=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")); err == nil {
		t.Fatal("a wrong server verifier must be rejected")
	}
}

func TestSCRAM_NonceMismatchRejected(t *testing.T) {
	sc := newSCRAMWithNonce("user", "pencil", "clientNonceXYZ")
	// server nonce that does not extend the client nonce
	if _, err := sc.clientFinal([]byte("r=totallyDifferent,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")); err == nil {
		t.Fatal("a server nonce not extending the client nonce must be rejected")
	}
}
