package tenant

import (
	"context"
	"testing"
)

func TestFromContext_failsClosedWithoutPrincipal(t *testing.T) {
	// A bare context has no principal: tenant-scoped work must be refused, never
	// silently treated as "all tenants" (G1 fail-closed).
	if _, err := FromContext(context.Background()); err != ErrNoPrincipal {
		t.Fatalf("want ErrNoPrincipal, got %v", err)
	}
	if _, err := TenantID(context.Background()); err != ErrNoPrincipal {
		t.Fatalf("TenantID want ErrNoPrincipal, got %v", err)
	}
}

func TestWithPrincipal_roundTrip(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{TenantID: "t-123", UserID: "u-1"})
	got, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.TenantID != "t-123" || got.UserID != "u-1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestFromContext_emptyTenantIsNoPrincipal(t *testing.T) {
	// A principal with an empty tenant id is not a valid scope.
	ctx := WithPrincipal(context.Background(), Principal{TenantID: ""})
	if _, err := FromContext(ctx); err != ErrNoPrincipal {
		t.Fatalf("empty tenant should be ErrNoPrincipal, got %v", err)
	}
}
