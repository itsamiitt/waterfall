package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// AsyncHTTPAdapter is a multi-round-trip provider adapter (ADR-0024 Phase 3) for providers whose
// enrichment is not a single request/response:
//
//   - submit→poll: submit a job, receive a token/id, poll a status endpoint until the result is
//     ready (Dropcontact, Icypeas, Enrow, Wiza, SignalHire, BetterContact, Verifalia batch, …).
//   - match→fetch: resolve an identifier first (e.g. D&B cleanseMatch → DUNS), then fetch data by
//     that id (D&B data blocks, Explorium, Endole). This is the degenerate case where the first
//     poll IS the fetch and Decode returns done=true immediately.
//
// Like HTTPAdapter it holds NO secret — each round-trip carries only the AuthDescriptor and the
// egress AuthInjector places the credential (including oauth2-cc token exchange, Phase 2). It
// implements PolicyOverrider (Phase 1) so the engine grants it a longer BOUNDED budget; the poll
// loop honours ctx cancellation/deadline on every sleep and never sleeps past ctx.Done(), so G3
// still holds (bounded + breaker + capped-retry wrap this in provider.Call).
type AsyncHTTPAdapter struct {
	NameV   string
	BaseURL string
	Client  *http.Client
	Auth    AuthDescriptor
	Caps    []Capability
	// Policy is the bounded budget for the whole submit+poll sequence. When its Timeout is 0 a
	// conservative async default (60s, single attempt) is used.
	Policy CallPolicy

	// Submit builds the job-submission (or match) request against BaseURL.
	Submit func(ctx context.Context, base string, req Request) (*http.Request, error)
	// ParseSubmit extracts the poll token (job id / DUNS / status URL) from the submit 2xx body.
	// It may return an already-classified *domain.ProviderError for a 200-with-error body.
	ParseSubmit func(body []byte) (pollToken string, err error)
	// Poll builds the status/fetch request from the poll token.
	Poll func(ctx context.Context, base, pollToken string) (*http.Request, error)
	// Decode maps a poll 2xx body into a Result and a done flag. done=false means "still pending,
	// keep polling"; done=true means terminal. It may return a classified *domain.ProviderError.
	Decode func(body []byte) (res Result, done bool, err error)
	// PollInterval is the wait between polls (ctx-aware). Defaults to 2s when zero.
	PollInterval time.Duration
}

func (h *AsyncHTTPAdapter) Name() string               { return h.NameV }
func (h *AsyncHTTPAdapter) Capabilities() []Capability { return h.Caps }

// Base and AuthDescriptor satisfy Introspectable (used by the registry seeder / SSRF allow-list).
func (h *AsyncHTTPAdapter) Base() string                   { return h.BaseURL }
func (h *AsyncHTTPAdapter) AuthDescriptor() AuthDescriptor { return h.Auth }

// CallPolicy implements PolicyOverrider — an async sequence needs a longer bounded budget than the
// synchronous 3s default, and MaxAttempts=1 (the internal loop handles polling; the engine must not
// re-run the whole submit+poll on a transient).
func (h *AsyncHTTPAdapter) CallPolicy() CallPolicy {
	if h.Policy.Timeout > 0 {
		return h.Policy
	}
	return CallPolicy{Timeout: 60 * time.Second, MaxAttempts: 1}
}

func (h *AsyncHTTPAdapter) httpClient() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

func (h *AsyncHTTPAdapter) Fetch(ctx context.Context, req Request) (Result, error) {
	// 1. Submit the job / match request.
	subReq, err := h.Submit(ctx, h.BaseURL, req)
	if err != nil {
		return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
	}
	body, err := h.roundTrip(subReq)
	if err != nil {
		return Result{}, err
	}
	token, err := h.ParseSubmit(body)
	if err != nil {
		return Result{}, h.classifyDecodeErr(err)
	}

	// 2. Poll until Decode reports done, or the bounded ctx expires.
	interval := h.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		pollReq, err := h.Poll(ctx, h.BaseURL, token)
		if err != nil {
			return Result{}, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
		}
		pbody, err := h.roundTrip(pollReq)
		if err != nil {
			return Result{}, err
		}
		res, done, err := h.Decode(pbody)
		if err != nil {
			return Result{}, h.classifyDecodeErr(err)
		}
		if done {
			return res, nil
		}
		// Wait before the next poll — but never past the caller's deadline.
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, domain.NewProviderError(h.NameV, domain.ClassTransient, ctx.Err())
		case <-timer.C:
		}
	}
}

// roundTrip attaches the auth descriptor, performs one request, classifies the status, and returns
// the body — mirroring HTTPAdapter.Fetch's error taxonomy so async providers behave identically.
func (h *AsyncHTTPAdapter) roundTrip(httpReq *http.Request) ([]byte, error) {
	if h.Auth.KeyPoolSelector != "" {
		httpReq = httpReq.WithContext(withAuthDescriptor(httpReq.Context(), h.Auth))
	}
	resp, err := h.httpClient().Do(httpReq)
	if err != nil {
		if errors.Is(err, ErrSSRFBlocked) {
			return nil, domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, domain.NewProviderError(h.NameV, domain.ClassTransient, err)
		}
		return nil, domain.NewProviderError(h.NameV, domain.ClassProviderDown, err)
	}
	defer resp.Body.Close()
	if class, ok := classifyStatus(resp.StatusCode); !ok {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, domain.NewProviderError(h.NameV, class, fmt.Errorf("status %d: %s", resp.StatusCode, string(b)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, domain.NewProviderError(h.NameV, domain.ClassTransient, err)
	}
	return body, nil
}

// classifyDecodeErr preserves an already-classified *domain.ProviderError from ParseSubmit/Decode
// (a 200-with-error body → AUTH/QUOTA/…) and otherwise treats it as a BAD_REQUEST decode failure —
// exactly as HTTPAdapter.Fetch does.
func (h *AsyncHTTPAdapter) classifyDecodeErr(err error) error {
	var pe *domain.ProviderError
	if errors.As(err, &pe) {
		return pe
	}
	return domain.NewProviderError(h.NameV, domain.ClassBadRequest, err)
}
