package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Server is the HTTP gateway. It owns no provider secrets and no tenant state beyond the
// stores it delegates to.
type Server struct {
	Auth        Authenticator
	Limiter     *RateLimiter
	Dispatcher  *job.Dispatcher             // runs jobs (sync path + owns the async workers)
	Submitter   job.Submitter               // async submission (in-process queue OR durable outbox)
	Jobs        job.Store                   // job state reads (tenant-scoped)
	Records     store.FieldVersions         // read model for GET /records
	DeadLetters DeadLetterAdmin             // optional; enables the /v1/dead-letters routes when set
	Metrics     *metrics.Registry           // optional; enables /metrics + RED instrumentation
	WriteScope  string                      // optional; if set, write routes require this JWT scope (403 otherwise)
	ReadyCheck  func(context.Context) error // optional; /readyz is 200 only when this returns nil
	// ShouldClaim gates admission of NEW work (T5a / OI-P5-2 drain-gating). When set and it returns
	// false, the worker is draining: job submissions (sync + async) and dead-letter redrives are
	// refused with 503 {"error":{"code":"draining"}} + Retry-After, so the worker stops claiming new
	// work while in-flight jobs — already admitted, holding leased keys + reserved credits — finish.
	// Reads (GET job/records/dead-letters) are never gated. nil (default) always admits, so existing
	// behavior is unchanged. The orchestrator wires the heartbeat client's ShouldClaim here
	// (srv.ShouldClaim = heartbeatClient.ShouldClaim).
	ShouldClaim func() bool
	Now         func() time.Time
	Logger      *slog.Logger

	reqCount     *metrics.Counter
	reqDur       *metrics.Histogram
	redriveCount *metrics.Counter
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Handler builds the routed http.Handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.Metrics != nil {
		s.reqCount = s.Metrics.Counter("http_requests_total", "HTTP requests by route, method, status.", "route", "method", "status")
		s.reqDur = s.Metrics.Histogram("http_request_duration_seconds", "HTTP request latency by route.",
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}, "route")
		mux.Handle("GET /metrics", s.Metrics.Handler())
	}
	submit := s.gateDrain(s.submit)
	if s.WriteScope != "" {
		submit = s.requireScope(s.WriteScope, submit)
	}
	mux.Handle("GET /healthz", s.instrument("/healthz", s.health))
	mux.Handle("GET /readyz", s.instrument("/readyz", s.ready))
	mux.Handle("POST /v1/enrichments", s.instrument("/v1/enrichments", s.protected(submit)))
	mux.Handle("GET /v1/enrichments/{id}", s.instrument("/v1/enrichments/{id}", s.protected(s.getJob)))
	mux.Handle("GET /v1/records/{subjectID}", s.instrument("/v1/records/{subjectID}", s.protected(s.getRecord)))
	if s.DeadLetters != nil {
		if s.Metrics != nil {
			s.redriveCount = s.Metrics.Counter("dlq_redrive_total", "Dead-letter redrive requests that reset a parked job.")
		}
		mux.Handle("GET /v1/dead-letters", s.instrument("/v1/dead-letters", s.protected(s.getDeadLetters)))
		// Redrive is a write (re-executes a job): gate it on the same scope as submit, and on the
		// drain gate — a draining worker must not claim redriven work either.
		redrive := s.gateDrain(s.redriveDeadLetter)
		if s.WriteScope != "" {
			redrive = s.requireScope(s.WriteScope, redrive)
		}
		mux.Handle("POST /v1/dead-letters/{id}/redrive", s.instrument("/v1/dead-letters/{id}/redrive", s.protected(redrive)))
	}
	return s.recoverer(mux)
}

// statusWriter captures the response status for metrics/logging.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

// instrument records RED golden signals (rate, errors, duration) and a structured request
// log line per call. The `route` label is the path TEMPLATE (never the concrete path), so
// no record ids / PII enter labels or logs.
func (s *Server) instrument(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r)
		dur := time.Since(start).Seconds()
		if s.reqCount != nil {
			s.reqCount.Inc(route, r.Method, strconv.Itoa(sw.status))
			s.reqDur.Observe(dur, route)
		}
		s.log().Info("http_request", "method", r.Method, "route", route, "status", sw.status, "dur_ms", dur*1000)
	}
}

// protected authenticates the caller (binding the tenant principal into the context —
// G1), then applies the per-tenant rate limit, before invoking h.
func (s *Server) protected(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Auth.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credential")
			return
		}
		if s.Limiter != nil && !s.Limiter.Allow(p.TenantID) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "per-tenant rate limit exceeded")
			return
		}
		// tenant_id flows ONLY from the verified principal, never the body.
		ctx := tenant.WithPrincipal(r.Context(), p)
		h(w, r.WithContext(ctx))
	}
}

// requireScope enforces that the (already-bound) principal holds scope before invoking h.
// Runs inside protected(), so the principal is in the context. Missing scope is 403, not 401.
func (s *Server) requireScope(scope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credential")
			return
		}
		if !p.HasScope(scope) {
			writeError(w, http.StatusForbidden, "forbidden", "missing required scope")
			return
		}
		h(w, r)
	}
}

// draining reports whether the worker is draining (ShouldClaim set and returning false). nil
// ShouldClaim (the default) always admits, so the gate is inert unless the orchestrator wires it.
func (s *Server) draining() bool {
	return s.ShouldClaim != nil && !s.ShouldClaim()
}

// gateDrain refuses NEW work with 503 {"error":{"code":"draining"}} + Retry-After when the worker
// is draining (T5a / OI-P5-2). It wraps only the write/admission handlers (submit, redrive); reads
// pass through untouched. The check is per-request, so a worker that flips to draining stops
// admitting immediately while in-flight requests — already past this gate — run to completion.
func (s *Server) gateDrain(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.draining() {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "draining",
				"worker is draining and not accepting new work; retry shortly")
			return
		}
		h(w, r)
	}
}

// recoverer converts a panic in any handler into a 500 rather than crashing the process.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log().Error("panic in handler", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready is the readiness probe: 200 only when the ReadyCheck (e.g. a datastore ping) passes, so
// a load balancer routes traffic to this instance only once its dependencies are reachable. With
// no ReadyCheck (memory mode) it is always ready. Distinct from /healthz (liveness).
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.ReadyCheck != nil {
		if err := s.ReadyCheck(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	var b errorBody
	b.Error.Code = errCode
	b.Error.Message = msg
	writeJSON(w, code, b)
}
