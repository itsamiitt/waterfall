// Package apispec holds the OpenAPI parity contract test for the Waterfall dashboard admin API.
//
// TestAdminOpenAPIParity is the OI-API-1 closeout gate: it asserts the machine-authoritative spec
// docs/waterfall-dashboard/openapi-admin.json is at EXACT parity with the routes actually mounted
// by the feature packages (spec == mux, both directions). It runs offline — no database, no build
// tags — so plain `go test ./internal/dash/apispec/` (and `go test ./...`) exercises it.
//
// Routes are discovered by SCANNING SOURCE rather than importing every feature package (which would
// need each package's live Deps wired). The scanner parses the mux-registration files with go/ast
// and evaluates each `METHOD /v1/admin/...` mux pattern expression, resolving the package-local
// basePath constants, the providers op_state range loop, the configver spec.BasePath indirection
// (routing + workflows), and the httpx s.mount(method, path) helper.
package apispec

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// opsAllowlist is the closed set of NON-/v1/admin operational routes the server mounts. They are
// deliberately excluded from the admin spec (doc 04 §1.1: ops endpoints live outside /v1/admin).
var opsAllowlist = map[string]bool{
	"GET /healthz": true,
	"GET /readyz":  true,
	"GET /metrics": true,
}

var braceParam = regexp.MustCompile(`\{[^}]*\}`)

// normalize collapses every `{param}` path segment to `{}` so param-name differences between the
// spec (e.g. `{scope_key}`, `{job_id}`) and the code (`{scope}`, `{id}`) are not false mismatches.
func normalize(methodSpacePath string) string {
	return braceParam.ReplaceAllString(strings.TrimSpace(methodSpacePath), "{}")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// self = <root>/internal/dash/apispec/parity_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", ".."))
}

func TestAdminOpenAPIParity(t *testing.T) {
	root := repoRoot(t)

	specRoutes := loadSpecRoutes(t, filepath.Join(root, "docs", "waterfall-dashboard", "openapi-admin.json"))
	mountedRoutes, opsRoutes := scanMountedRoutes(t, root)

	if len(specRoutes) == 0 {
		t.Fatal("spec produced zero routes — openapi-admin.json paths not parsed")
	}
	if len(mountedRoutes) == 0 {
		t.Fatal("scanner produced zero mounted routes — the source scan is broken")
	}

	// Sanity: every non-admin route the scanner saw must be a known ops route (nothing leaks).
	var strayOps []string
	for rt := range opsRoutes {
		if !opsAllowlist[rt] {
			strayOps = append(strayOps, rt)
		}
	}
	if len(strayOps) > 0 {
		sort.Strings(strayOps)
		t.Errorf("scanner found non-/v1/admin routes outside the ops allowlist: %v", strayOps)
	}

	// Direction 1: every mounted admin route must appear in the spec.
	var missingFromSpec []string
	for rt := range mountedRoutes {
		if !specRoutes[normalize(rt)] {
			missingFromSpec = append(missingFromSpec, rt)
		}
	}
	// Direction 2: every spec path must map to a mounted route.
	var missingFromMux []string
	mountedNorm := map[string]bool{}
	for rt := range mountedRoutes {
		mountedNorm[normalize(rt)] = true
	}
	for rt := range specRoutes {
		if !mountedNorm[rt] {
			missingFromMux = append(missingFromMux, rt)
		}
	}

	sort.Strings(missingFromSpec)
	sort.Strings(missingFromMux)
	if len(missingFromSpec) > 0 {
		t.Errorf("MOUNTED routes absent from openapi-admin.json (%d):\n  %s",
			len(missingFromSpec), strings.Join(missingFromSpec, "\n  "))
	}
	if len(missingFromMux) > 0 {
		t.Errorf("SPEC paths with no mounted route (%d):\n  %s",
			len(missingFromMux), strings.Join(missingFromMux, "\n  "))
	}

	t.Logf("parity OK: %d mounted admin routes == %d spec operations (%d ops routes allowlisted)",
		len(mountedRoutes), len(specRoutes), len(opsRoutes))
}

