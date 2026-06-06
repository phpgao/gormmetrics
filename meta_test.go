package gormmetrics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// collectAndRead calls m.collect into a buffered channel and returns all metrics.
func collectAndRead(m *metaMetrics) []prometheus.Metric {
	ch := make(chan prometheus.Metric, 64)
	m.collect(ch)
	close(ch)
	var ms []prometheus.Metric
	for mc := range ch {
		ms = append(ms, mc)
	}
	return ms
}

// fqName extracts the fully-qualified metric name from a prometheus.Desc string.
func fqName(m prometheus.Metric) string {
	s := m.Desc().String()
	prefix := `fqName: "`
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	i += len(prefix)
	j := strings.Index(s[i:], `"`)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// metricLabels returns the label pairs of a ConstMetric.
func metricLabels(m prometheus.Metric) []*dto.LabelPair {
	d := &dto.Metric{}
	_ = m.Write(d)
	return d.GetLabel()
}

// metricValue returns the Gauge/Counter value of a ConstMetric.
func metricValue(m prometheus.Metric) float64 {
	d := &dto.Metric{}
	_ = m.Write(d)
	if g := d.GetGauge(); g != nil {
		return g.GetValue()
	}
	if c := d.GetCounter(); c != nil {
		return c.GetValue()
	}
	return -1
}

// findMetric returns a metric by fqName and label match. labelWant can be nil
// to match any metric with the given name.
func findMetric(ms []prometheus.Metric, name string, labelWant map[string]string) prometheus.Metric {
	for _, m := range ms {
		if fqName(m) != name {
			continue
		}
		if labelWant == nil {
			return m
		}
		if labelsMatch(m, labelWant) {
			return m
		}
	}
	return nil
}

func labelsMatch(m prometheus.Metric, want map[string]string) bool {
	got := make(map[string]string, len(want))
	for _, lp := range metricLabels(m) {
		got[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func requireMetric(t *testing.T, ms []prometheus.Metric, name string, labels map[string]string, value float64) {
	t.Helper()
	m := findMetric(ms, name, labels)
	if m == nil {
		t.Errorf("metric %s%v not found", name, labels)
		return
	}
	got := metricValue(m)
	if got != value {
		t.Errorf("metric %s%v = %v, want %v", name, labels, got, value)
	}
}

// --- Tests ---

func TestNewMetaMetricsNamespacePrefix(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	ch := make(chan *prometheus.Desc, 6)
	m.describe(ch)
	close(ch)
	var names []string
	for d := range ch {
		names = append(names, d.String())
	}
	for _, want := range []string{`"gormmetrics_up"`, `"gormmetrics_scrape_success"`} {
		found := false
		for _, n := range names {
			if strings.Contains(n, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected descriptor containing %s", want)
		}
	}
}

func TestNewMetaMetricsWithNamespace(t *testing.T) {
	m := newMetaMetrics("myapp", nil, nil)
	ch := make(chan *prometheus.Desc, 6)
	m.describe(ch)
	close(ch)
	var names []string
	for d := range ch {
		names = append(names, d.String())
	}
	for _, want := range []string{`"myapp_gormmetrics_up"`, `"myapp_gormmetrics_scrape_success"`} {
		found := false
		for _, n := range names {
			if strings.Contains(n, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected descriptor containing %s", want)
		}
	}
}

func TestMetaMetricsConstLabels(t *testing.T) {
	m := newMetaMetrics("", map[string]string{"cluster": "eu-1"}, nil)
	ch := make(chan *prometheus.Desc, 6)
	m.describe(ch)
	close(ch)
	for d := range ch {
		if !strings.Contains(d.String(), `cluster="eu-1"`) {
			t.Errorf("expected const label cluster=eu-1 in %s", d.String())
		}
	}
}

func TestMetaMetricsRecordUp(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_up", nil, 0)

	m.recordUp(true)
	ms = collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_up", nil, 1)

	m.recordUp(false)
	ms = collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_up", nil, 0)
}

func TestMetaMetricsRecordScrapeSuccess(t *testing.T) {
	m := newMetaMetrics("", nil, nil)

	m.recordScrape("scraper_a", scrapeOutcome{
		samples:  []Sample{{Name: "x", Type: Gauge}, {Name: "y", Type: Gauge}},
		duration: 100 * time.Millisecond,
	})

	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scrape_success", map[string]string{"scraper": "scraper_a"}, 1)
	requireMetric(t, ms, "gormmetrics_scrape_duration_seconds", map[string]string{"scraper": "scraper_a"}, 0.1)
	requireMetric(t, ms, "gormmetrics_scrape_samples", map[string]string{"scraper": "scraper_a"}, 2)
}

func TestMetaMetricsRecordScrapeError(t *testing.T) {
	m := newMetaMetrics("", nil, nil)

	m.recordScrape("scraper_b", scrapeOutcome{
		err:      errors.New("Access denied for user 'app'@'%'"),
		duration: 5 * time.Millisecond,
	})

	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scrape_success", map[string]string{"scraper": "scraper_b"}, 0)
	requireMetric(t, ms, "gormmetrics_scrape_errors",
		map[string]string{"scraper": "scraper_b", "error": "permission_denied"}, 1)
}

func TestMetaMetricsRecordScrapeCustomClassifier(t *testing.T) {
	m := newMetaMetrics("", nil, func(err error) string { return "custom_bucket" })

	m.recordScrape("scraper_c", scrapeOutcome{err: errors.New("some error")})

	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scrape_errors",
		map[string]string{"scraper": "scraper_c", "error": "custom_bucket"}, 1)
}

func TestMetaMetricsCustomClassifierFallback(t *testing.T) {
	// Custom classifier returning "" falls back to classifyErr.
	m := newMetaMetrics("", nil, func(err error) string { return "" })

	m.recordScrape("scraper_d", scrapeOutcome{err: errors.New("context deadline exceeded")})

	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scrape_errors",
		map[string]string{"scraper": "scraper_d", "error": "timeout"}, 1)
}

func TestMetaMetricsErrorCountAccumulates(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	for i := 0; i < 3; i++ {
		m.recordScrape("scraper_e", scrapeOutcome{err: errors.New("context deadline exceeded")})
	}
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scrape_errors",
		map[string]string{"scraper": "scraper_e", "error": "timeout"}, 3)
}

func TestMetaMetricsObserveDisabled(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	m.observeDisabled("scraper_x", errors.New("Access denied: PROCESS required"))
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scraper_disabled",
		map[string]string{"scraper": "scraper_x", "reason": "permission_denied"}, 1)
}

func TestMetaMetricsObserveDisabledCustomClassifier(t *testing.T) {
	m := newMetaMetrics("", nil, func(err error) string { return "custom_reason" })
	m.observeDisabled("scraper_y", errors.New("ACCESS DENIED"))
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scraper_disabled",
		map[string]string{"scraper": "scraper_y", "reason": "custom_reason"}, 1)
}

func TestMetaMetricsRecordDisabledFirstTime(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	m.recordDisabled("scraper_z")
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_scraper_disabled",
		map[string]string{"scraper": "scraper_z", "reason": "(unknown)"}, 1)
}

