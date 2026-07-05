package telemetry

import (
	"strconv"
	"sync/atomic"
	"time"
)

// atomicInt64 is a tiny wrapper so counters can be read in tests without scraping the metrics
// registry. It is race-safe.
type atomicInt64 struct{ v atomic.Int64 }

func (a *atomicInt64) add(n int64) { a.v.Add(n) }
func (a *atomicInt64) load() int64 { return a.v.Load() }

// appendInt appends the base-10 form of n to b.
func appendInt(b []byte, n int) []byte { return strconv.AppendInt(b, int64(n), 10) }

// s dereferences a nullable text column to a string ("" for NULL).
func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// i64 parses a nullable bigint text column (0 for NULL/garbage).
func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*p, 10, 64)
	return n
}

// parseTS parses a Postgres timestamptz (or RFC3339) text rendering into a UTC time.Time.
func parseTS(str string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, str); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
