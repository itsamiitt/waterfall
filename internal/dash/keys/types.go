// Package keys is the dashboard's Provider Key inventory and Key Pool surface (module 3, doc 04
// §2.4): metadata CRUD over the Class-P provider_keys / key_pools / key_pool_members /
// key_import_batches tables, per-Key state-machine actions (KM-3, doc 07 §9), Key Pool
// membership + selection-strategy management, and the async bulk-import pipeline (csv / xlsx /
// json / paste).
//
// Invariants this package upholds:
//   - Key material is NEVER stored, logged, or serialized in the clear. It is sealed on the way
//     in via secrets.Backend.Seal(ctx, "provider_key", plaintext); provider_keys stores only the
//     secret_envelope_id, the display secret_last4, and never the plaintext (doc 05 §7.3). The
//     Secret wrapper type redacts String()/MarshalJSON().
//   - provider_keys, key_pools, key_pool_members, key_import_batches and secret_envelopes are
//     Class P (doc 05 §3.1): every read/write runs through db.Store.PlatformTx.
//   - Bounded queries: List is cursor-paginated under db.ClampLimit.
//   - Duplicate key material is detected by the KEYED HMAC fingerprint (secret_envelopes
//     .aad_fingerprint) WITHOUT ever decrypting an envelope.
package keys

// Key status is the KM-3 Provider Key state machine (provider_keys.status CHECK, migration 0005;
// transitions pinned in doc 07 §9).
const (
	StatusActive      = "active"
	StatusDisabled    = "disabled"
	StatusPaused      = "paused"
	StatusExhausted   = "exhausted"
	StatusRateLimited = "rate_limited"
	StatusAuthFailed  = "auth_failed"
	StatusExpired     = "expired"
	StatusRotating    = "rotating"
	StatusArchived    = "archived"
)

// validStatus is the closed status vocabulary.
var validStatus = map[string]bool{
	StatusActive: true, StatusDisabled: true, StatusPaused: true, StatusExhausted: true,
	StatusRateLimited: true, StatusAuthFailed: true, StatusExpired: true, StatusRotating: true,
	StatusArchived: true,
}

// manualTransitions is the set of operator-initiated status changes and their legal source
// states (doc 07 §9). An action from an illegal source is a 409 conflict at the HTTP layer.
// Automatic transitions (QUOTA->exhausted, AUTH->auth_failed, etc.) are driven by the P2
// rotation engine off Lease.Done and are not exposed here.
var manualTransitions = map[string]map[string]bool{
	// POST /keys/{id}/enable: paused|disabled -> active.
	"enable": {StatusPaused: true, StatusDisabled: true},
	// POST /keys/{id}/disable: park a serving/limited Key.
	"disable": {
		StatusActive: true, StatusPaused: true, StatusExhausted: true,
		StatusRateLimited: true, StatusExpired: true, StatusAuthFailed: true,
	},
	// POST /keys/{id}/rotate: open an overlap window (active|paused -> rotating).
	"rotate": {StatusActive: true, StatusPaused: true},
	// DELETE /keys/{id}: manual archive is legal from any non-archived state (doc 07 §9
	// "manual archive: any -> archived").
	"archive": {
		StatusActive: true, StatusDisabled: true, StatusPaused: true, StatusExhausted: true,
		StatusRateLimited: true, StatusAuthFailed: true, StatusExpired: true, StatusRotating: true,
	},
}

// transitionTarget returns the resulting status for a manual action and whether the current
// status is a legal source for it.
func transitionTarget(action, from string) (to string, ok bool) {
	sources, known := manualTransitions[action]
	if !known || !sources[from] {
		return "", false
	}
	switch action {
	case "enable":
		return StatusActive, true
	case "disable":
		return StatusDisabled, true
	case "rotate":
		return StatusRotating, true
	case "archive":
		return StatusArchived, true
	}
	return "", false
}

// validStrategies is the 12-strategy Key Pool selection catalog (key_pools.strategy CHECK,
// migration 0005; catalog in doc 07 §8).
var validStrategies = map[string]bool{
	"round_robin": true, "least_used": true, "weighted": true, "credit_based": true,
	"region_based": true, "lowest_latency": true, "highest_success": true, "ai_routing": true,
	"random": true, "priority": true, "failover": true, "overflow": true,
}