// --- spec side ---

// loadSpecRoutes parses openapi-admin.json with encoding/json (no YAML dep) and returns the set of
// normalized "METHOD /path" operations.
func loadSpecRoutes(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec JSON: %v", err)
	}
	httpMethods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true, "trace": true}
	out := map[string]bool{}
	for p, ops := range doc.Paths {
		for m := range ops {
			if !httpMethods[strings.ToLower(m)] {
				continue // skip non-operation keys (parameters, summary, $ref, servers)
			}
			out[normalize(strings.ToUpper(m)+" "+p)] = true
		}
	}
	return out
}

// --- mux side (source scan) ---

// scanMountedRoutes parses the mux-registration source files and returns (adminRoutes, opsRoutes),
// each a set of un-normalized "METHOD /path" strings. adminRoutes are the /v1/admin surface; opsRoutes
// are everything else the mux mounts (validated against opsAllowlist by the caller).
func scanMountedRoutes(t *testing.T, root string) (admin, ops map[string]bool) {
	t.Helper()
	files := gatherRouteFiles(t, root)

	fset := token.NewFileSet()
	parsed := make(map[string]*ast.File, len(files))
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		parsed[f] = af
	}

	// Global fact: every string literal assigned to a `BasePath:` field in a composite literal.
	// This resolves configver's `bp := spec.BasePath` to {"/v1/admin/routing","/v1/admin/workflows"}.
	basePathValues := collectBasePathValues(parsed)

	admin = map[string]bool{}
	ops = map[string]bool{}
	for _, af := range parsed {
		env := fileEnv(af, basePathValues)
		fileBase := ""
		if v, ok := env["basePath"]; ok && len(v) == 1 {
			fileBase = v[0]
		}
		for _, pat := range extractPatterns(af, env, fileBase) {
			method, path, ok := splitPattern(pat)
			if !ok {
				continue
			}
			rt := method + " " + path
			if strings.HasPrefix(path, "/v1/admin") {
				admin[rt] = true
			} else {
				ops[rt] = true
			}
		}
	}
	return admin, ops
}

func gatherRouteFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, pat := range []string{
		filepath.Join(root, "internal", "dash", "*", "http.go"),
		filepath.Join(root, "internal", "dash", "*", "routes.go"),
		filepath.Join(root, "internal", "dash", "*", "server.go"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		files = append(files, m...)
	}
	// The single SSE stream (GET /v1/admin/streams) is registered in realtime/sse.go, outside the
	// http.go/routes.go/server.go convention — include it explicitly so parity stays complete.
	if sse := filepath.Join(root, "internal", "dash", "realtime", "sse.go"); fileExists(sse) {
		files = append(files, sse)
	}
	// Exclude test files defensively (globs above already do, but be explicit).
	out := files[:0]
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		t.Fatal("no route source files found — repo layout changed?")
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// collectBasePathValues gathers all string literals assigned to a field named "BasePath" in any
// composite literal across the parsed files.
func collectBasePathValues(parsed map[string]*ast.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, af := range parsed {
		ast.Inspect(af, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "BasePath" {
				return true
			}
			if s, ok := stringLit(kv.Value); ok && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// fileEnv builds the identifier→string-values environment for a single file: package/file-level
// string consts and vars, plus the two loop/indirection bindings the code relies on:
//   - a `for _, x := range []string{...}` loop var (providers op_state actions), and
//   - a `x := <expr>.BasePath` assignment (configver's `bp`), bound to basePathValues.
func fileEnv(af *ast.File, basePathValues []string) map[string][]string {
	env := map[string][]string{}

	// Pass 1: const/var string bindings. Iterate to a fixpoint so a binding that concatenates an
	// earlier one resolves (all current consts are plain literals, but be robust).
	type binding struct {
		name string
		expr ast.Expr
	}
	var bindings []binding
	for _, d := range af.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, nm := range vs.Names {
				bindings = append(bindings, binding{nm.Name, vs.Values[i]})
			}
		}
	}
	for pass := 0; pass < len(bindings)+1; pass++ {
		progress := false
		for _, b := range bindings {
			if _, done := env[b.name]; done {
				continue
			}
			if vals, ok := evalExpr(b.expr, env, basePathValues); ok {
				env[b.name] = vals
				progress = true
			}
		}
		if !progress {
			break
		}
	}

	// Pass 2: range-loop vars over []string{...} literals (providers op_state actions).
	ast.Inspect(af, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		id, ok := rs.Value.(*ast.Ident)
		if !ok {
			return true
		}
		cl, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var vals []string
		for _, e := range cl.Elts {
			if s, ok := stringLit(e); ok {
				vals = append(vals, s)
			}
		}
		if len(vals) > 0 {
			env[id.Name] = vals
		}
		return true
	})

	// Pass 3: `x := <sel>.BasePath` assignments (configver's `bp`).
	ast.Inspect(af, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if sel, ok := as.Rhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "BasePath" {
			env[lhs.Name] = basePathValues
		}
		return true
	})

	return env
}

