// Package pgmigrate is a tiny ordered SQL migration runner. It applies `NNNN_*.sql` files
// from a directory in filename order, recording each in a `schema_migrations` table so
// re-runs are no-ops. Each file is applied together with its version record in ONE
// transaction, so a migration is all-or-nothing (this is why the migration files carry no
// BEGIN/COMMIT of their own).
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
		// Apply the migration and record it atomically. The file has no transaction control,
		// so wrapping it here makes the whole step all-or-nothing.
		stmt := "begin;\n" + string(sqlBytes) + "\n;insert into schema_migrations (version) values (" + quoteLiteral(version) + ");\ncommit;"
		if err := conn.Exec(stmt); err != nil {
			_ = conn.Exec("rollback")
			return newly, fmt.Errorf("pgmigrate: applying %s: %w", version, err)
		}
		newly = append(newly, version)
	}
	return newly, nil
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
