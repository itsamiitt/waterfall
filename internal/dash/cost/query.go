// Package cost is the dashboard's cost-analytics surface (module 10, doc 12 §P6): a single
// whitelist group-by query builder over the cost/usage rollups, cost-per-enrichment and
// cost-per-successful-result (numerator + denominator carried together, never a pre-divided
// ratio), ROI per filled Field, a linear+seasonal forecast, budgets CRUD (alerting only —
// doctrine: budgets alert, G4 Cost Ceilings enforce), and an NDJSON export that streams the
// SAME query builder over keyset-paginated short transactions.
//
// Gates / invariants:
//   - G1 tenant isolation. Every read runs through db.Store.Tx bound to the ctx Principal; a
//     tenant sees only its own rollup rows, an operator may span Tenants only via the enumerated
//     operator SELECT policies (cost_rollup_1d / tenant_usage_1d). Reads NEVER scan raw
//     usage_events (doc 04 §2.10) — the API serves exclusively from rollups.
//   - Closed dimension set. group_by is validated against a fixed whitelist; anything else is a
//     400. As of T4 (doc 15 §T4) group_by=key serves cost_rollup_1d.key_id (Class T, tenant-scoped
//     by RLS) — revising the original doc 03 §2.6 RF-3 boundary, which routed it to the operator-only
//     key_usage_1d rollup. The `operator` spec flag + ErrKeyGroupByForbidden remain the general seam
//     for any future operator-only group_by, but no dimension is operator-only today.
//   - Bounded windows. Windows beyond a source's retention horizon are rejected
//     (ErrWindowOutOfRange -> 400 window_out_of_range).
package cost

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrWindowOutOfRange is returned when a requested window is inverted or reaches beyond the
// source rollup's retention horizon (doc 03 §4). The HTTP layer maps it to 400
// window_out_of_range (doc 04 §1.4).
var ErrWindowOutOfRange = errors.New("cost: requested window is out of range")

// ErrInvalidGroupBy is returned for a group_by outside the closed whitelist (doc 04 §2.10). The
// HTTP layer maps it to 400 invalid_filter.
var ErrInvalidGroupBy = errors.New("cost: group_by is not in the allowed set")

// ErrInvalidFilter is returned for a filter dimension that is not valid for the chosen source.
var ErrInvalidFilter = errors.New("cost: filter dimension is not allowed for this group_by")

// ErrKeyGroupByForbidden is returned when a non-operator requests group_by=key (Class-P
// key_usage_1d has no tenant read projection — doc 03 §2.6). The HTTP layer maps it to 403.
var ErrKeyGroupByForbidden = errors.New("cost: group_by=key is operator-only")

const day = 24 * time.Hour

// groupSpec pins one whitelisted group_by to its physical source: the rollup table, the grouped
// key column, the time column, and the credits/calls/successful-results aggregate expressions.
// Column and table names come only from this fixed table — never from request input — so the
// builder is injection-free even though it concatenates identifiers.
type groupSpec struct {
	source    string        // rollup table
	keyCol    string        // grouped dimension column (also the JSON key)
	timeCol   string        // window column (day for cost_rollup_1d, bucket_start otherwise)
	timeIsDay bool          // true when timeCol is a DATE (cost_rollup_1d.day)
	credits   string        // SUM(...) expression for credits
	calls     string        // SUM(...) expression for calls (the numerator's denominator)
	success   string        // SUM(...) expression for successful results
	filters   []string      // dimension columns that may be filtered on this source
	operator  bool          // true => operator-only (Class-P source)
	retention time.Duration // window horizon (doc 03 §4)
}

