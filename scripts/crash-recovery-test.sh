#!/usr/bin/env bash
# crash-recovery-test.sh — live, end-to-end proof of the Postgres durable path through the REAL
# enrichapi binary. It submits async jobs, HARD-KILLS the process mid-flight (kill -9, i.e. a
# crash, not a graceful stop), restarts it, and asserts every job still completes — recovered
# from the transactional outbox by the relay, with G2 idempotency preventing double execution.
#
# Requires PostgreSQL 17 (bin dir below) + the Go toolchain. Stands up an EPHEMERAL trust cluster
# (per the project's no-persistent-service constraint), self-bootstraps schema + roles via the
# binary's POSTGRES_ADMIN_DSN path, and tears everything down at the end.
set -uo pipefail

PGBIN="/c/Program Files/PostgreSQL/17/bin"
export PATH="$PGBIN:/c/Users/Administrator/go-sdk/go/bin:$PATH"
export GOTOOLCHAIN=local CGO_ENABLED=0
REPO="/c/Users/Administrator/Downloads/waterfall"
SCRATCH="/c/Users/ADMINI~1/AppData/Local/Temp/2/claude/C--Users-Administrator-Downloads-waterfall/65eefe07-fd1c-4c32-b7b0-40759953da13/scratchpad"
PGDATA="$SCRATCH/pgdata_crash"
PGPORT=55432
APPPORT=8099
BIN="$SCRATCH/enrichapi_crash.exe"
N=40
TOK="Authorization: Bearer acme-token"   # dev token -> tenant-acme, enrich:write scope

ADMIN_DSN="host=127.0.0.1 port=$PGPORT user=postgres dbname=postgres"
APP_DSN="host=127.0.0.1 port=$PGPORT user=app_rls dbname=postgres"
RELAY_DSN="host=127.0.0.1 port=$PGPORT user=relay dbname=postgres"

say() { echo ">>> $*"; }
psql_admin() { psql -h 127.0.0.1 -p "$PGPORT" -U postgres -d postgres -tAc "$1" 2>/dev/null; }

start_app() {  # -> echoes PID
  ( cd "$REPO" && exec env \
      POSTGRES_ADMIN_DSN="$ADMIN_DSN" POSTGRES_DSN="$APP_DSN" POSTGRES_RELAY_DSN="$RELAY_DSN" \
      PORT="$APPPORT" MIGRATIONS_DIR="migrations" \
      "$BIN" >>"$SCRATCH/app.log" 2>&1 ) &
  echo $!
}
wait_ready() {
  for _ in $(seq 1 40); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$APPPORT/healthz")" = "200" ]; then return 0; fi
    sleep 0.25
  done
  return 1
}

cleanup() {
  [ -n "${APP_PID:-}" ] && kill -9 "$APP_PID" 2>/dev/null
  pg_ctl -D "$PGDATA" stop -m fast >/dev/null 2>&1
  rm -rf "$PGDATA" "$SCRATCH/app.log" "$SCRATCH/pg_crash.log"
}
trap cleanup EXIT

# --- 0. build + fresh cluster -------------------------------------------------
say "building enrichapi"
( cd "$REPO" && go build -o "$BIN" ./cmd/enrichapi ) || { echo "build failed"; exit 1; }
say "initdb (ephemeral trust cluster)"
rm -rf "$PGDATA"
initdb -A trust -U postgres -D "$PGDATA" >/dev/null 2>&1 || { echo "initdb failed"; exit 1; }
pg_ctl -D "$PGDATA" -o "-p $PGPORT" -l "$SCRATCH/pg_crash.log" -w start >/dev/null 2>&1 || { echo "pg start failed"; cat "$SCRATCH/pg_crash.log"; exit 1; }

# --- 1. round one: submit, then CRASH -----------------------------------------
: > "$SCRATCH/app.log"
say "starting binary (round 1) — self-bootstraps migrations + app/relay roles"
APP_PID=$(start_app)
wait_ready || { echo "app not ready (round 1)"; tail -20 "$SCRATCH/app.log"; exit 1; }
say "app up as PID $APP_PID; submitting $N async jobs"
for i in $(seq 0 $((N-1))); do
  curl -s -o /dev/null -X POST "http://127.0.0.1:$APPPORT/v1/enrichments" \
    -H "$TOK" -H "Idempotency-Key: idem-$i" -H "Content-Type: application/json" \
    -d "{\"subject\":{\"id\":\"rec-$i\",\"known\":{\"company_domain\":\"acme.com\",\"first_name\":\"jane\",\"last_name\":\"doe\"}},\"want\":[\"work_email\"],\"confidence_target\":0.7,\"cost_ceiling\":100,\"config_version\":\"v1\"}"
done
# HARD kill immediately — a crash, not a graceful shutdown.
kill -9 "$APP_PID" 2>/dev/null
sleep 0.5
PENDING_AT_CRASH=$(psql_admin "select count(*) from job_outbox where pending;")
TOTAL_ROWS=$(psql_admin "select count(*) from job_outbox;")
say "CRASHED (kill -9). durably captured=$TOTAL_ROWS  still-pending-at-crash=$PENDING_AT_CRASH"

# --- 2. round two: restart, relay recovers ------------------------------------
say "restarting binary (round 2) — relay drains the outbox"
APP_PID=$(start_app)
wait_ready || { echo "app not ready (round 2)"; tail -20 "$SCRATCH/app.log"; exit 1; }
say "waiting for recovery (all $N records present)"
present=0
for _ in $(seq 1 60); do
  present=0
  for i in $(seq 0 $((N-1))); do
    body=$(curl -s -H "$TOK" "http://127.0.0.1:$APPPORT/v1/records/rec-$i")
    case "$body" in *'"work_email"'*) present=$((present+1));; esac
  done
  [ "$present" -eq "$N" ] && break
  sleep 0.5
done

DELIVERED=$(psql_admin "select count(*) from job_outbox where not pending;")
LEDGER=$(psql_admin "select count(*) from idempotency_ledger;")
STILL_PENDING=$(psql_admin "select count(*) from job_outbox where pending;")

echo
say "RESULT"
echo "    total jobs submitted      : $N"
echo "    durably captured (outbox) : $TOTAL_ROWS"
echo "    pending at crash          : $PENDING_AT_CRASH"
echo "    records present post-recovery: $present / $N"
echo "    outbox delivered (terminal): $DELIVERED"
echo "    idempotency-ledger rows   : $LEDGER   (G2: >= submitted, no double-charge)"
echo "    still pending             : $STILL_PENDING"

if [ "$present" -eq "$N" ] && [ "$STILL_PENDING" -eq 0 ] && [ "$DELIVERED" -eq "$N" ]; then
  echo
  say "PASS — every job survived the crash and completed via outbox recovery."
  exit 0
fi
echo
say "FAIL — not all jobs recovered."
tail -30 "$SCRATCH/app.log"
exit 1
