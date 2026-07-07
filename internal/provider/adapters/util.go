package adapters

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
)

// digitsOnly strips everything but ASCII digits (for providers that want a bare numeric phone).
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rawStr renders a JSON value of uncertain type (a field that a vendor may return as either a
// number or a quoted string) as its plain text, so an adapter can map it without a decode error.
// "" / null → "". Used for fields whose exact JSON type is UNVERIFIED.
func rawStr(r json.RawMessage) string {
	s := strings.TrimSpace(string(r))
	if s == "" || s == "null" {
		return ""
	}
	return strings.Trim(s, `"`)
}

// Shared sentinel errors for async (submit→poll) adapters (ADR-0024). Wrapped in a classified
// *domain.ProviderError by the adapter so the engine sees the right taxonomy class.
var (
	errNoJobID     = errors.New("submit returned no job id")
	errResultsGone = errors.New("job results expired or deleted")
	errNoMatch     = errors.New("no matching record for the query")
)

// itoa renders an int64 attribute (employee_count, founded_year) as its decimal string, the form
// the one-value-per-Field store keeps.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// yearOf extracts a 4-digit year prefix from an ISO date string ("2015-06-01" -> "2015"); returns
// "" when the input is shorter than 4 chars.
func yearOf(s string) string {
	if len(s) >= 4 {
		return s[:4]
	}
	return ""
}

// bareDomain reduces a URL or host to a bare domain: strips scheme, a leading "www.", and any
// path/query/fragment, lowercasing the result (e.g. "https://www.Ekohe.com/about" -> "ekohe.com").
func bareDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// phoneStatusFromType collapses a vendor's line-type word into the normalized phone_status value
// the engine stores: valid_mobile | valid_landline | valid_voip | valid_other | valid_unknown.
// Callers map explicit invalid/unreachable cases themselves before calling this (it only classifies
// a resolved line type).
func phoneStatusFromType(lineType string) string {
	switch strings.ToLower(strings.TrimSpace(lineType)) {
	case "mobile", "wireless":
		return "valid_mobile"
	case "landline", "fixed", "fixed line", "fixed_line", "fixed-line", "fixedline":
		return "valid_landline"
	case "voip", "virtual", "fixedvoip":
		return "valid_voip"
	case "":
		return "valid_unknown"
	default:
		return "valid_other"
	}
}

// classifyErrMsg maps a vendor's in-body error message (the 200-with-error-field pattern used by
// MillionVerifier, DeBounce, Clearout, …) onto the error taxonomy so the engine can act correctly:
// credit/quota wording -> QUOTA (failover the key), rate/concurrent/daily-limit -> RATE_LIMIT,
// everything else (bad/missing/expired key) -> AUTH. Order matters: credit is checked before the
// broad "limit" match.
func classifyErrMsg(msg string) domain.ErrorClass {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "credit") || strings.Contains(m, "insufficient") ||
		strings.Contains(m, "quota") || strings.Contains(m, "upgrade") || strings.Contains(m, "balance"):
		return domain.ClassQuota
	case strings.Contains(m, "concurrent") || strings.Contains(m, "per day") ||
		strings.Contains(m, "rate") || strings.Contains(m, "maximum") || strings.Contains(m, "too many"):
		return domain.ClassRateLimit
	default:
		return domain.ClassAuth
	}
}

// setIfQ sets query param k to v only when v is non-empty, so optional match inputs are omitted
// rather than sent as blank params.
func setIfQ(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

// bodyErr builds a classified *domain.ProviderError from a vendor's in-body error message (the
// 200-with-error pattern: QuickEmailVerification, MyEmailVerifier, …). The class is derived from the
// message via classifyErrMsg; a blank message falls back to a generic text so the error is never
// empty. Callers that classify by a numeric error CODE instead build the error inline.
func bodyErr(providerName, msg string) error {
	m := strings.TrimSpace(msg)
	if m == "" {
		m = "provider returned an error"
	}
	return domain.NewProviderError(providerName, classifyErrMsg(msg), errors.New(m))
}

// normList renders a multi-valued attribute (technographics, intent_topics) as the single
// normalized Observation value the one-value-per-Field store expects (ADR-0023): trimmed,
// de-duplicated, sorted, comma-joined. Blank entries are dropped; "" is returned when nothing
// remains, so the caller can omit the Field (NOT_FOUND) rather than store an empty value.
func normList(vals []string) string {
	seen := make(map[string]struct{}, len(vals))
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
