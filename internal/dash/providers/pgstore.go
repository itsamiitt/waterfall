package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// PGStore is the production Store over the Class-P providers table + providers_catalog view.
// Full-row access uses db.Store.PlatformTx (tenant='platform'); the catalog projection uses the
// caller's own Principal so RLS narrows it to tenant_readable rows.
type PGStore struct {
	store *db.Store
}

// NewPGStore wires a PGStore to the shared db.Store.
func NewPGStore(store *db.Store) *PGStore { return &PGStore{store: store} }

var _ Store = (*PGStore)(nil)

// Insert writes a new providers row (all defaults NOT supplied fall to the DB) and returns the
// stored row. A slug collision surfaces as ErrConflict.
func (s *PGStore) Insert(ctx context.Context, cols []colVal) (Provider, error) {
	names, placeholders, args := renderCols(cols, 0)
	sql := `insert into providers (` + strings.Join(names, ", ") + `) values (` +
		strings.Join(placeholders, ", ") + `) returning ` + fullColumns
	var out Provider
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		out = scanProvider(res.Rows[0])
		return nil
	})
	if isUniqueViolation(err) {
		return Provider{}, ErrConflict
	}
	if err != nil {
		return Provider{}, err
	}
	return out, nil
}

// Update applies the column set (always bumping updated_at) and returns the updated row, or
// ErrNotFound when no such provider exists.
func (s *PGStore) Update(ctx context.Context, id string, cols []colVal) (Provider, error) {
	names, placeholders, args := renderCols(cols, 0)
	assigns := make([]string, 0, len(names)+1)
	for i := range names {
		assigns = append(assigns, names[i]+" = "+placeholders[i])
	}
	assigns = append(assigns, "updated_at = now()")
	args = append(args, id)
	sql := `update providers set ` + strings.Join(assigns, ", ") +
		fmt.Sprintf(` where id = $%d returning `, len(args)) + fullColumns
	var out Provider
	found := false
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = scanProvider(res.Rows[0])
		found = true
		return nil
	})
	if err != nil {
		return Provider{}, err
	}
	if !found {
		return Provider{}, ErrNotFound
	}
	return out, nil
}

// Delete hard-removes a providers row, reporting whether one existed.
func (s *PGStore) Delete(ctx context.Context, id string) (bool, error) {
	deleted := false
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`delete from providers where id = $1 returning id`, id)
		if err != nil {
			return err
		}
		deleted = len(res.Rows) > 0
		return nil
	})
	return deleted, err
}

// GetFull returns the complete row (operator scope). ErrNotFound when absent.
func (s *PGStore) GetFull(ctx context.Context, id string) (Provider, error) {
	return s.getOne(ctx, false, id)
}

// GetCatalog returns the tenant catalog projection for one provider under the caller's Principal.
func (s *PGStore) GetCatalog(ctx context.Context, id string) (Provider, error) {
	return s.getOne(ctx, true, id)
}

func (s *PGStore) getOne(ctx context.Context, catalog bool, id string) (Provider, error) {
	table, cols, scan := fullTable(catalog)
	var out Provider
	found := false
	run := s.store.PlatformTx
	if catalog {
		run = s.store.Tx
	}
	err := run(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select `+cols+` from `+table+` where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = scan(res.Rows[0])
		found = true
		return nil
	})
	if err != nil {
		return Provider{}, err
	}
	if !found {
		return Provider{}, ErrNotFound
	}
	return out, nil
}

// ListFull lists the full catalog (operator scope), cursor-paginated on id.
func (s *PGStore) ListFull(ctx context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	return s.list(ctx, false, f, cur, limit)
}

// ListCatalog lists the tenant catalog projection under the caller's Principal.
func (s *PGStore) ListCatalog(ctx context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	return s.list(ctx, true, f, cur, limit)
}

