// Package metrics is a tiny, dependency-free metrics registry that renders the Prometheus
// text exposition format (docs/20). It supports labeled Counters, Gauges, GaugeFuncs, and
// Histograms — enough for the golden signals + enrichment KPIs without pulling in a client
// library. All operations are concurrency-safe.
//
// Cardinality discipline (docs/20 §7): callers must use bounded label values (route
// templates, provider names, result classes) and MUST NOT put PII or unbounded ids
// (record ids, emails) into labels.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Registry holds metric families and renders them.
type Registry struct {
	mu     sync.Mutex
	order  []*family
	byName map[string]*family
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{byName: map[string]*family{}}
}

type family struct {
	name, help, typ string
	labelNames      []string
	buckets         []float64      // histogram only
	fn              func() float64 // gaugefunc only
	mu              sync.Mutex
	series          map[string]*sample
}

type sample struct {
	labelVals    []string
	val          float64  // counter/gauge
	bucketCounts []uint64 // histogram cumulative
	sum          float64
	count        uint64
}

func (r *Registry) register(f *family) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byName[f.name]; ok {
		return existing
	}
	f.series = map[string]*sample{}
	r.byName[f.name] = f
	r.order = append(r.order, f)
	return f
}

// Counter registers (or returns) a counter family.
func (r *Registry) Counter(name, help string, labelNames ...string) *Counter {
	return &Counter{f: r.register(&family{name: name, help: help, typ: "counter", labelNames: labelNames})}
}

// Gauge registers (or returns) a gauge family.
func (r *Registry) Gauge(name, help string, labelNames ...string) *Gauge {
	return &Gauge{f: r.register(&family{name: name, help: help, typ: "gauge", labelNames: labelNames})}
}

// GaugeFunc registers a gauge whose value is computed at scrape time (no labels).
func (r *Registry) GaugeFunc(name, help string, fn func() float64) {
	r.register(&family{name: name, help: help, typ: "gauge", fn: fn})
}

// Histogram registers (or returns) a histogram family with the given upper-bound buckets.
func (r *Registry) Histogram(name, help string, buckets []float64, labelNames ...string) *Histogram {
	return &Histogram{f: r.register(&family{name: name, help: help, typ: "histogram", labelNames: labelNames, buckets: buckets})}
}

func (f *family) get(vals []string) *sample {
	key := strings.Join(vals, "\x1f")
	s := f.series[key]
	if s == nil {
		s = &sample{labelVals: vals}
		if f.typ == "histogram" {
			s.bucketCounts = make([]uint64, len(f.buckets))
		}
		f.series[key] = s
	}
	return s
}

// Counter is a monotonically increasing metric.
type Counter struct{ f *family }

// Inc adds 1 for the given label values.
func (c *Counter) Inc(labelVals ...string) { c.Add(1, labelVals...) }

// Add adds delta (>=0) for the given label values.
func (c *Counter) Add(delta float64, labelVals ...string) {
	c.f.mu.Lock()
	c.f.get(labelVals).val += delta
	c.f.mu.Unlock()
}

// Gauge is a value that can go up or down.
type Gauge struct{ f *family }

// Set sets the gauge value.
func (g *Gauge) Set(v float64, labelVals ...string) {
	g.f.mu.Lock()
	g.f.get(labelVals).val = v
	g.f.mu.Unlock()
}

// Add adds delta (may be negative).
func (g *Gauge) Add(delta float64, labelVals ...string) {
	g.f.mu.Lock()
	g.f.get(labelVals).val += delta
	g.f.mu.Unlock()
}

// Histogram accumulates observations into cumulative buckets.
type Histogram struct{ f *family }

// Observe records one value.
func (h *Histogram) Observe(v float64, labelVals ...string) {
	h.f.mu.Lock()
	s := h.f.get(labelVals)
	s.count++
	s.sum += v
	for i, b := range h.f.buckets {
		if v <= b {
			s.bucketCounts[i]++
		}
	}
	h.f.mu.Unlock()
}

// Handler serves the metrics in Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		r.Render(w)
	})
}

// Render writes all metrics in Prometheus text exposition format.
func (r *Registry) Render(w io.Writer) {
	r.mu.Lock()
	families := append([]*family(nil), r.order...)
	r.mu.Unlock()

	for _, f := range families {
		fmt.Fprintf(w, "# HELP %s %s\n", f.name, f.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", f.name, f.typ)
		if f.fn != nil {
			fmt.Fprintf(w, "%s %s\n", f.name, formatFloat(f.fn()))
			continue
		}
		f.mu.Lock()
		for _, s := range f.sorted() {
			base := labelString(f.labelNames, s.labelVals, "", "")
			switch f.typ {
			case "histogram":
				for i, b := range f.buckets {
					fmt.Fprintf(w, "%s_bucket%s %d\n", f.name,
						labelString(f.labelNames, s.labelVals, "le", formatFloat(b)), s.bucketCounts[i])
				}
				fmt.Fprintf(w, "%s_bucket%s %d\n", f.name,
					labelString(f.labelNames, s.labelVals, "le", "+Inf"), s.count)
				fmt.Fprintf(w, "%s_sum%s %s\n", f.name, base, formatFloat(s.sum))
				fmt.Fprintf(w, "%s_count%s %d\n", f.name, base, s.count)
			default:
				fmt.Fprintf(w, "%s%s %s\n", f.name, base, formatFloat(s.val))
			}
		}
		f.mu.Unlock()
	}
}

func (f *family) sorted() []*sample {
	out := make([]*sample, 0, len(f.series))
	for _, s := range f.series {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].labelVals, "\x1f") < strings.Join(out[j].labelVals, "\x1f")
	})
	return out
}

func labelString(names, vals []string, extraName, extraVal string) string {
	if len(names) == 0 && extraName == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for i, n := range names {
		if i >= len(vals) {
			break
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escape(vals[i]))
		b.WriteString(`"`)
	}
	if extraName != "" {
		if !first {
			b.WriteByte(',')
		}
		b.WriteString(extraName)
		b.WriteString(`="`)
		b.WriteString(escape(extraVal))
		b.WriteString(`"`)
	}
	b.WriteByte('}')
	return b.String()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
