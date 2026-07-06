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
# setup). Serialize them. The dashboard suite (internal/dash/e2e) runs LAST and rebuilds only the
# migration-0004 tables it owns, so it is independent of the sibling packages' schema state.
# -short: the two full-scale P12 load fixtures (TestImportLoad50k seals 50k rows over ~15m;
# TestFold1M folds 1,000,000 usage_events) are ON-DEMAND (docs/13 §1 "Load: on demand", §6). They
# guard on testing.Short() and SKIP here so the routine RLS gate stays fast and does not destabilize
# the shared ephemeral cluster with sustained multi-minute write load. Their single-instance dev
# measurements are recorded in docs/13 §6; re-run them WITHOUT -short (e.g.
# `go test -tags integration -run 'TestImportLoad50k|TestFold1M' -timeout 30m ./internal/dash/keys/
# ./internal/dash/telemetry/`) to reproduce those numbers. The chaos-invariant tests
# (publish-crash, PG-restart, poison-row) are cheap and always run.
go test -tags integration -v -p 1 -short \
  -run 'TestConn_SimpleAndExtended|TestRolePrivileges|TestRLS_TenantIsolation|TestPG_IdempotencyLedger|TestPG_CostLedger|TestPGOutbox_DurableDeliveryAndCrashSafety|TestPGOutbox_DeadLetterAfterMaxAttempts|TestPGOutbox_RedriveReplaysParkedJob|TestApply_OrderedAndIdempotent|TestPending_ReportsUnapplied|TestApply_NoTransactionEscapeHatch|TestE2E_FullStack|TestDashRLSZeroRows|TestDashLoginMFAAndSecurity|TestDashFeatureWiring|TestDashApprovalGateWiring|TestKeysImportSealAndRLS|TestPoisonImportRowIsolation|TestProvidersLifecycleAndRLS|TestRotationLeaseNoOverLease|TestRotationEngineE2E|TestConfigLifecycleAndRLS|TestConcurrentPublishConflict|TestPublishCrashFaultPointInvariant|TestPGRestartPoolRecovers|TestHealthTimelineFoldAndNoData|TestTelemetryFoldRefoldIdentical|TestTelemetryPartitionMaintainer|TestTelemetryReconcileKeyBudgets|TestTelemetryLeaderElection|TestApprovalsExactlyOnce|TestApprovalsNegativeDecisions|TestCostGroupBysMatchLedgers|TestCostRLSIsolation|TestAlertsFireDedupeResolve|TestAlertsTestSendSSRFBlocked|TestAlertsRLSIsolation|TestQueuesReplayIdempotent|TestQueuesFilteredReplay|TestQueuesTenantIsolation|TestQueueStatsFoldAndList|TestWorkersDrainConverges|TestWorkersLostDetection|TestSSESoakLite|TestSelfMonitorRLS|TestPollerDerivesEvents|TestOverviewSnapshotThenDelta|TestOverviewAggregatorFailover|TestSessionsRevokeAllForUser' \
  ./internal/pg/ ./internal/pgstore/ ./internal/pgoutbox/ ./internal/pgmigrate/ ./internal/e2e/ ./internal/dash/e2e/ ./internal/dash/keys/ ./internal/dash/providers/ ./internal/dash/rotation/ ./internal/dash/configver/ ./internal/dash/health/ ./internal/dash/telemetry/ ./internal/dash/approvals/ ./internal/dash/cost/ ./internal/dash/alerts/ ./internal/dash/queues/ ./internal/dash/workers/ ./internal/dash/realtime/ ./internal/dash/overview/ ./internal/dash/security/
