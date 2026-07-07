package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/enrichment/waterfall/internal/domain"
)

// HTTPAdapter is a generic API-first provider adapter: it makes a real HTTP request to a
// provider endpoint and maps the response onto a normalized Result. It is the concrete
// realisation of the "no scraping, provider APIs only" constraint (ADR-0002).
//
// Secret containment: the adapter sets the auth HEADER NAME from its AuthDescriptor but
// deliberately does NOT put a secret value in it — in the real system the egress-proxy
// (docs/13 §3) injects the leased key as the request leaves the trust boundary. Here the
// Client's Transport is that injection seam.
type HTTPAdapter struct {
	NameV   string
	BaseURL string
	Client  *http.Client
	Auth    AuthDescriptor
	Caps    []Capability
	// Build turns a Request into an HTTP request against BaseURL. If nil, a GET to
	// BaseURL is used.
	Build func(ctx context.Context, base string, req Request) (*http.Request, error)
	// Decode maps a 2xx response body into a Result.
	Decode func(body []byte) (Result, error)
	// Policy, when non-nil, overrides the engine's default CallPolicy for this adapter
	// (ADR-0024 Phase 1) — e.g. a longer bounded budget for a slow provider. Nil leaves the
	// engine default in force, so existing adapters are unaffected.
	Policy *CallPolicy
}

func (h *HTTPAdapter) Name() string               { return h.NameV }
func (h *HTTPAdapter) Capabilities() []Capability { return h.Caps }

// Base and AuthDescriptor satisfy Introspectable (used by the registry seeder / SSRF allow-list).
func (h *HTTPAdapter) Base() string                   { return h.BaseURL }
func (h *HTTPAdapter) AuthDescriptor() AuthDescriptor { return h.Auth }

// CallPolicy implements PolicyOverrider. It returns h.Policy when set, else the zero policy
// (Timeout==0) which the engine reads as "no override — use my default".
func (h *HTTPAdapter) CallPolicy() CallPolicy {
	if h.Policy != nil {
		return *h.Policy
	}
	return CallPolicy{}
}

func (h *HTTPAdapter) Fetch(ctx context.Context, req Request) (Result, error) {
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	build := h.Build
	if build == nil {
		build = func(ctx context.Context, base string, _ Request) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
		}
	}
	httpReq, err := build(ctx, h.BaseURL, req)
	if err != nil {
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
	}
	// Attach the auth descriptor so the egress AuthInjector can place the credential as
	// the request leaves — the adapter itself never holds or sets the secret.
	if h.Auth.KeyPoolSelector != "" {
		httpReq = httpReq.WithContext(withAuthDescriptor(httpReq.Context(), h.Auth))
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		// An SSRF policy refusal is a non-retryable BAD_REQUEST, not a transient fault.
		if errors.Is(err, ErrSSRFBlocked) {
			return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
		}
		// Transport error (includes ctx deadline) — transient against this provider.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return Result{}, domain.NewProviderError(h.NameV, domain.ClassTransient, err)
		}
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassProviderDown, err)
	}
	defer resp.Body.Close()

	if class, ok := classifyStatus(resp.StatusCode); !ok {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Result{}, domain.NewProviderError(h.NameV, class,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassTransient, err)
	}
	if h.Decode == nil {
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest,
			errors.New("adapter has no Decode function"))
	}
	res, err := h.Decode(body)
	if err != nil {
		// Many providers return a 2xx with an in-body error flag (e.g. ZeroBounce/BuiltWith signal
		// a bad key or exhausted credits as HTTP 200 + {"error":...}). Such a Decode returns an
		// already-classified *domain.ProviderError (AUTH/QUOTA/…) that must be preserved so the
		// engine can failover the key rather than treating it as a generic BAD_REQUEST. A plain
		// decode failure (malformed JSON) still maps to BAD_REQUEST.
		var pe *domain.ProviderError
		if errors.As(err, &pe) {
			return Result{}, pe
		}
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
	}
	return res, nil
}

// classifyStatus maps HTTP status codes onto the error taxonomy
// (skills/api-integration). ok=true means "treat as success, decode body".
func classifyStatus(code int) (domain.ErrorClass, bool) {
	switch {
	case code >= 200 && code < 300:
		return domain.ClassUnknown, true
	case code == http.StatusUnauthorized: // 401
		return domain.ClassAuth, false
	case code == http.StatusPaymentRequired: // 402 -> credits exhausted
		return domain.ClassQuota, false
	case code == http.StatusLocked: // 423 -> account/subscription locked or paused (e.g. Findymail)
		return domain.ClassQuota, false
	case code == http.StatusForbidden: // 403 — some providers (Hunter) use this to throttle
		return domain.ClassRateLimit, false
	case code == http.StatusNotFound: // 404 — no data for this subject
		return domain.ClassNotFound, false
	case code == http.StatusTooManyRequests: // 429
		return domain.ClassRateLimit, false
	case code == http.StatusBadRequest, code == http.StatusUnprocessableEntity: // 400/422
		return domain.ClassBadRequest, false
	case code == http.StatusBadGateway, code == http.StatusServiceUnavailable, code == http.StatusGatewayTimeout:
		return domain.ClassProviderDown, false
	case code >= 500:
		return domain.ClassTransient, false
	default:
		return domain.ClassTransient, false
	}
}