// extractPatterns walks a file's mux registrations and returns every resolvable "METHOD /path"
// pattern. It handles mux.HandleFunc / mux.Handle (arg0 = pattern) and the httpx s.mount helper
// (args = mux, method, path → method + " " + fileBase + path).
func extractPatterns(af *ast.File, env map[string][]string, fileBase string) []string {
	var out []string
	ast.Inspect(af, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "HandleFunc", "Handle":
			if len(call.Args) < 1 {
				return true
			}
			if vals, ok := evalExpr(call.Args[0], env, nil); ok {
				out = append(out, vals...)
			}
		case "mount":
			// s.mount(mux, method, path, handler): 4 args, [1]=method, [2]=path.
			if len(call.Args) < 3 || fileBase == "" {
				return true
			}
			methods, ok1 := evalExpr(call.Args[1], env, nil)
			paths, ok2 := evalExpr(call.Args[2], env, nil)
			if !ok1 || !ok2 {
				return true
			}
			for _, m := range methods {
				for _, p := range paths {
					out = append(out, m+" "+fileBase+p)
				}
			}
		}
		return true
	})
	return out
}

// evalExpr evaluates a string-typed expression to its set of possible concrete values. It resolves
// string literals, identifiers (via env), `+` concatenation (cartesian product), parentheses, and a
// bare `<x>.BasePath` selector (→ basePathValues, when provided). Anything else → (nil, false).
func evalExpr(e ast.Expr, env map[string][]string, basePathValues []string) ([]string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if s, ok := stringLit(v); ok {
			return []string{s}, true
		}
		return nil, false
	case *ast.Ident:
		if vals, ok := env[v.Name]; ok {
			return vals, true
		}
		return nil, false
	case *ast.ParenExpr:
		return evalExpr(v.X, env, basePathValues)
	case *ast.SelectorExpr:
		if v.Sel.Name == "BasePath" && len(basePathValues) > 0 {
			return basePathValues, true
		}
		return nil, false
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return nil, false
		}
		left, ok := evalExpr(v.X, env, basePathValues)
		if !ok {
			return nil, false
		}
		right, ok := evalExpr(v.Y, env, basePathValues)
		if !ok {
			return nil, false
		}
		out := make([]string, 0, len(left)*len(right))
		for _, l := range left {
			for _, r := range right {
				out = append(out, l+r)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func stringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// splitPattern splits a "METHOD /path" mux pattern into its method and path. It accepts only
// patterns that carry an explicit method (every admin route does).
func splitPattern(pat string) (method, path string, ok bool) {
	pat = strings.TrimSpace(pat)
	i := strings.IndexByte(pat, ' ')
	if i <= 0 {
		return "", "", false
	}
	method = strings.TrimSpace(pat[:i])
	path = strings.TrimSpace(pat[i+1:])
	if !strings.HasPrefix(path, "/") {
		return "", "", false
	}
	return method, path, true
}