func TestMetaMetricsRecordDisabledIdempotent(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	m.observeDisabled("scraper_z", errors.New("permission denied"))
	m.recordDisabled("scraper_z")

	ms := collectAndRead(m)
	// Still permission_denied, not overwritten by "(unknown)"
	requireMetric(t, ms, "gormmetrics_scraper_disabled",
		map[string]string{"scraper": "scraper_z", "reason": "permission_denied"}, 1)

	// Confirm "(unknown)" is absent
	if m := findMetric(ms, "gormmetrics_scraper_disabled",
		map[string]string{"scraper": "scraper_z", "reason": "(unknown)"}); m != nil {
		t.Error("(unknown) should not overwrite existing reason")
	}
}

func TestMetaMetricsCollectEmpty(t *testing.T) {
	m := newMetaMetrics("", nil, nil)
	ms := collectAndRead(m)
	requireMetric(t, ms, "gormmetrics_up", nil, 0)
}

func TestMetaMetricsDescribeCount(t *testing.T) {
	m := newMetaMetrics("test", map[string]string{"env": "dev"}, nil)
	ch := make(chan *prometheus.Desc, 6)
	m.describe(ch)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	if count != 6 {
		t.Errorf("describe should emit 6 descriptors, got %d", count)
	}
}

func TestClassifyErrAllBranches(t *testing.T) {
	cases := map[string]string{
		"":                                        "",
		"context deadline exceeded":                "timeout",
		"context canceled":                         "canceled",
		"Access denied for user":                   "permission_denied",
		"permission denied to access":              "permission_denied",
		"must be superuser":                        "permission_denied",
		"insufficient privilege":                   "permission_denied",
		"connection refused":                       "connectivity",
		"no such host":                             "connectivity",
		"broken pipe":                              "connectivity",
		"i/o timeout":                              "connectivity",
		"connection reset by peer":                 "connectivity",
		"syntax error at or near":                  "query",
		"relation does not exist":                  "query",
		"unknown column 'foo'":                     "query",
		"something completely unexpected happened": "other",
	}
	for msg, want := range cases {
		var err error
		if msg != "" {
			err = errors.New(msg)
		}
		if got := classifyErr(err); got != want {
			t.Errorf("classifyErr(%q) = %q, want %q", msg, got, want)
		}
	}
}
