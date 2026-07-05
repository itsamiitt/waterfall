package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
)

// RecoveryCodeCount is the number of single-use recovery codes issued at MFA confirmation
// (doc 05 §5.2). Each is 10 base32 characters (~50 bits of entropy).
const RecoveryCodeCount = 10

const recoveryCodeLen = 10

// GenerateRecoveryCodes returns RecoveryCodeCount plaintext codes and their sha256 hashes. The
// plaintext is shown to the user exactly once; only the hashes are persisted (doc 05 §5.2).
func GenerateRecoveryCodes() (plain []string, hashes [][]byte, err error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	plain = make([]string, RecoveryCodeCount)
	hashes = make([][]byte, RecoveryCodeCount)
	for i := 0; i < RecoveryCodeCount; i++ {
		raw := make([]byte, 8) // 8 bytes -> >= 10 base32 chars
		if _, err = rand.Read(raw); err != nil {
			return nil, nil, err
		}
		code := enc.EncodeToString(raw)[:recoveryCodeLen]
		plain[i] = code
		hashes[i] = HashRecoveryCode(code)
	}
	return plain, hashes, nil
}

// HashRecoveryCode returns sha256(code); recovery codes are stored only as this hash.
func HashRecoveryCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}
