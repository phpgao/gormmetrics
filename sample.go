package gormmetrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// MetricType enumerates the Prometheus metric shapes this library knows how
// to emit. Histograms must additionally supply Sample.Buckets and
// Sample.HistogramObs (a histogram sample is an entire histogram payload,
// not a single observation).
type MetricType int

const (
	// Gauge is a point-in-time numeric value that can go up or down.
	Gauge MetricType = iota
	// Counter is a monotonically increasing value. Emit the absolute,
	// server-reported value — Prometheus handles rate() and counter resets.
	Counter
	// Histogram is a pre-bucketed distribution. Use HistogramSample helper
	// to construct.
	Histogram
)

func (m MetricType) String() string {
	switch m {
	case Gauge:
		return "gauge"
	case Counter:
		return "counter"
	case Histogram:
		return "histogram"
	}
	return "unknown"
}

// Sample is a single metric reading produced by a Scraper. It is converted
// to a prometheus.Metric at /metrics serving time.
//
// For Gauge / Counter:
//   - Value is the data point
//   - HistogramBuckets / HistogramCount / HistogramSum are ignored
//
// For Histogram:
//   - Value is ignored
//   - HistogramBuckets maps "le" upper bound -> cumulative count
//   - HistogramCount is the total observation count
//   - HistogramSum is the sum of all observations
//
// Labels keys must already be valid Prometheus label names. They are merged
// with the Collector's const labels at conversion time; on conflict the
// per-sample label wins (allowing scrapers to override e.g. db_name).
type Sample struct {
	Name   string
	Help   string
	Type   MetricType
	Value  float64
	Labels map[string]string

	HistogramBuckets map[float64]uint64
	HistogramCount   uint64
	HistogramSum     float64
}

// toPromMetric materialises a Sample as a prometheus.Metric using the
// supplied descriptor and label-value ordering. The caller is responsible
// for ensuring desc and labelKeys agree.
func (s Sample) toPromMetric(desc *prometheus.Desc, labelValues []string) (prometheus.Metric, error) {
	switch s.Type {
	case Gauge:
		return prometheus.NewConstMetric(desc, prometheus.GaugeValue, s.Value, labelValues...)
	case Counter:
		return prometheus.NewConstMetric(desc, prometheus.CounterValue, s.Value, labelValues...)
	case Histogram:
		return prometheus.NewConstHistogram(desc, s.HistogramCount, s.HistogramSum, s.HistogramBuckets, labelValues...)
	}
	return nil, errUnknownType{t: s.Type}
}

type errUnknownType struct{ t MetricType }

func (e errUnknownType) Error() string { return "gormmetrics: unknown MetricType " + e.t.String() }
