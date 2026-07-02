#!/usr/bin/env bash
# Runs the live Postgres integration tests, including the gate-G1 tenant-isolation (RLS)
# release-blocker (docs/21 §1, docs/32).
#
# Usage:
#   WATERFALL_PG_DSN="host=... port=... user=... dbname=..." scripts/run-rls-test.sh
#     -> run against an existing Postgres (CI service container).
#   scripts/run-rls-test.sh
#     -> spin up an EPHEMERAL trust cluster (needs initdb/pg_ctl on PATH or $PGBIN),
#        run the tests, then tear it down.
#
# The test connects as superuser to build the schema, then runs every isolation assertion
# as a NON-superuser role (superusers bypass RLS).
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${WATERFALL_PG_DSN:-}" ]; then
  PGBIN="${PGBIN:-}"
  if [ -n "$PGBIN" ]; then export PATH="$PGBIN:$PATH"; fi
  command -v initdb >/dev/null || { echo "initdb not found; set PGBIN to the Postgres bin dir"; exit 1; }

  WORK="$(mktemp -d)"
  PGDATA="$WORK/data"
  PORT="${PGPORT:-55432}"
  initdb -D "$PGDATA" -U postgres -A trust --encoding=UTF8 >/dev/null
  pg_ctl -D "$PGDATA" -o "-p $PORT" -l "$WORK/pg.log" start
  trap 'pg_ctl -D "$PGDATA" stop -m immediate >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT
  export WATERFALL_PG_DSN="host=127.0.0.1 port=$PORT user=postgres dbname=postgres"
  echo "ephemeral cluster: $WATERFALL_PG_DSN"
fi

# Note: the SCRAM (TestConn_SCRAM) and TLS (TestConn_TLS) tests are skipped unless
# WATERFALL_PG_SCRAM_DSN / WATERFALL_PG_TLS_DSN are set against a cluster configured with a
# scram role + ssl=on (an ephemeral trust cluster does not exercise them).
#
# -p 1 is REQUIRED: every package's tests drop/recreate the SAME schema in ONE shared database,
# so running package test binaries in parallel would race (e.g. pgmigrate's drop vs pgoutbox's
# setup). Serialize them.
go test -tags integration -v -p 1 \
  -run 'TestConn_SimpleAndExtended|TestRolePrivileges|TestRLS_TenantIsolation|TestPG_IdempotencyLedger|TestPG_CostLedger|TestPGOutbox_DurableDeliveryAndCrashSafety|TestPGOutbox_DeadLetterAfterMaxAttempts|TestPGOutbox_RedriveReplaysParkedJob|TestApply_OrderedAndIdempotent|TestPending_ReportsUnapplied|TestE2E_FullStack' \
  ./internal/pg/ ./internal/pgstore/ ./internal/pgoutbox/ ./internal/pgmigrate/ ./internal/e2e/
