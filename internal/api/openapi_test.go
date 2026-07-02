package api_test

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// openAPISpec is the minimal shape of the spec we contract-test against.
type openAPISpec struct {
	Paths map[string]map[string]struct {
		Responses map[string]any `json:"responses"`
	} `json:"paths"`
}

func loadSpec(t *testing.T) openAPISpec {
	t.Helper()
	raw, err := os.ReadFile("../../docs/api/openapi.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec openAPISpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return spec
}

// declares reports whether the spec declares statusCode for the given path+method.
func (s openAPISpec) declares(path, method, statusCode string) bool {
	op, ok := s.Paths[path][method]
	if !ok {
		return false
	}
	_, ok = op.Responses[statusCode]
	return ok
}

// TestOpenAPI_ContractMatchesImplementation binds the spec to the running handler: every
// status code the implementation returns for a representative request must be DECLARED in
// the OpenAPI spec (the spec cannot silently omit or misstate a response the API produces).
func TestOpenAPI_ContractMatchesImplementation(t *testing.T) {
	spec := loadSpec(t)
	env := setup(t, nil)
	h := env.handler

	type probe struct {
		name         string
		path, method string
		exec         func() int // returns observed status code
	}
	b := body("person-oa", 100, 0.85, "work_email")
	probes := []probe{
		{"healthz", "/healthz", "get", func() int {
			return do(h, "GET", "/healthz", "", "", nil).Code
		}},
		{"submit sync ok", "/v1/enrichments", "post", func() int {
			return do(h, "POST", "/v1/enrichments?mode=sync", "tokA", "oa-1", b).Code
		}},
		{"submit async ok", "/v1/enrichments", "post", func() int {
			return do(h, "POST", "/v1/enrichments", "tokA", "oa-async", b).Code
		}},
		{"submit no auth", "/v1/enrichments", "post", func() int {
			return do(h, "POST", "/v1/enrichments", "", "oa-2", b).Code
		}},
		{"submit no idem", "/v1/enrichments", "post", func() int {
			return do(h, "POST", "/v1/enrichments", "tokA", "", b).Code
		}},
		{"submit bad field", "/v1/enrichments", "post", func() int {
			return do(h, "POST", "/v1/enrichments?mode=sync", "tokA", "oa-3", body("p", 10, 0.8, "nope_field")).Code
		}},
		{"get job not found", "/v1/enrichments/{id}", "get", func() int {
			return do(h, "GET", "/v1/enrichments/does-not-exist", "tokA", "", nil).Code
		}},
		{"get job no auth", "/v1/enrichments/{id}", "get", func() int {
			return do(h, "GET", "/v1/enrichments/x", "", "", nil).Code
		}},
		{"get record ok", "/v1/records/{subjectID}", "get", func() int {
			return do(h, "GET", "/v1/records/anything", "tokA", "", nil).Code
		}},
		{"get record no auth", "/v1/records/{subjectID}", "get", func() int {
			return do(h, "GET", "/v1/records/anything", "", "", nil).Code
		}},
	}

	for _, p := range probes {
		code := p.exec()
		if !spec.declares(p.path, p.method, strconv.Itoa(code)) {
			t.Errorf("%s: %s %s returned %d, which the OpenAPI spec does not declare", p.name, p.method, p.path, code)
		}
	}
}

// TestOpenAPI_IdempotencyConflictDeclared confirms the 409 path is real and declared.
func TestOpenAPI_IdempotencyConflictDeclared(t *testing.T) {
	spec := loadSpec(t)
	env := setup(t, nil)
	do(env.handler, "POST", "/v1/enrichments", "tokA", "oa-dup", body("p-1", 100, 0.8, "work_email"))
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "oa-dup", body("p-2", 100, 0.8, "work_email"))
	if rec.Code != 409 {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if !spec.declares("/v1/enrichments", "post", "409") {
		t.Fatal("spec must declare 409 for POST /v1/enrichments")
	}
}
