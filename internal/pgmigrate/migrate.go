// Package pgmigrate is a tiny ordered SQL migration runner. It applies `NNNN_*.sql` files
// from a directory in filename order, recording each in a `schema_migrations` table so
// re-runs are no-ops. Each file is applied together with its version record in ONE
// transaction, so a migration is all-or-nothing (this is why the migration files carry no
// BEGIN/COMMIT of their own).
//
// Escape hatch — `-- pgmigrate: no-transaction`. A migration whose first ~5 lines carry the
// directive comment `-- pgmigrate: no-transaction` is applied WITHOUT the wrapping BEGIN/COMMIT:
// its statements are split and executed one at a time, each auto-committing. This is required for
// statements Postgres refuses to run inside a transaction block — notably
// `CREATE INDEX CONCURRENTLY`. The trade-off is deliberate: a no-transaction migration is NOT
// atomic (an earlier statement stays committed if a later one fails), so such files must be written
// to be resumable/idempotent. The `schema_migrations` row is still written (in its own
// auto-committing statement) only after every statement in the file succeeds. Directive-less files
// keep the exact atomic-wrap behavior — a failing normal migration still rolls back whole.
package pgmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/enrichment/waterfall/internal/pg"
)

// Apply runs every not-yet-applied migration in dir against conn, returning the versions it
// newly applied (in order). It is idempotent: already-applied files are skipped.
func Apply(conn *pg.Conn, dir string) ([]string, error) {
	if err := conn.Exec(`create table if not exists schema_migrations (
		version    text primary key,
		applied_at timestamptz not null default now()
	)`); err != nil {
		return nil, fmt.Errorf("pgmigrate: ensure schema_migrations: %w", err)
	}

	applied, err := appliedVersions(conn)
	if err != nil {
		return nil, err
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var newly []string
	for _, f := range files {
		version := filepath.Base(f)
		if applied[version] {
			continue
		}
		sqlBytes, err := os.ReadFile(f)
		if err != nil {
			return newly, err
		}
		body := string(sqlBytes)
		if hasNoTxnDirective(body) {
			if err := applyNoTxn(conn, version, body); err != nil {
				return newly, err
			}
		} else {
			if err := applyAtomic(conn, version, body); err != nil {
				return newly, err
			}
		}
		newly = append(newly, version)
	}
	return newly, nil
}

// applyAtomic runs a directive-less migration and its version record in ONE transaction, so the
// whole step is all-or-nothing. The file has no transaction control, so wrapping it here makes it
// atomic — a failing statement rolls back everything, including the schema_migrations insert. This
// is the historical behavior; it is preserved byte-for-byte for every existing migration.
func applyAtomic(conn *pg.Conn, version, body string) error {
	stmt := "begin;\n" + body + "\n;insert into schema_migrations (version) values (" + quoteLiteral(version) + ");\ncommit;"
	if err := conn.Exec(stmt); err != nil {
		_ = conn.Exec("rollback")
		return fmt.Errorf("pgmigrate: applying %s: %w", version, err)
	}
	return nil
}

// applyNoTxn runs a `-- pgmigrate: no-transaction` migration: it splits the file into individual
// statements and executes each on its own via the simple query protocol, so each auto-commits and
// none is wrapped in an implicit transaction block (which is what lets CREATE INDEX CONCURRENTLY
// succeed). Statements are NOT rolled back on a later failure — the file must be resumable. The
// version is recorded only after every statement succeeds, in its own auto-committing insert.
func applyNoTxn(conn *pg.Conn, version, body string) error {
	for _, stmt := range splitStatements(body) {
		if err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("pgmigrate: applying %s (no-transaction; prior statements committed): %w", version, err)
		}
	}
	if err := conn.Exec("insert into schema_migrations (version) values (" + quoteLiteral(version) + ")"); err != nil {
		return fmt.Errorf("pgmigrate: recording %s: %w", version, err)
	}
	return nil
}