// groupSpecs is the CLOSED group_by whitelist (doc 04 §2.10). All five dimensions
// (provider/tenant/workflow/country/key) serve cost_rollup_1d; key uses the key_id column added in
// migration 0012 (T4 / RF-3). Tenant/workflow/country/key are tenant-scoped by RLS.
var groupSpecs = map[string]groupSpec{
	"provider": {
		source: "cost_rollup_1d", keyCol: "provider_id", timeCol: "day", timeIsDay: true,
		credits: "coalesce(sum(credits),0)", calls: "coalesce(sum(calls),0)",
		success:   "coalesce(sum(successful_results),0)",
		filters:   []string{"provider_id", "workflow_key", "country"},
		retention: 730 * day,
	},
	"tenant": {
		source: "cost_rollup_1d", keyCol: "tenant_id", timeCol: "day", timeIsDay: true,
		credits: "coalesce(sum(credits),0)", calls: "coalesce(sum(calls),0)",
		success:   "coalesce(sum(successful_results),0)",
		filters:   []string{"provider_id", "workflow_key", "country"},
		retention: 730 * day,
	},
	"workflow": {
		source: "cost_rollup_1d", keyCol: "workflow_key", timeCol: "day", timeIsDay: true,
		credits: "coalesce(sum(credits),0)", calls: "coalesce(sum(calls),0)",
		success:   "coalesce(sum(successful_results),0)",
		filters:   []string{"provider_id", "workflow_key", "country"},
		retention: 730 * day,
	},
	"country": {
		source: "cost_rollup_1d", keyCol: "country", timeCol: "day", timeIsDay: true,
		credits: "coalesce(sum(credits),0)", calls: "coalesce(sum(calls),0)",
		success:   "coalesce(sum(successful_results),0)",
		filters:   []string{"provider_id", "workflow_key", "country"},
		retention: 730 * day,
	},
	"key": {
		// RF-3 (T4, doc 15 §T4): key-scoped cost now serves cost_rollup_1d, which carries key_id as
		// of migration 0012. Because cost_rollup_1d is Class T (tenant-isolated by RLS, with the
		// enumerated operator cross-Tenant read policy), key drill-down is available to a Tenant for
		// its OWN keys — no longer operator-only — and gains the cross-dimension filters the
		// key_usage_1d source could not offer. Days folded before T4 land under key_id='' (rendered
		// as the empty/"unattributed" bucket). 2y retention matches the other cost_rollup_1d grains.
		source: "cost_rollup_1d", keyCol: "key_id", timeCol: "day", timeIsDay: true,
		credits: "coalesce(sum(credits),0)", calls: "coalesce(sum(calls),0)",
		success:   "coalesce(sum(successful_results),0)",
		filters:   []string{"provider_id", "workflow_key", "country"},
		retention: 730 * day,
	},
}

// checkWindow rejects an inverted window or one whose lower bound predates the retention horizon
// (now - retention). Pure so the bound logic is unit-testable without a DB.
func checkWindow(from, to, now time.Time, retention time.Duration) error {
	if !to.After(from) {
		return ErrWindowOutOfRange
	}
	if from.Before(now.Add(-retention)) {
		return ErrWindowOutOfRange
	}
	return nil
}

// query is a validated, bound group-by request: the whitelisted spec plus its parameter list.
type query struct {
	spec    groupSpec
	groupBy string
	from    time.Time
	to      time.Time
	filters map[string]string // dim -> value (validated against spec.filters)
}

// buildQuery validates group_by/window/filters/role and returns a bound query. isOperator gates
// group_by=key. now bounds the retention check.
func buildQuery(groupBy string, from, to time.Time, filters map[string]string, isOperator bool, now time.Time) (query, error) {
	spec, ok := groupSpecs[groupBy]
	if !ok {
		return query{}, ErrInvalidGroupBy
	}
	if spec.operator && !isOperator {
		return query{}, ErrKeyGroupByForbidden
	}
	if err := checkWindow(from, to, now, spec.retention); err != nil {
		return query{}, err
	}
	for dim := range filters {
		if !contains(spec.filters, dim) {
			return query{}, fmt.Errorf("%w: %q", ErrInvalidFilter, dim)
		}
	}
	return query{spec: spec, groupBy: groupBy, from: from, to: to, filters: filters}, nil
}

// sql renders the aggregate SELECT with a keyset lower bound (cursorKey, exclusive) for streaming
// export; pass "" for a single unbounded page. limit<=0 means no LIMIT. Parameters are returned
// separately (never interpolated).
func (q query) sql(cursorKey string, limit int) (string, []any) {
	s := q.spec
	var b strings.Builder
	args := make([]any, 0, 6)

	fmt.Fprintf(&b, "select %s as gkey, %s as credits, %s as calls, %s as success from %s where %s >= $1 and %s < $2",
		s.keyCol, s.credits, s.calls, s.success, s.source, s.timeCol, s.timeCol)
	if s.timeIsDay {
		args = append(args, q.from.UTC().Format("2006-01-02"), q.to.UTC().Format("2006-01-02"))
	} else {
		args = append(args, q.from.UTC(), q.to.UTC())
	}

	// Whitelisted equality filters. Sorted for a stable parameter order.
	dims := make([]string, 0, len(q.filters))
	for dim := range q.filters {
		dims = append(dims, dim)
	}
	sort.Strings(dims)
	for _, dim := range dims {
		args = append(args, q.filters[dim])
		fmt.Fprintf(&b, " and %s = $%d", dim, len(args))
	}

	if cursorKey != "" {
		args = append(args, cursorKey)
		fmt.Fprintf(&b, " and %s > $%d", s.keyCol, len(args))
	}

	fmt.Fprintf(&b, " group by %s order by %s asc", s.keyCol, s.keyCol)
	if limit > 0 {
		fmt.Fprintf(&b, " limit %d", limit)
	}
	return b.String(), args
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
