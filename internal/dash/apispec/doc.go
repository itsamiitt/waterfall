package apispec

// This file makes internal/dash/apispec a buildable (non-test-only) package so `go build ./...`
// and `go vet ./...` include it. The package carries no runtime code: its whole purpose is the
// OI-API-1 OpenAPI parity contract in parity_test.go, which pins
// docs/waterfall-dashboard/openapi-admin.json at parity with the mounted /v1/admin mux routes.
