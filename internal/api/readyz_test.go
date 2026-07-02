package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/enrichment/waterfall/internal/api"
)

func TestReadyz(t *testing.T) {
	cases := []struct {
		name  string
		check func(context.Context) error
		want  int
	}{
		{"no check is always ready", nil, 200},
		{"passing check is ready", func(context.Context) error { return nil }, 200},
		{"failing check is unavailable", func(context.Context) error { return errors.New("db down") }, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := (&api.Server{ReadyCheck: tc.check}).Handler()
			if rec := do(h, "GET", "/readyz", "", "", nil); rec.Code != tc.want {
				t.Fatalf("/readyz: got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
