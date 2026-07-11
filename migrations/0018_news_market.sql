-- Migration 0018 — news & market monitoring (roadmap, ADR-0025/§15; docs/research-intelligence/15).
-- internal/news is the single owner of these tables. They are SCHEMA-ONLY until a news-monitoring ADR
-- (RM-OI-2) promotes the collection lane behind its own approval gate — the news-category adapters and
-- the intent-lane feed are NOT wired here. What lands now is the persistence + tenant-isolation contract,
-- so the schema is proven (G1) and the feed can attach later without a migration.
--
-- Boundary (ADR-0025): news adapters are INDEX-ONLY. news_items stores the discovered item's URL and
-- metadata (title/topic/published_at) — never the article body; body extraction stays banned. A URL from
-- a news index may only be resolved via another registered provider API, never DOM-scraped.
--
-- Class-T tenant isolation (gate G1), same mechanism as 0001/0015/0016: the app connects with a role that
-- has NO BYPASSRLS and runs, per transaction, SET LOCAL app.current_tenant = '<tenant-from-principal>';
-- every policy scopes rows to that setting (app_current_tenant() helper defined in 0001). tenant_id is
-- NEVER taken from a request body. No BEGIN/COMMIT here — internal/pgmigrate applies each file atomically.

-- ---------------------------------------------------------------------------
-- news_items — one discovered news/event index entry about an account (company_domain). URL + metadata
-- only (index-only, ADR-0025); dedup per (tenant, account, url).
-- ---------------------------------------------------------------------------
CREATE TABLE news_items (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    text        NOT NULL,
    account      text        NOT NULL,               -- company_domain the item is about
    source       text        NOT NULL,               -- news-provider slug (e.g. 'gdelt')
    title        text        NOT NULL,
    url          text        NOT NULL,               -- the item URL (index-only; body never stored)
    topic        text        NOT NULL DEFAULT '',    -- coarse categorization
    published_at timestamptz,                          -- item publication time (nullable)
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, account, url)                   -- idempotent per account+url
);
CREATE INDEX news_items_account_idx ON news_items (tenant_id, account, published_at DESC);

-- ---------------------------------------------------------------------------
-- market_signals — one observed market/financial signal about an account (funding, stock move, hiring
-- surge, …). Magnitude drives intent weighting once the lane is promoted; detail carries the raw shape.
-- ---------------------------------------------------------------------------
CREATE TABLE market_signals (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   text        NOT NULL,
    account     text        NOT NULL,
    signal_type text        NOT NULL,                 -- e.g. 'funding', 'stock_move', 'hiring_surge'
    magnitude   double precision NOT NULL DEFAULT 0,
    detail      jsonb       NOT NULL DEFAULT '{}',
    observed_at timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX market_signals_account_idx ON market_signals (tenant_id, account, observed_at DESC);

-- ---------------------------------------------------------------------------
-- FORCE RLS on both tables (applies policies even to the table owner). The app role has no BYPASSRLS, so
-- cross-tenant reads are impossible even with an application bug (G1).
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['news_items', 'market_signals'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Release-blocker (run as a NON-superuser against a real Postgres; superusers bypass RLS):
--   SET app.current_tenant='A'; INSERT ...; SET app.current_tenant='B';
--   SELECT count(*) FROM news_items;      -- MUST be 0
--   SELECT count(*) FROM market_signals;  -- MUST be 0
