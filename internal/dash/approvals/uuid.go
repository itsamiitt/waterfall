package approvals

import (
	"crypto/rand"
	"fmt"
)

// newUUID mints an RFC 4122 v4 uuid from crypto/rand (stdlib only), matching the shape the
// approval_requests uuid PK expects.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
