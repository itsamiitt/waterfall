package overview

import (
	"strconv"
	"strings"
)

// --- nullable text column helpers (local copies; no cross-feature import) ---

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

// parseBigintArray parses a Postgres bigint[] text literal '{n1,n2,...}' into []int64.
func parseBigintArray(s string) []int64 {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s == "{}" {
		return nil
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	var out []int64
	for _, tok := range strings.Split(s, ",") {
		n, _ := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
		out = append(out, n)
	}
	return out
}
