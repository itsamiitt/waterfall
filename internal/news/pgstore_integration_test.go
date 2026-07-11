//go:build integration

// Live tenant-isolation proof for the news store (gate G1) against a real PostgreSQL. Set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
//
// Assertions run as a NON-superuser role (app_rls) so RLS is actually enforced (superusers bypass it).
package news_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/news"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func principal(tenantID string) context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u"})
}

func TestNewsRLS_Isolation(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the news RLS integration test")
	}
	admin, err := pg.Connect(pg.ParseDSN(dsn))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	tryExec := func(sql string) { _ = admin.Exec(sql) }
	tryExec("drop owned by app_rls cascade")
	tryExec("drop role if exists app_rls")
	tryExec("drop table if exists news_items, market_signals cascade")
	tryExec("drop function if exists app_current_tenant() cascade")

	applyFile := func(path string) {
		ddl, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	applyFile("../../migrations/0001_init.sql")
	applyFile("../../migrations/0018_news_market.sql")

	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := admin.Exec("grant select, insert, update, delete on news_items, market_signals to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	appCfg := pg.ParseDSN(dsn)
	appCfg.User = "app_rls"
	store := news.NewStore(pg.NewPool(appCfg, 4))
	defer store.Close()

	ctxA := principal("tenant-A")
	ctxB := principal("tenant-B")

	// tenant-A writes an item + a signal.
	pub := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := store.SaveItems(ctxA, []news.NewsItem{
		{Account: "acme.com", Source: "gdelt", Title: "Acme raises Series B", URL: "https://news.example/a1", Topic: "funding", PublishedAt: pub},
	}); err != nil {
		t.Fatalf("SaveItems A: %v", err)
	}
	obs := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	if err := store.SaveSignals(ctxA, []news.MarketSignal{
		{Account: "acme.com", SignalType: "funding", Magnitude: 0.9, Detail: json.RawMessage(`{"round":"B"}`), ObservedAt: obs},
	}); err != nil {
		t.Fatalf("SaveSignals A: %v", err)
	}

	// tenant-A reads them back, including the published_at round-trip.
	itemsA, err := store.ItemsByAccount(ctxA, "acme.com")
	if err != nil || len(itemsA) != 1 || itemsA[0].Title != "Acme raises Series B" || itemsA[0].Topic != "funding" {
		t.Fatalf("ItemsByAccount A = %+v err=%v", itemsA, err)
	}
	if !itemsA[0].PublishedAt.Equal(pub) {
		t.Fatalf("published_at round-trip = %v, want %v", itemsA[0].PublishedAt, pub)
	}
	sigsA, err := store.SignalsByAccount(ctxA, "acme.com")
	if err != nil || len(sigsA) != 1 || sigsA[0].SignalType != "funding" || sigsA[0].Magnitude != 0.9 {
		t.Fatalf("SignalsByAccount A = %+v err=%v", sigsA, err)
	}

	// tenant-B sees NONE of tenant-A's rows (RLS G1).
	if itemsB, err := store.ItemsByAccount(ctxB, "acme.com"); err != nil || len(itemsB) != 0 {
		t.Fatalf("tenant-B must NOT see A's items (n=%d err=%v)", len(itemsB), err)
	}
	if sigsB, err := store.SignalsByAccount(ctxB, "acme.com"); err != nil || len(sigsB) != 0 {
		t.Fatalf("tenant-B must NOT see A's signals (n=%d err=%v)", len(sigsB), err)
	}

	// Idempotent item insert: re-saving the same (account,url) is a no-op.
	if err := store.SaveItems(ctxA, []news.NewsItem{
		{Account: "acme.com", Source: "gdelt", Title: "dup", URL: "https://news.example/a1", Topic: "funding"},
	}); err != nil {
		t.Fatalf("SaveItems A dup: %v", err)
	}
	if itemsA2, err := store.ItemsByAccount(ctxA, "acme.com"); err != nil || len(itemsA2) != 1 {
		t.Fatalf("idempotent insert must not duplicate (n=%d err=%v)", len(itemsA2), err)
	}

	// Fail-closed: no principal ⇒ error, never cross-tenant.
	if _, err := store.ItemsByAccount(context.Background(), "acme.com"); err == nil {
		t.Fatal("store must reject an unauthenticated context")
	}
}
