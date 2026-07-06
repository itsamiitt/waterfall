-- Migration 0010 — self_monitor snapshot row-set (doc 03 §2.7, doc 10 OBS-1, doc 12 OI-P7-1).
-- Single-row-per-key upserts: loop heartbeats, fold watermarks, per-instance SSE client
-- counts, and the leader aggregator's overview/queue snapshot payloads served by followers.
-- Class P: platform-only RLS, FORCE; written exclusively through internal/dash/realtime's
-- SelfMon store under PlatformTx. Applied atomically by internal/pgmigrate; no BEGIN/COMMIT.
CREATE TABLE self_monitor (
    key          text PRIMARY KEY,      -- e.g. overview_snapshot, queue_stats_sample,
                                        --      fold:usage, sse:<instance>
    component    text NOT NULL,         -- emitting loop family: overview | queue_sampler |
                                        --      aggregator | sse | evaluator | scheduler
    instance     text,                  -- emitting dashboardd instance id (NULL for
                                        --      leader-singleton rows)
    payload      jsonb,                 -- snapshot body (tiles / queue sample); aggregates only
    seq          bigint NOT NULL DEFAULT 0,  -- monotonic snapshot sequence (DB-side increment)
    sse_clients  bigint,                -- per-instance SSE client count (sse:* rows only)
    watermark_ts timestamptz,           -- fold watermark (fold:* rows only)
    updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE self_monitor ENABLE ROW LEVEL SECURITY;
ALTER TABLE self_monitor FORCE ROW LEVEL SECURITY;
CREATE POLICY self_monitor_platform_only ON self_monitor
    USING (app_current_tenant() = 'platform')
    WITH CHECK (app_current_tenant() = 'platform');
