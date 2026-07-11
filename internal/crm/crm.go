// Package crm is the (roadmap) CRM outbound subsystem (ADR-0030; docs/research-intelligence/15 §4.3). It
// is the single owner of crm_connections, crm_field_maps, and crm_push_ledger (migration 0019) — a
// control-plane module holding connection config + field maps + the idempotent push ledger. The push
// itself is a CRM connector adapter executed THROUGH the single egress-proxy (same AuthDescriptor + egress
// key-injection + SSRF allow-list + breaker as every other call); there is NO second internet route and no
// CRM token in the control-plane (ADR-0030/0010/0017). This file lands the types + the G2 idempotency key.
package crm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// Connection is one configured CRM connection. SecretRef is an ADR-0017 envelope reference (never a
// plaintext OAuth token); the token is injected only at the egress boundary.
type Connection struct {
	ConnectionID string
	Provider     string
	Status       string
	SecretRef    string
	Config       json.RawMessage
}

// FieldMap is a versioned Dossier-field → CRM-field mapping for a connection. The version participates in
// the push idempotency key, so a remap yields distinct pushes.
type FieldMap struct {
	ConnectionID string
	Version      int
	Mapping      json.RawMessage
}

// PushRecord is one row of the idempotent push ledger. IdemKey should be computed with PushKey.
type PushRecord struct {
	ConnectionID    string
	IdemKey         string
	Record          string
	FieldMapVersion int
	DossierVersion  string
	Status          string
}

// PushKey is the G2 idempotency key for a CRM push (ADR-0030): hash(tenant, connection, record,
// field_map_version, dossier_version). A retry/redelivery with the same inputs yields the same key, so the
// UNIQUE (tenant_id, idem_key) ledger constraint makes the second push a no-op. NUL-delimited so no field
// boundary can be forged by concatenation.
func PushKey(tenant, connectionID, record string, fieldMapVersion int, dossierVersion string) string {
	h := sha256.New()
	for _, part := range []string{tenant, connectionID, record, strconv.Itoa(fieldMapVersion), dossierVersion} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
