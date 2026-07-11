package research

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/tenant"
)

type stubAssembler struct {
	dossier Dossier
	err     error
}

func (f stubAssembler) Assemble(ctx context.Context, s Subject) (Dossier, error) {
	return f.dossier, f.err
}

type fakeRunStore struct {
	mu       sync.Mutex
	statuses map[string][]string
	saved    map[string]Dossier
	saveErr  error
	terminal chan string
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{statuses: map[string][]string{}, saved: map[string]Dossier{}, terminal: make(chan string, 8)}
}

func (f *fakeRunStore) SetRunStatus(ctx context.Context, runID, status string) error {
	f.mu.Lock()
	f.statuses[runID] = append(f.statuses[runID], status)
	f.mu.Unlock()
	if status == RunDone || status == RunFailed {
		select {
		case f.terminal <- runID:
		default:
		}
	}
	return nil
}

func (f *fakeRunStore) SaveDossier(ctx context.Context, dossierID, subjectKey string, d Dossier) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	f.saved[dossierID] = d
	f.mu.Unlock()
	return nil
}

func (f *fakeRunStore) transitions(runID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statuses[runID]...)
}

func TestRunner_Process_Done(t *testing.T) {
	store := newFakeRunStore()
	r := NewRunner(stubAssembler{dossier: Dossier{CompanyProfile: map[string]string{"name": "Acme"}}}, store, 4, nil)
	r.process(runTask{principal: tenant.Principal{TenantID: "t1"}, runID: "run-1", subject: Subject{Domain: "acme.com"}})

	if got := store.transitions("run-1"); len(got) != 2 || got[0] != RunRunning || got[1] != RunDone {
		t.Fatalf("transitions = %v, want [running done]", got)
	}
	if _, ok := store.saved[subjectID(Subject{Domain: "acme.com"})]; !ok {
		t.Fatal("dossier must be persisted on success")
	}
}

func TestRunner_Process_AssemblyFailure(t *testing.T) {
	store := newFakeRunStore()
	r := NewRunner(stubAssembler{err: errors.New("boom")}, store, 4, nil)
	r.process(runTask{principal: tenant.Principal{TenantID: "t1"}, runID: "run-2", subject: Subject{Domain: "x.com"}})

	if got := store.transitions("run-2"); len(got) != 2 || got[0] != RunRunning || got[1] != RunFailed {
		t.Fatalf("transitions = %v, want [running failed]", got)
	}
	if len(store.saved) != 0 {
		t.Fatal("no dossier should be persisted on assembly failure")
	}
}

func TestRunner_Process_PersistFailure(t *testing.T) {
	store := newFakeRunStore()
	store.saveErr = errors.New("db down")
	r := NewRunner(stubAssembler{dossier: Dossier{}}, store, 4, nil)
	r.process(runTask{principal: tenant.Principal{TenantID: "t1"}, runID: "run-5", subject: Subject{Domain: "y.com"}})

	if got := store.transitions("run-5"); len(got) != 2 || got[1] != RunFailed {
		t.Fatalf("transitions = %v, want [running failed] on persist error", got)
	}
}

func TestRunner_Submit_EndToEnd(t *testing.T) {
	store := newFakeRunStore()
	r := NewRunner(stubAssembler{dossier: Dossier{}}, store, 4, nil)
	r.Start(2)
	defer r.Stop()

	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "t1"})
	if !r.Submit(ctx, "run-3", Subject{Domain: "acme.com"}) {
		t.Fatal("Submit should enqueue a run with a principal")
	}
	select {
	case <-store.terminal:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not reach a terminal state")
	}
	if got := store.transitions("run-3"); len(got) == 0 || got[len(got)-1] != RunDone {
		t.Fatalf("final status = %v, want done", got)
	}

	// Submit without a principal is refused (fail-closed).
	if r.Submit(context.Background(), "run-4", Subject{Domain: "x"}) {
		t.Fatal("Submit without a principal must be refused")
	}
}
