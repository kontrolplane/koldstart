package cli

import "testing"

// TestMetricsSummary checks each series is summed whole, the 99th percentile is the highest of
// its points, and series of different lengths are read without going past their end.
func TestMetricsSummary(t *testing.T) {
	tests := []struct {
		name string
		m    Metrics
		want MetricsSummary
	}{
		{name: "empty", want: MetricsSummary{}},
		{
			name: "same lengths",
			m: Metrics{Invocations: []float64{1, 2, 3}, Errors: []float64{0, 1, 0}, Throttles: []float64{0, 0, 2},
				DurationP99: []float64{120, 480, 95}},
			want: MetricsSummary{Invocations: 6, Errors: 1, Throttles: 2, DurationP99: 480},
		},
		{
			name: "fewer errors than invocations",
			m:    Metrics{Invocations: []float64{1, 2, 3}, Errors: []float64{4}},
			want: MetricsSummary{Invocations: 6, Errors: 4},
		},
		{
			name: "more points than invocations",
			m:    Metrics{Invocations: []float64{1}, Throttles: []float64{1, 1}, DurationP99: []float64{10, 300}},
			want: MetricsSummary{Invocations: 1, Throttles: 2, DurationP99: 300},
		},
	}
	for _, tt := range tests {
		if got := tt.m.Summary(); got != tt.want {
			t.Errorf("%s: Summary = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
