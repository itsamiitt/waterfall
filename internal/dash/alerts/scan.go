package alerts

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// --- row scanners (columns match the SELECT lists in store.go) ---

func scanRule(r []*string) Rule {
	return Rule{
		ID:         str(r[0]),
		Name:       str(r[1]),
		Metric:     str(r[2]),
		Scope:      parseScope(str(r[3])),
		Op:         str(r[4]),
		Threshold:  f64(r[5]),
		WindowS:    int(i64(r[6])),
		CooldownS:  int(i64(r[7])),
		Severity:   str(r[8]),
		Channels:   parseUUIDArray(str(r[9])),
		Enabled:    boolOf(r[10]),
		MutedUntil: tsPtr(r[11]),
		CreatedBy:  str(r[12]),
		UpdatedAt:  parseTS(str(r[13])),
	}
}

func scanChannel(r []*string) Channel {
	return Channel{
		ID:               str(r[0]),
		Kind:             str(r[1]),
		Name:             str(r[2]),
		ConfigEnvelopeID: str(r[3]),
		Status:           str(r[4]),
		CreatedAt:        parseTS(str(r[5])),
	}
}

func scanEvent(r []*string) Event {
	return Event{
		ID:         i64(r[0]),
		RuleID:     str(r[1]),
		State:      str(r[2]),
		Value:      f64Ptr(r[3]),
		FiredAt:    parseTS(str(r[4])),
		ResolvedAt: tsPtr(r[5]),
		NotifiedAt: tsPtr(r[6]),
		AckBy:      str(r[7]),
		AckAt:      tsPtr(r[8]),
		DedupeKey:  str(r[9]),
	}
}

// --- scope <-> jsonb ---

// scopeJSON renders a scope map as a jsonb parameter (nil map -> SQL NULL).
func scopeJSON(scope map[string]string) any {
	if len(scope) == 0 {
		return nil
	}
	b, err := json.Marshal(scope)
	if err != nil {
		return nil
	}
	return string(b)
}

func parseScope(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// --- uuid[] literal helpers ---

// uuidArrayLiteral renders []string as a Postgres array literal '{a,b}' cast with ::uuid[]. An
// empty slice yields '{}' (an empty array, not NULL).
func uuidArrayLiteral(xs []string) string {
	if len(xs) == 0 {
		return "{}"
	}
	return "{" + strings.Join(xs, ",") + "}"
}

// uuidArrayOrNull returns a '{...}' literal or nil (for coalesce on PATCH: nil = unchanged).
func uuidArrayOrNull(xs []string) any {
	if xs == nil {
		return nil
	}
	return uuidArrayLiteral(xs)
}

func parseUUIDArray(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s == "{}" {
		return nil
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	var out []string
	for _, tok := range strings.Split(s, ",") {
		tok = strings.Trim(strings.TrimSpace(tok), "\"")
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// --- nullable column helpers ---

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*p, 10, 64)
	return n
}

func f64(p *string) float64 {
	if p == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(*p, 64)
	return v
}

func f64Ptr(p *string) *float64 {
	if p == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*p, 64)
	if err != nil {
		return nil
	}
	return &v
}

func boolOf(p *string) bool {
	return p != nil && (*p == "t" || *p == "true")
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func tsOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func tsPtr(p *string) *time.Time {
	if p == nil {
		return nil
	}
	t := parseTS(*p)
	if t.IsZero() {
		return nil
	}
	return &t
}

func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999-07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