// Key is the typed image of a provider_keys row (metadata + display-safe fields only; runtime
// counters that the router owns are intentionally not surfaced through the metadata CRUD path).
// No field ever holds plaintext key material — SecretEnvelopeID + SecretLast4 identify the key.
type Key struct {
	ID                 string
	ProviderID         string
	Label              string
	SecretEnvelopeID   string
	SecretLast4        string
	AuthMethod         string
	Status             string
	DisableReason      string
	Health             string
	Weight             int64
	Priority           *int64
	Region             string
	Environment        string
	Team               string
	Owner              string
	Notes              string
	DailyLimit         *int64
	MonthlyLimit       *int64
	RPMLimit           *int64
	ConcurrencyLimit   *int64
	CreditsRemaining   *int64
	CreditsUsed        *int64
	ExpiresAt          string // Postgres timestamptz text ("" = NULL)
	OwnerTenantID      string
	RotationGroup      string
	ImportedBatchID    string
	Tags               []string
	RotatedTo          string
	RotateOverlapUntil string
	LastUsedAt         string
	LastRotatedAt      string
	CreatedBy          string
	CreatedAt          string
	UpdatedAt          string
}

// KeyPatch carries the mutable metadata fields of PATCH /keys/{id}. Nil fields are untouched;
// ciphertext is never in scope (there is no reveal or re-key path through PATCH).
type KeyPatch struct {
	Label            *string
	AuthMethod       *string
	Weight           *int64
	Priority         *int64
	Region           *string
	Environment      *string
	Team             *string
	Owner            *string
	Notes            *string
	DailyLimit       *int64
	MonthlyLimit     *int64
	RPMLimit         *int64
	ConcurrencyLimit *int64
	RotationGroup    *string
	Tags             *[]string
	ExpiresAt        *string
}

// KeyFilter bounds a List query (doc 04 §2.4 filters). Empty fields are no-ops.
type KeyFilter struct {
	ProviderID      string
	Status          string
	Health          string
	Region          string
	Environment     string
	Tag             string
	RotationGroup   string
	ImportedBatchID string
	PoolID          string
	Q               string // label prefix / substring search
}

// Pool is the typed image of a key_pools row.
type Pool struct {
	ID             string
	ProviderID     string
	Name           string
	Strategy       string
	StrategyParams string // jsonb text ("" = NULL)
	OwnerTenantID  string
	Status         string
	CreatedAt      string
	MemberCount    int
}

// Selector is the pool selector provider_id||':'||name matching AuthDescriptor.KeyPoolSelector.
func (p Pool) Selector() string { return p.ProviderID + ":" + p.Name }

// ImportBatch is the typed image of a key_import_batches row (async bulk-import provenance).
type ImportBatch struct {
	ID         string
	ProviderID string
	Source     string // csv | xlsx | json | paste
	Total      int
	Succeeded  int
	Failed     int
	Errors     string // jsonb text
	Status     string // running | succeeded | partial | failed
	CreatedBy  string
	CreatedAt  string
	FinishedAt string
}

// importRow is one parsed inbound record. Secret holds plaintext transiently in memory only,
// sealed immediately during processing and never persisted or logged.
type importRow struct {
	Label        string
	Secret       string
	Region       string
	Environment  string
	Pool         string
	Weight       *int64
	Priority     *int64
	DailyLimit   *int64
	MonthlyLimit *int64
	RPMLimit     *int64
}

// rowError is a per-row import failure recorded in key_import_batches.errors (doc 04 §4.4).
type rowError struct {
	Row     int    `json:"row"`
	ID      any    `json:"id"`      // target object id for bulk ops; null for imports
	Code    string `json:"code"`    // §1.6 registry code
	Message string `json:"message"` // never contains key material
}

// last4 returns the trailing 4 characters of a secret for display (secret_last4), or the whole
// string when shorter. It is the ONLY substring of the plaintext that is ever persisted.
func last4(secret string) string {
	if len(secret) <= 4 {
		return secret
	}
	return secret[len(secret)-4:]
}