// noTxnDirective is the escape-hatch marker; a comment line containing it in the file's first ~5
// lines switches the runner to the no-transaction path.
const noTxnDirective = "pgmigrate: no-transaction"

// hasNoTxnDirective reports whether the migration opts out of the transactional wrap. It scans the
// first few physical lines for a SQL line-comment (`--`) carrying the directive token.
func hasNoTxnDirective(sql string) bool {
	lines := strings.SplitN(sql, "\n", 8)
	for i, ln := range lines {
		if i >= 6 {
			break
		}
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "--") && strings.Contains(t, noTxnDirective) {
			return true
		}
	}
	return false
}

// splitStatements splits a SQL file into individual top-level statements on semicolons, ignoring
// semicolons inside line comments (`-- …`), block comments (`/* … */`), single/double-quoted
// literals (with doubled-quote escapes), and dollar-quoted strings (`$tag$ … $tag$`). Statements
// that are empty after trimming are dropped. Only the no-transaction path uses this; the atomic
// path still sends the whole file as one simple query, unchanged.
func splitStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			stmts = append(stmts, s)
		}
		cur.Reset()
	}
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			j := i + 2
			for j < n && sql[j] != '\n' {
				j++
			}
			cur.WriteString(sql[i:j])
			i = j
		case c == '/' && i+1 < n && sql[i+1] == '*':
			j := i + 2
			for j+1 < n && !(sql[j] == '*' && sql[j+1] == '/') {
				j++
			}
			if j+1 < n {
				j += 2
			} else {
				j = n
			}
			cur.WriteString(sql[i:j])
			i = j
		case c == '\'' || c == '"':
			j := i + 1
			for j < n {
				if sql[j] == c {
					if j+1 < n && sql[j+1] == c { // doubled-quote escape
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			cur.WriteString(sql[i:j])
			i = j
		case c == '$':
			if tag, ok := dollarTag(sql, i); ok {
				rest := sql[i+len(tag):]
				end := strings.Index(rest, tag)
				var j int
				if end < 0 {
					j = n
				} else {
					j = i + len(tag) + end + len(tag)
				}
				cur.WriteString(sql[i:j])
				i = j
			} else {
				cur.WriteByte(c)
				i++
			}
		case c == ';':
			flush()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return stmts
}

// dollarTag returns the dollar-quote opening delimiter starting at sql[i] (which must be '$'),
// e.g. "$$" or "$func$", and whether one was found. A tag is `$`, an optional identifier, then `$`.
func dollarTag(sql string, i int) (string, bool) {
	for j := i + 1; j < len(sql); j++ {
		c := sql[j]
		if c == '$' {
			return sql[i : j+1], true
		}
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return "", false
}

// Pending returns the migration files in dir NOT yet recorded as applied, in order. It is a
// read-only check for the startup self-check: an app that does not run migrations itself (no
// admin DSN) can verify its database is fully migrated and refuse to start otherwise. If
// schema_migrations does not exist yet, every file is pending.
func Pending(conn *pg.Conn, dir string) ([]string, error) {
	applied := map[string]bool{}
	// A missing schema_migrations table means nothing has been applied — treat as all pending.
	if res, err := conn.Query("select version from schema_migrations"); err == nil {
		for _, row := range res.Rows {
			if row[0] != nil {
				applied[*row[0]] = true
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var pending []string
	for _, f := range files {
		if v := filepath.Base(f); !applied[v] {
			pending = append(pending, v)
		}
	}
	return pending, nil
}

func appliedVersions(conn *pg.Conn) (map[string]bool, error) {
	res, err := conn.Query("select version from schema_migrations")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(res.Rows))
	for _, row := range res.Rows {
		if row[0] != nil {
			out[*row[0]] = true
		}
	}
	return out, nil
}

// quoteLiteral single-quote-escapes a string literal (versions are filenames — safe — but be
// defensive).
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
