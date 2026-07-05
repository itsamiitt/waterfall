package approvals

import (
	"context"
	"encoding/json"
	"testing"
)

// TestDefaultPolicy_FailClosedGatesAllActionKinds proves the built-in default (doc 05 §9.1) gates
// every action_kind with required_approvals >= 1 even with no approval_policies row, and picks the
// operator approver_role for the three platform action_kinds, tenant_admin otherwise.
func TestDefaultPolicy_FailClosedGatesAllActionKinds(t *testing.T) {
	wantRole := map[string]string{
		ActionKeyBulkDelete:        "tenant_admin",
		ActionRoutingPublish:       "tenant_admin",
		ActionWorkflowPublish:      "tenant_admin",
		ActionProviderDelete:       "operator",
		ActionProviderArchive:      "operator",
		ActionSecretsBackendChange: "operator",
	}
	if len(AllActionKinds) != 6 {
		t.Fatalf("expected the closed catalog to have 6 action_kinds, got %d", len(AllActionKinds))
	}
	for _, ak := range AllActionKinds {
		p := defaultPolicy(ak)
		if p.RequiredApprovals < 1 {
			t.Errorf("%s: default required_approvals = %d, must be >= 1 (gate never disarms)", ak, p.RequiredApprovals)
		}
		if p.ExpiresAfterS != 86400 {
			t.Errorf("%s: default expires_after_s = %d, want 86400", ak, p.ExpiresAfterS)
		}
		if p.ApproverRole != wantRole[ak] {
			t.Errorf("%s: default approver_role = %q, want %q", ak, p.ApproverRole, wantRole[ak])
		}
		if !p.Default {
			t.Errorf("%s: defaultPolicy must be flagged Default", ak)
		}
		if !isKnownActionKind(ak) {
			t.Errorf("%s: must be a known action_kind", ak)
		}
	}
	if isKnownActionKind("not_a_real_action") {
		t.Error("unknown action_kind must not be recognized")
	}
}

// TestNopGate_ProceedsInline proves the disabled-gating Gate always proceeds and creates no request.
func TestNopGate_ProceedsInline(t *testing.T) {
	proceed, id, err := NopGate{}.Check(context.Background(), ActionProviderDelete, json.RawMessage(`{}`))
	if err != nil || !proceed || id != "" {
		t.Fatalf("NopGate.Check = (%v,%q,%v), want (true,\"\",nil)", proceed, id, err)
	}
}

// TestRequestIDContext round-trips the request id an Executor reads as its Idempotency-Key.
func TestRequestIDContext(t *testing.T) {
	if _, ok := RequestIDFromContext(context.Background()); ok {
		t.Fatal("empty context must not carry a request id")
	}
	ctx := WithRequestID(context.Background(), "req-123")
	got, ok := RequestIDFromContext(ctx)
	if !ok || got != "req-123" {
		t.Fatalf("RequestIDFromContext = (%q,%v), want (req-123,true)", got, ok)
	}
}

// TestExecutorRegistry proves RegisterExecutor binds/looks up by action_kind and defaults to nil.
func TestExecutorRegistry(t *testing.T) {
	s := NewService(Config{})
	if s.executorFor(ActionKeyBulkDelete) != nil {
		t.Fatal("unregistered action_kind must resolve to a nil Executor")
	}
	called := false
	s.RegisterExecutor(ActionKeyBulkDelete, func(context.Context, json.RawMessage) error {
		called = true
		return nil
	})
	fn := s.executorFor(ActionKeyBulkDelete)
	if fn == nil {
		t.Fatal("registered Executor must be resolvable")
	}
	_ = fn(context.Background(), nil)
	if !called {
		t.Fatal("resolved Executor was not the one registered")
	}
}

// TestServiceImplementsGate is a compile-time-ish assertion that *Service satisfies Gate.
func TestServiceImplementsGate(t *testing.T) {
	var _ Gate = NewService(Config{})
}
