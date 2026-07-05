-- Migration 0007 — alerting and approvals (doc 03 §2.4, docs 05/10; resolves RF-5).
--
-- alert_events is the edge-triggered episode row (firing -> resolved); the partial unique
-- index (tenant_id, rule_id) WHERE state='firing' makes "one open episode per rule" a
-- database invariant (evaluator inserts use ON CONFLICT DO NOTHING). alert_events is NOT
-- partitioned: a global partial unique index cannot coexist with declarative partitioning,
-- and the invariant wins (OI-DB-2); retention is a batched DELETE at 180d (doc 03 §4).
--
-- alert_notifications is the delivery outbox (RF-5): a notification row is inserted in the
-- SAME transaction as the episode transition, then delivered by the notifier loop with
-- FOR UPDATE SKIP LOCKED claims — transactional intent, at-least-once delivery, dedupe via
-- the partial unique index on (dedupe_key) WHERE status='pending'.
--
-- Approvals: payload is fully resolved and pinned at request time; quorum is counted under
-- SELECT ... FOR UPDATE on the request row; execution is exactly-once with Idempotency Key
-- = request id (P4 gate). Applied atomically by internal/pgmigrate; no BEGIN/COMMIT here.

CREATE TABLE alert_channels (
    id                 uuid PRIMARY KEY,
    tenant_id          text NOT NULL,
    kind               text NOT NULL CHECK (kind IN
                       ('email', 'slack', 'teams', 'discord', 'webhook')),
    name               text NOT NULL,
    config_envelope_id uuid NOT NULL REFERENCES secret_envelopes(id),  -- URLs + secrets encrypted
    status             text NOT NULL DEFAULT 'active',
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_rules (
    id          uuid PRIMARY KEY,
    tenant_id   text NOT NULL,
    name        text NOT NULL,
    metric      text NOT NULL,         -- CLOSED vocabulary (doc 10); never free-form
    scope       jsonb,
    op          text NOT NULL CHECK (op IN ('gt', 'lt', 'gte', 'lte')),
    threshold   double precision NOT NULL,
    window_s    int NOT NULL,
    cooldown_s  int NOT NULL,
    severity    text,
    channels    uuid[],                -- alert_channels ids; validated in the service layer
    enabled     boolean NOT NULL DEFAULT true,
    muted_until timestamptz,           -- NULL = not muted; snoozes are audited PATCH writes
                                       -- (doc 04 §2.11); the evaluator skips rules with
                                       -- muted_until > now() (doc 10 §5.1)
    created_by  uuid,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   text NOT NULL,
    rule_id     uuid NOT NULL REFERENCES alert_rules(id),
    state       text NOT NULL CHECK (state IN ('firing', 'resolved')),
    value       double precision,
    fired_at    timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    notified_at timestamptz,
    ack_by      uuid,
    ack_at      timestamptz,
    dedupe_key  text NOT NULL           -- sha256(tenant_id || rule_id || canonical scope-instance)
);
-- MASTER SPEC §10b: at most one open episode per rule — a database invariant, not
-- evaluator memory. Evaluator INSERT ... ON CONFLICT DO NOTHING targets this index.
CREATE UNIQUE INDEX alert_events_one_firing_uq ON alert_events (tenant_id, rule_id)
    WHERE state = 'firing';
CREATE INDEX alert_events_tenant_fired_idx ON alert_events (tenant_id, fired_at DESC);

-- alert_notifications — delivery outbox (RF-5), owned by internal/dash/alerts.
-- dedupe_key here is NOTIFICATION-grained: hex(sha256(event_dedupe_key || ':' || channel_id
-- || ':' || occasion)) where occasion is 'fired', 'renotify:<cooldown-bucket>', or
-- 'resolved' — so one episode fans out to N channels without colliding, while retries of
-- the same send occasion dedupe.
CREATE TABLE alert_notifications (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     text NOT NULL,
    event_id      bigint NOT NULL REFERENCES alert_events(id),
    channel_id    uuid   NOT NULL REFERENCES alert_channels(id),
    dedupe_key    text   NOT NULL,
    status        text   NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts      int    NOT NULL DEFAULT 0,
    next_retry_at timestamptz,
    sent_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alert_notifications_pending_dedupe_uq ON alert_notifications (dedupe_key)
    WHERE status = 'pending';
CREATE INDEX alert_notifications_claim_idx ON alert_notifications (next_retry_at)
    WHERE status = 'pending';
CREATE INDEX alert_notifications_event_idx ON alert_notifications (tenant_id, event_id);

CREATE TABLE approval_policies (
    tenant_id          text NOT NULL,
    action_kind        text NOT NULL CHECK (action_kind IN
                       ('key_bulk_delete', 'provider_delete', 'provider_archive',
                        'routing_publish', 'workflow_publish', 'secrets_backend_change')),
    required_approvals int  NOT NULL DEFAULT 1,
    approver_role      text NOT NULL DEFAULT 'tenant_admin',
    expires_after_s    int  NOT NULL DEFAULT 86400,
    PRIMARY KEY (tenant_id, action_kind)
);

CREATE TABLE approval_requests (
    id                 uuid PRIMARY KEY,
    tenant_id          text NOT NULL,
    action_kind        text NOT NULL,
    payload            jsonb NOT NULL,   -- fully resolved at request time (payload pinning)
    requested_by       uuid NOT NULL,
    status             text NOT NULL DEFAULT 'pending' CHECK (status IN
                       ('pending', 'approved', 'rejected', 'expired', 'cancelled',
                        'executed', 'failed')),
    required_approvals int NOT NULL DEFAULT 1,
    expires_at         timestamptz NOT NULL,
    executed_at        timestamptz,
    execution_result   jsonb,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_requests_pending_idx ON approval_requests (expires_at)
    WHERE status = 'pending';

-- approval_decisions — PRIMARY KEY (request_id, approver_user_id) makes distinct-approver
-- quorum a DB constraint; requester != approver (four-eyes) is enforced in the service.
-- tenant_id present because this is a Class T table (ADR-0020).
CREATE TABLE approval_decisions (
    request_id       uuid NOT NULL REFERENCES approval_requests(id),
    tenant_id        text NOT NULL,
    approver_user_id uuid NOT NULL,
    decision         text NOT NULL CHECK (decision IN ('approve', 'reject')),
    comment          text,
    mfa_verified     boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, approver_user_id)
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['alert_channels', 'alert_rules', 'alert_events',
                             'alert_notifications', 'approval_policies', 'approval_requests',
                             'approval_decisions'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Operator cross-tenant READ (enumerated, ADR-0020).
CREATE POLICY alert_events_operator_read ON alert_events
    FOR SELECT USING (app_current_role() = 'operator');