func (s *PGStore) list(ctx context.Context, catalog bool, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	table, cols, scan := fullTable(catalog)

	wb := &whereBuilder{}
	applyFilters(wb, f, catalog)
	if cur.ID != "" {
		wb.add("id > $%d", cur.ID)
	}
	where := ""
	if len(wb.conds) > 0 {
		where = " where " + strings.Join(wb.conds, " and ")
	}
	sql := `select ` + cols + ` from ` + table + where +
		fmt.Sprintf(" order by id asc limit $%d", wb.next())
	args := append(wb.args, int64(limit+1))

	var out []Provider
	var next db.Cursor
	run := s.store.PlatformTx
	if catalog {
		run = s.store.Tx
	}
	err := run(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			rows = rows[:limit]
			next = db.Cursor{ID: sstr(rows[limit-1][0])}
		}
		out = make([]Provider, 0, len(rows))
		for _, row := range rows {
			out = append(out, scan(row))
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// GetManyFull returns the full rows for the given ids (operator scope), preserving no order.
func (s *PGStore) GetManyFull(ctx context.Context, ids []string) ([]Provider, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []Provider
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select `+fullColumns+` from providers where id = any($1::text[])`, formatTextArray(ids))
		if err != nil {
			return err
		}
		out = make([]Provider, 0, len(res.Rows))
		for _, row := range res.Rows {
			out = append(out, scanProvider(row))
		}
		return nil
	})
	return out, err
}

// fullTable returns the table/view, projection columns, and row scanner for a scope.
func fullTable(catalog bool) (string, string, func([]*string) Provider) {
	if catalog {
		return "providers_catalog", catalogColumns, scanCatalog
	}
	return "providers", fullColumns, scanProvider
}

// --- SQL assembly helpers ---

// renderCols turns a colVal slice into parallel (names, $-placeholders, args) starting after
// startArg existing params. Casts are appended to the placeholder (e.g. "$3::jsonb").
func renderCols(cols []colVal, startArg int) (names, placeholders []string, args []any) {
	n := startArg
	for _, cv := range cols {
		n++
		names = append(names, cv.name)
		ph := fmt.Sprintf("$%d", n)
		if cv.cast != "" {
			ph += "::" + cv.cast
		}
		placeholders = append(placeholders, ph)
		args = append(args, cv.val)
	}
	return names, placeholders, args
}

// whereBuilder accumulates parameterized predicates with $-placeholders in order.
type whereBuilder struct {
	conds []string
	args  []any
}

// add appends a predicate binding val to the next placeholder index; every "$%d" occurrence in
// condFmt is filled with that same index (so a predicate may reference the parameter twice).
func (b *whereBuilder) add(condFmt string, val any) {
	b.args = append(b.args, val)
	idx := len(b.args)
	idxs := make([]any, strings.Count(condFmt, "$%d"))
	for i := range idxs {
		idxs[i] = idx
	}
	b.conds = append(b.conds, fmt.Sprintf(condFmt, idxs...))
}

// next is the placeholder index of the NEXT parameter to be appended (for the trailing limit).
func (b *whereBuilder) next() int { return len(b.args) + 1 }

// applyFilters translates a Filter into predicates. op_state is skipped on the tenant catalog
// projection (the view does not expose it).
func applyFilters(b *whereBuilder, f Filter, catalog bool) {
	if f.Status != "" {
		b.add("status = $%d", f.Status)
	}
	if f.OpState != "" && !catalog {
		b.add("op_state = $%d", f.OpState)
	}
	if f.Category != "" {
		b.add("category = $%d", f.Category)
	}
	if f.Q != "" {
		b.add("(lower(display_name) like lower($%d) or lower(id) like lower($%d))", f.Q+"%")
	}
	if f.Region != "" {
		b.add("$%d = any(region)", f.Region)
	}
	if f.Tag != "" {
		b.add("$%d = any(tags)", f.Tag)
	}
}

func isUniqueViolation(err error) bool {
	var pe *pg.PGError
	return errors.As(err, &pe) && pe.Code == "23505"
}
