-- Migration 0003 — outbox dead-letter safety (docs/39).
--
-- `attempts` counts relay DELIVERIES of a row. A job that never reaches a durable-terminal
-- state — e.g. a worker that crashes on it every time (a "poison" job) — is redelivered
-- at-least-once and would otherwise loop forever, permanently occupying a worker. After
-- `max_attempts` deliveries the relay PARKS it: dead=true, pending=false, so it stops being
-- claimed and instead surfaces in the dead-letter queue for inspection. `last_error` records
-- why it was parked. (A job that RUNS and fails is already terminal/`failed` — not a poison
-- loop; this backstops the crash-loop case that terminal status cannot catch.)
--
-- Requires migration 0002. Applied atomically by the migration runner; no BEGIN/COMMIT here.

ALTER TABLE job_outbox ADD COLUMN attempts   integer NOT NULL DEFAULT 0;
ALTER TABLE job_outbox ADD COLUMN dead       boolean NOT NULL DEFAULT false;
ALTER TABLE job_outbox ADD COLUMN last_error text;

-- Dead rows have pending=false, so the existing pending index already excludes them from the
-- claim scan. This partial index keeps dead-letter LISTING (newest first) cheap.
CREATE INDEX job_outbox_dead_idx ON job_outbox (updated_at DESC) WHERE dead;
