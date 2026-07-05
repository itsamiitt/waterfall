package health

import "testing"

func TestScheduleValidate(t *testing.T) {
	cases := []struct {
		name string
		s    Schedule
		ok   bool
	}{
		{"valid", Schedule{ProviderID: "p", IntervalS: 60, JitterPct: 10}, true},
		{"valid-min", Schedule{ProviderID: "p", IntervalS: 5, JitterPct: 0}, true},
		{"valid-max", Schedule{ProviderID: "p", IntervalS: 86400, JitterPct: 100}, true},
		{"no-provider", Schedule{IntervalS: 60, JitterPct: 10}, false},
		{"interval-too-low", Schedule{ProviderID: "p", IntervalS: 4, JitterPct: 10}, false},
		{"interval-too-high", Schedule{ProviderID: "p", IntervalS: 86401, JitterPct: 10}, false},
		{"jitter-negative", Schedule{ProviderID: "p", IntervalS: 60, JitterPct: -1}, false},
		{"jitter-too-high", Schedule{ProviderID: "p", IntervalS: 60, JitterPct: 101}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := c.s.validate()
			if ok != c.ok {
				t.Fatalf("validate(%+v) ok=%v, want %v", c.s, ok, c.ok)
			}
		})
	}
}
