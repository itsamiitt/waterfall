package secrets

import (
	"context"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// PGBackend is the production Backend over the Class-P secret_envelopes table. Every access
// runs through db.Store.PlatformTx (tenant='platform'), the one-owner-per-table path — no
// tenant-scoped transaction can ever read or write this table (doc 05 §3.1/§3.2).
type PGBackend struct {
	store  *db.Store
	kr     *Keyring
	pepper []byte
}

// NewPGBackend wires a PGBackend to its store, master keyring, and fingerprint pepper.
func NewPGBackend(store *db.Store, kr *Keyring, fingerprintPepper []byte) *PGBackend {
	return &PGBackend{store: store, kr: kr, pepper: fingerprintPepper}
}

// Seal encrypts plaintext and INSERTs a new secret_envelopes row, returning its id.
func (b *PGBackend) Seal(ctx context.Context, kind string, plaintext []byte) (EnvelopeID, error) {
	e, err := sealEnvelope(b.kr, b.pepper, kind, plaintext, "")
	if err != nil {
		return "", err
	}
	if err := b.store.PlatformTx(ctx, func(c *pg.Conn) error { return insertEnvelope(c, e) }); err != nil {
		return "", err
	}
	return e.id, nil
}

// Open loads the envelope and decrypts it.
func (b *PGBackend) Open(ctx context.Context, id EnvelopeID) ([]byte, error) {
	e, ok, err := b.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return openEnvelope(b.kr, e)
}

// Rotate re-seals the plaintext under a new envelope with rotated_from lineage, then INSERTs
// it. The predecessor row is left intact for the retirement step of the rotation runbook.
func (b *PGBackend) Rotate(ctx context.Context, id EnvelopeID) (EnvelopeID, error) {
	e, ok, err := b.load(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	pt, err := openEnvelope(b.kr, e)
	if err != nil {
		return "", err
	}
	ne, err := sealEnvelope(b.kr, b.pepper, e.kind, pt, string(id))
	if err != nil {
		return "", err
	}
	if err := b.store.PlatformTx(ctx, func(c *pg.Conn) error { return insertEnvelope(c, ne) }); err != nil {
		return "", err
	}
	return ne.id, nil
}

// RefingerprintOnRotate recomputes envelope id's provider_key duplicate-detection fingerprint
// under the backend's CURRENT pepper (from plaintext already in hand at a key rotate/import) and
// UPDATEs it in place — the OI-SEC-4 pepper-rotation seam (see Refingerprinter and the procedure in
// backend.go). It is deliberately targeted, never a bulk re-hash: only the key being rotated or
// imported is touched, so a pepper change never requires decrypting the whole store. Envelopes not
// rotated keep their prior-pepper fingerprints; cross-pepper duplicate detection therefore degrades
// gracefully rather than breaking. No-op for non-provider_key kinds; ErrNotFound for an unknown id.
func (b *PGBackend) RefingerprintOnRotate(ctx context.Context, id EnvelopeID, plaintext []byte) error {
	e, ok, err := b.load(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if e.kind != "provider_key" {
		return nil
	}
	fp := fingerprintOf(b.pepper, plaintext)
	return b.store.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`update secret_envelopes set aad_fingerprint = $2::bytea where id = $1`,
			string(id), encodeBytea(fp))
	})
}

// load reads the columns needed to decrypt an envelope. The fingerprint is not read back
// (it is a write-side duplicate-detection index, never needed on the Open path).
func (b *PGBackend) load(ctx context.Context, id EnvelopeID) (envelope, bool, error) {
	e := envelope{id: id}
	found := false
	err := b.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select kind, master_key_id, dek_wrapped, nonce, ciphertext
			 from secret_envelopes where id = $1`, string(id))
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		e.kind = str(row[0])
		e.masterKeyID = str(row[1])
		if e.dekWrapped, err = decodeBytea(str(row[2])); err != nil {
			return err
		}
		if e.nonce, err = decodeBytea(str(row[3])); err != nil {
			return err
		}
		if e.ciphertext, err = decodeBytea(str(row[4])); err != nil {
			return err
		}
		found = true
		return nil
	})
	return e, found, err
}

// insertEnvelope writes one secret_envelopes row. bytea columns are sent in \x hex text form;
// aad_fingerprint and rotated_from are NULL when absent.
func insertEnvelope(c *pg.Conn, e envelope) error {
	var fp any
	if e.fingerprint != nil {
		fp = encodeBytea(e.fingerprint)
	}
	var rf any
	if e.rotatedFrom != "" {
		rf = e.rotatedFrom
	}
	return c.ExecParams(
		`insert into secret_envelopes
		   (id, kind, master_key_id, dek_wrapped, nonce, ciphertext, aad_fingerprint, rotated_from)
		 values ($1, $2, $3, $4::bytea, $5::bytea, $6::bytea, $7::bytea, $8)`,
		string(e.id), e.kind, e.masterKeyID,
		encodeBytea(e.dekWrapped), encodeBytea(e.nonce), encodeBytea(e.ciphertext),
		fp, rf)
}

// str dereferences a nullable text column to "" on NULL.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var _ Backend = (*PGBackend)(nil)
