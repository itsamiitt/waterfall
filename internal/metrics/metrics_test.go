package metrics

import (
	"strings"
	"testing"
)

func render(r *Registry) string {
	var b strings.Builder
	r.Render(&b)
	return b.String()
}

func mustContain(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q\n---\n%s", want, out)
	}
}

func TestCounter(t *testing.T) {
	r := New()
	c := r.Counter("calls_total", "Total calls.", "result")
	c.Inc("ok")
	c.Add(2, "ok")
	c.Inc("err")
	out := render(r)
	mustContain(t, out, "# TYPE calls_total counter")
	mustContain(t, out, `calls_total{result="ok"} 3`)
	mustContain(t, out, `calls_total{result="err"} 1`)
}

func TestGaugeAndGaugeFunc(t *testing.T) {
	r := New()
	g := r.Gauge("inflight", "In flight.")
	g.Set(5)
	g.Add(-2)
	depth := 7
	r.GaugeFunc("queue_depth", "Queue depth.", func() float64 { return float64(depth) })
	out := render(r)
	mustContain(t, out, "inflight 3")
	mustContain(t, out, "queue_depth 7")
}

func TestHistogram(t *testing.T) {
	r := New()
	h := r.Histogram("dur_seconds", "Durations.", []float64{0.1, 1})
	h.Observe(0.05)
	h.Observe(0.5)
	out := render(r)
	mustContain(t, out, "# TYPE dur_seconds histogram")
	mustContain(t, out, `dur_seconds_bucket{le="0.1"} 1`)
	mustContain(t, out, `dur_seconds_bucket{le="1"} 2`)
	mustContain(t, out, `dur_seconds_bucket{le="+Inf"} 2`)
	mustContain(t, out, "dur_seconds_count 2")
	mustContain(t, out, "dur_seconds_sum 0.55")
}

func TestLabelEscaping(t *testing.T) {
	r := New()
	c := r.Counter("evt_total", "Events.", "msg")
	c.Inc(`a"b\c`)
	out := render(r)
	mustContain(t, out, `evt_total{msg="a\"b\\c"} 1`)
}

func TestReRegisterReturnsSameFamily(t *testing.T) {
	r := New()
	a := r.Counter("x_total", "X.")
	b := r.Counter("x_total", "X.")
	a.Inc()
	b.Inc()
	mustContain(t, render(r), "x_total 2") // both handles share the family
}
