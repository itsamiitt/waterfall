# Toolchain / environment (Windows Server 2025, this box)

- **Go 1.26:** `/c/Program Files/Go/bin` (add to PATH). `go build ./... && go vet ./...`.
- **-race:** needs CGO + a gcc. `CGO_ENABLED=1 CC=<mingw-gcc> go test -race ./...`.
  mingw gcc is on this box; if `gcc` isn't on PATH, find it under `/c/Users/aamya/tools/` (mingw/msys)
  or `/c/ProgramData/chocolatey/bin`. If no gcc is available, run the non-race tests and note it.
- **PostgreSQL 17:** binaries at `/c/Users/aamya/tools/pg17/pgsql/bin` (initdb, pg_ctl, psql).
  Ephemeral pattern: `initdb -D "$WORK/data" -U postgres -A trust`; `pg_ctl ... -o "-p <port>" start`;
  apply `migrations/00*.sql` in order with `psql -v ON_ERROR_STOP=1 -f`.
- **Integration harness:** `PGBIN=/c/Users/aamya/tools/pg17/pgsql/bin bash scripts/run-rls-test.sh`
  (spins ephemeral PG, applies migrations, runs the -run set; passes `-short` so heavy load tests skip).
  Add your integration test's package + `-run` name to that script.
- **Node/web:** `web/` — `npm run check:ci` (tsc + vitest + allowlist + orphan + build + bundle);
  Playwright E2E is env-gated (E2E_BASE_URL/E2E_EMAIL/E2E_PASSWORD/E2E_TOTP_SECRET).
- **Migrations:** `migrations/NNNN_snake.sql`, NO BEGIN/COMMIT (runner wraps each in a tx). Next free
  number after 0012 is 0013. `-- pgmigrate: no-transaction` directive for CREATE INDEX CONCURRENTLY.
- **apispec parity:** every mounted `/v1/admin` route must appear in
  `docs/waterfall-dashboard/openapi-admin.json` or `internal/dash/apispec` parity fails. Adding a route
  ⇒ add its path/op to openapi-admin.json (+ .yaml).
- **Line endings:** commit with `git -c core.autocrlf=false commit` to avoid CRLF churn.
- **gofmt -w** before committing Go.
