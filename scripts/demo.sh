#!/usr/bin/env bash
# demo.sh — one-command tour of the Waterfall Enrichment Engine. Runs entirely offline (in-memory
# providers + store); if a local PostgreSQL 17 is detected it also runs the live database harnesses.
#
#   bash scripts/demo.sh
#
# Phases: build -> unit tests -> offline engine demo -> live HTTP round-trip -> (optional) Postgres.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# Locate the Go toolchain (PATH first, then this box's SDK location).
if command -v go >/dev/null 2>&1; then GO=go
elif [ -x "/c/Users/Administrator/go-sdk/go/bin/go.exe" ]; then GO="/c/Users/Administrator/go-sdk/go/bin/go.exe"
else echo "go toolchain not found on PATH"; exit 1; fi
export GOTOOLCHAIN=local CGO_ENABLED=0

APPPORT="${DEMO_PORT:-8088}"
BIN="$(mktemp -d)/enrichapi.exe"
APP_PID=""
banner() { echo; echo "════════════════════════════════════════════════════════"; echo "  $*"; echo "════════════════════════════════════════════════════════"; }
cleanup() { [ -n "$APP_PID" ] && kill -9 "$APP_PID" 2>/dev/null; rm -rf "$(dirname "$BIN")"; }
trap cleanup EXIT

banner "1/5  Build (stdlib only — no external modules)"
"$GO" build ./... && echo "build ok"
"$GO" build -o "$BIN" ./cmd/enrichapi && echo "enrichapi built"

banner "2/5  Unit suite (offline, no external services)"
"$GO" test ./... 2>&1 | tail -25

banner "3/5  Offline engine demo — one record, two mock providers, full provenance"
"$GO" run ./cmd/enrichd

banner "4/5  Live HTTP round-trip against the gateway (memory mode)"
PORT="$APPPORT" "$BIN" >/dev/null 2>&1 &
APP_PID=$!
ready=0
for _ in $(seq 1 40); do
  if [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$APPPORT/healthz")" = "200" ]; then ready=1; break; fi
  sleep 0.25
done
if [ "$ready" != "1" ]; then echo "gateway did not start"; exit 1; fi
echo "POST /v1/enrichments?mode=sync  (Bearer acme-token -> tenant-acme):"
curl -s -X POST "http://127.0.0.1:$APPPORT/v1/enrichments?mode=sync" \
  -H 'Authorization: Bearer acme-token' -H 'Idempotency-Key: demo-1' -H 'Content-Type: application/json' \
  -d '{"subject":{"id":"p1","known":{"company_domain":"acme.com","first_name":"jane","last_name":"doe"}},"want":["work_email"],"confidence_target":0.7,"cost_ceiling":100,"config_version":"v1"}'
echo
echo "GET /metrics (engine + RED signal lines):"
curl -s "http://127.0.0.1:$APPPORT/metrics" | grep -E "provider_calls_total|enrichment_fields_filled_total|http_requests_total" | grep -v "^#" | head -5
kill -9 "$APP_PID" 2>/dev/null; APP_PID=""

banner "5/5  Live PostgreSQL harnesses (RLS, outbox, crash recovery)"
if command -v initdb >/dev/null 2>&1 || [ -x "/c/Program Files/PostgreSQL/17/bin/initdb.exe" ]; then
  echo "PostgreSQL 17 detected — running live database tests + crash recovery:"
  bash "$REPO/scripts/run-rls-test.sh" 2>&1 | tail -20
  bash "$REPO/scripts/crash-recovery-test.sh" 2>&1 | grep -E ">>>|records present|ledger"
else
  echo "PostgreSQL 17 not found — skipping live DB harnesses."
  echo "Install PG17 (or set it on PATH) and re-run, or: bash scripts/run-rls-test.sh"
fi

banner "Demo complete."
