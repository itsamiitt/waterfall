package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Query is one search request.
type Query struct {
	Text  string
	Count int // desired hit count; 0 ⇒ the provider default
}

// Hit is one search result — DISCOVERY only (URL + text), never a canonical Field value.
type Hit struct {
	Title   string
	URL     string
	Snippet string
}

// Result is a completed search.
type Result struct {
	Provider string
	Hits     []Hit
}

// Client performs one bounded search through the egress *http.Client (AuthInjector transport). It
// holds NO secret; a per-provider circuit breaker (G3) trips on ill-health. Safe for concurrent use.
type Client struct {
	HTTP   *http.Client        // the egress client (AuthInjector transport). Required in production.
	Policy provider.CallPolicy // bounded budget per call; {20s, 1} when zero.
	now    func() time.Time

	mu       sync.Mutex
	breakers map[string]*provider.Breaker
}

var errBreakerOpen = errors.New("search circuit breaker open")

// NewClient builds a search client over the egress HTTP client.
func NewClient(egress *http.Client) *Client {
	return &Client{HTTP: egress, now: time.Now, breakers: map[string]*provider.Breaker{}}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) breakerFor(slug string) *provider.Breaker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.breakers == nil {
		c.breakers = map[string]*provider.Breaker{}
	}
	if br, ok := c.breakers[slug]; ok {
		return br
	}
	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}
	br := provider.NewBreaker(5, 30*time.Second, nowFn)
	c.breakers[slug] = br
	return br
}

// Search runs one bounded, breaker-guarded query against p. G3: hard timeout + breaker. The secret
// stays at the egress boundary (provider.WithAuthDescriptor). Errors are the classified taxonomy.
func (c *Client) Search(ctx context.Context, p Provider, q Query) (Result, error) {
	pol := c.Policy
	if pol.Timeout <= 0 {
		pol = provider.CallPolicy{Timeout: 20 * time.Second, MaxAttempts: 1}
	}
	br := c.breakerFor(p.Slug)
	if !br.Allow() {
		return Result{}, domain.NewProviderError(p.Slug, domain.ClassProviderDown, errBreakerOpen)
	}

	httpReq, err := buildSearch(ctx, p, q)
	if err != nil {
		return Result{}, domain.NewProviderError(p.Slug, domain.ClassBadRequest, err)
	}
	if p.Auth.KeyPoolSelector != "" {
		httpReq = httpReq.WithContext(provider.WithAuthDescriptor(httpReq.Context(), p.Auth))
	}
	cctx, cancel := context.WithTimeout(httpReq.Context(), pol.Timeout)
	defer cancel()
	httpReq = httpReq.WithContext(cctx)

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		class := domain.ClassProviderDown
		switch {
		case errors.Is(err, provider.ErrSSRFBlocked):
			class = domain.ClassBadRequest
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			class = domain.ClassTransient
		}
		if class == domain.ClassTransient || class == domain.ClassProviderDown {
			br.RecordFailure()
		}
		return Result{}, domain.NewProviderError(p.Slug, class, err)
	}
	defer resp.Body.Close()

	if class, ok := provider.ClassifyStatus(resp.StatusCode); !ok {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if class == domain.ClassRateLimit || class == domain.ClassTransient || class == domain.ClassProviderDown {
			br.RecordFailure()
		}
		return Result{}, domain.NewProviderError(p.Slug, class,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(b)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		br.RecordFailure()
		return Result{}, domain.NewProviderError(p.Slug, domain.ClassTransient, err)
	}
	hits, err := parseSearch(p, body)
	if err != nil {
		return Result{}, domain.NewProviderError(p.Slug, domain.ClassBadRequest, err)
	}
	br.RecordSuccess()
	return Result{Provider: p.Slug, Hits: hits}, nil
}

// buildSearch constructs the wire request for the provider's dialect. It sets only NON-secret
// headers; the credential is injected at egress.
func buildSearch(ctx context.Context, p Provider, q Query) (*http.Request, error) {
	switch p.Dialect {
	case DialectBrave:
		u, err := url.Parse(p.BaseURL)
		if err != nil {
			return nil, err
		}
		vals := u.Query()
		vals.Set("q", q.Text)
		if q.Count > 0 {
			vals.Set("count", strconv.Itoa(q.Count))
		}
		u.RawQuery = vals.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		return req, nil
	case DialectTavily:
		body := map[string]any{"query": q.Text}
		if q.Count > 0 {
			body["max_results"] = q.Count
		}
		return jsonPost(ctx, p.BaseURL, body)
	case DialectSerper:
		body := map[string]any{"q": q.Text}
		if q.Count > 0 {
			body["num"] = q.Count
		}
		return jsonPost(ctx, p.BaseURL, body)
	default:
		return nil, fmt.Errorf("collect: unknown search dialect %d", p.Dialect)
	}
}

func jsonPost(ctx context.Context, endpoint string, body map[string]any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// parseSearch decodes a 2xx body for the provider's dialect into discovery Hits.
func parseSearch(p Provider, body []byte) ([]Hit, error) {
	switch p.Dialect {
	case DialectBrave:
		var r struct {
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hits := make([]Hit, 0, len(r.Web.Results))
		for _, x := range r.Web.Results {
			hits = append(hits, Hit{Title: x.Title, URL: x.URL, Snippet: x.Description})
		}
		return hits, nil
	case DialectTavily:
		var r struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hits := make([]Hit, 0, len(r.Results))
		for _, x := range r.Results {
			hits = append(hits, Hit{Title: x.Title, URL: x.URL, Snippet: x.Content})
		}
		return hits, nil
	case DialectSerper:
		var r struct {
			Organic []struct {
				Title   string `json:"title"`
				Link    string `json:"link"`
				Snippet string `json:"snippet"`
			} `json:"organic"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		hits := make([]Hit, 0, len(r.Organic))
		for _, x := range r.Organic {
			hits = append(hits, Hit{Title: x.Title, URL: x.Link, Snippet: x.Snippet})
		}
		return hits, nil
	default:
		return nil, fmt.Errorf("collect: unknown search dialect %d", p.Dialect)
	}
}
