// Package db is the dashboard's shared persistence seam: the dual-GUC RLS transaction
// helper and the opaque cursor codec that every other internal/dash package builds on.
//
// Gates enforced:
//   - G1 tenant isolation. Tx binds BOTH app.current_tenant and app.current_role per
//     transaction (ADR-0020 dual-GUC model, doc 05 §1.3) from the verified Principal
//     (tenant.FromContext) — never from a request body — using set_config(..., true) so the
//     binding is transaction-local and a pooled connection can never leak a prior Tenant or
//     role (doc 02 §3 "Pool safety"). No Principal in context => fail closed, no access.
//   - Bounded queries. EncodeCursor/DecodeCursor produce opaque base64url keyset cursors and
//     ClampLimit enforces the default 50 / hard cap 200 window (doc 04 §1.4).
//
// PlatformTx is the privileged system path for Class-P tables (secret_envelopes and, later,
// the aggregator). It binds tenant='platform', role='operator' regardless of the ctx
// Principal; it is server-internal only, and Go package visibility — nothing outside the
// server imports internal/dash — is the guard.
package db

import (
	"context"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Store wraps a connection pool and hands out dual-GUC transactions. It is the single seam
// through which dashboard features reach Postgres under FORCE ROW LEVEL SECURITY.
type Store struct {
	pool *pg.Pool
}

// New wraps a connection pool.
func New(pool *pg.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for callers that need connectivity probes (e.g. /readyz).
func (s *Store) Pool() *pg.Pool { return s.pool }

// RoleFromPrincipal returns the RBAC role carried in a scope of the form "role:<r>", where
// <r> is one of operator | tenant_admin | tenant_user (doc 05 §1.1). It returns "" when no
// recognized role scope is present — the caller then binds an empty app.current_role, which
// matches no policy (fail closed).
func RoleFromPrincipal(p tenant.Principal) string {
	for _, sc := range p.Scopes {
		switch sc {
		case "role:operator":
			return "operator"
		case "role:tenant_admin":
			return "tenant_admin"
		case "role:tenant_user":
			return "tenant_user"
		}
	}
	return ""
}

// Tx runs fn inside a transaction with app.current_tenant and app.current_role bound from the
// ctx Principal (G1, doc 05 §1.3). Both GUCs are set LOCAL (transaction-scoped). The
// connection is returned to the pool afterward, or discarded if the transaction left it in a
// bad state. Fail-closed: a context without a Principal returns tenant.ErrNoPrincipal and
// never opens a transaction.
func (s *Store) Tx(ctx context.Context, fn func(*pg.Conn) error) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err // fail-closed: no principal => no access
	}
	return s.bind(ctx, p.TenantID, RoleFromPrincipal(p), fn)
}

// PlatformTx runs fn as the platform system Principal (tenant='platform', role='operator'),
// bypassing the ctx Principal. It is the ONLY path permitted to touch Class-P tables such as
// secret_envelopes (doc 05 §3.1). Server-internal use only — Go visibility is the guard, so
// no request handler can reach it directly.
func (s *Store) PlatformTx(ctx context.Context, fn func(*pg.Conn) error) error {
	return s.bind(ctx, "platform", "operator", fn)
}

// bind mirrors internal/pgstore's tx helper precisely, adding the second GUC. A connection is
// marked broken only when begin or commit fails (leaving it in an unknown protocol state);
// set_config and fn failures roll back but keep the connection usable.
func (s *Store) bind(ctx context.Context, tenantID, role string, fn func(*pg.Conn) error) error {
	c, err := s.pool.Get(ctx)
	if err != nil {
		return err
	}
	broken := false
	defer func() { s.pool.Put(c, broken) }()

	if err := c.Exec("begin"); err != nil {
		broken = true
		return err
	}
	if err := c.ExecParams(
		"select set_config('app.current_tenant', $1, true), set_config('app.current_role', $2, true)",
		tenantID, role); err != nil {
		_ = c.Exec("rollback")
		return err
	}
	if ferr := fn(c); ferr != nil {
		_ = c.Exec("rollback") // op failed; roll back but the connection is still usable
		return ferr
	}
	if err := c.Exec("commit"); err != nil {
		broken = true
		return err
	}
	return nil
}
