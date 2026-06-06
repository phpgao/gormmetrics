package gormmetrics

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// fakeScraper implements Scraper for tests. ProbeErr (if non-nil) makes
// fakeScraper also implement ProbingScraper.
type fakeScraper struct {
	name      string
	samples   []Sample
	scrapeErr error
	calls     int
}

func (f *fakeScraper) Name() string { return f.name }
func (f *fakeScraper) Scrape(_ context.Context, _ *sql.DB) ([]Sample, error) {
	f.calls++
	return f.samples, f.scrapeErr
}

// fakeProbingScraper exposes Probe so it satisfies ProbingScraper.
type fakeProbingScraper struct {
	fakeScraper
	pErr error
}

func (p *fakeProbingScraper) Probe(_ context.Context, _ *sql.DB) error { return p.pErr }

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestNewRequiresDB documents the contract that omitting WithDB is a
// configuration bug, not a runtime hazard.
func TestNewRequiresDB(t *testing.T) {
	_, err := New()
	if !errors.Is(err, ErrNoDB) {
		t.Fatalf("expected ErrNoDB, got %v", err)
	}
}

// TestCollectEmitsSamplesAndMeta is the happy-path end-to-end test: one
// scraper, one sample, verify both the data point and the meta-metrics
// land in the registry.
func TestCollectEmitsSamplesAndMeta(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{
		name: "fake",
		samples: []Sample{
			{Name: "fake_value", Help: "test gauge", Type: Gauge, Value: 42},
		},
	}

	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := serveOnce(t, c)

	assertContains(t, body, "fake_value 42")
	assertContains(t, body, "gormmetrics_up 1")
	assertContains(t, body, `gormmetrics_scrape_success{scraper="fake"} 1`)
	assertContains(t, body, `gormmetrics_scrape_samples{scraper="fake"} 1`)
}

// TestCacheTTLDeduplicatesScrapes verifies that two /metrics requests
// within the TTL window share one scraper invocation.
func TestCacheTTLDeduplicatesScrapes(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()
	mock.ExpectPing()

	scr := &fakeScraper{name: "fake", samples: []Sample{{Name: "x", Type: Gauge, Value: 1}}}

	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = serveOnce(t, c)
	_ = serveOnce(t, c)

	if scr.calls != 1 {
		t.Fatalf("expected cache to deduplicate; scraper called %d times, want 1", scr.calls)
	}
}

// TestScrapeErrorReportedViaMeta proves that a scraper returning an error
// doesn't break the Collector — the error is converted into a meta-metric
// data point so operators can alert on it.
func TestScrapeErrorReportedViaMeta(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{name: "broken", scrapeErr: errors.New("access denied")}

	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0), WithLogger(NopLogger{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := serveOnce(t, c)

	assertContains(t, body, `gormmetrics_scrape_success{scraper="broken"} 0`)
	assertContains(t, body, `gormmetrics_scrape_errors{error="permission_denied",scraper="broken"} 1`)
}

// TestProbeFailureDisablesScraper exercises the ProbingScraper path: a
// scraper whose probe fails should never be scraped, but must still
// appear in the disabled meta-metric so operators can see it.
func TestProbeFailureDisablesScraper(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	p := &fakeProbingScraper{
		fakeScraper: fakeScraper{name: "needs-grant", samples: []Sample{{Name: "x", Type: Gauge}}},
		pErr:        errors.New("Access denied; need PROCESS privilege"),
	}

	c, err := New(WithDB(db), WithScrapers(p), WithLogger(NopLogger{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if disabled := c.DisabledScrapers(); len(disabled) != 1 {
		t.Fatalf("expected 1 disabled scraper, got %v", disabled)
	}

	body := serveOnce(t, c)

	// The scraper must NOT have been invoked at all (samples shouldn't show).
	if p.calls != 0 {
		t.Fatalf("disabled scraper was scraped %d times", p.calls)
	}
	assertContains(t, body, `gormmetrics_scraper_disabled{reason="permission_denied",scraper="needs-grant"} 1`)
}

// TestUpZeroOnPingFailure verifies the explicit up gauge: when the DB
// can't even respond to a ping, gormmetrics_up must report 0 — that's
// the canonical "DB is dead" signal Prometheus alerts watch for.
func TestUpZeroOnPingFailure(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing().WillReturnError(errors.New("dial tcp: connection refused"))

	scr := &fakeScraper{name: "fake", samples: []Sample{{Name: "x", Type: Gauge}}}
	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := serveOnce(t, c)
	assertContains(t, body, "gormmetrics_up 0")
}

// TestPingFailureShortCircuitsScrapers is the regression guard for the
// "N × scrapeTimeout avalanche" bug. When ping fails the Collect cycle
// must NOT invoke any registered scraper's Scrape — otherwise a dead
// DB would block /metrics for N × scrapeTimeout seconds.
//
// We assert by counting Scrape() invocations on the fake scraper after
// a request that observed ping=down.
func TestPingFailureShortCircuitsScrapers(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing().WillReturnError(errors.New("dial tcp: connection refused"))

	scr := &fakeScraper{
		name:    "expensive",
		samples: []Sample{{Name: "expensive_metric", Type: Gauge, Value: 1}},
	}
	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := serveOnce(t, c)

	if scr.calls != 0 {
		t.Fatalf("scraper Scrape() must not be called when ping fails; got %d calls", scr.calls)
	}
	// The scraper's user-facing metric must NOT appear (no data).
	if strings.Contains(body, "expensive_metric") {
		t.Fatalf("expensive_metric should be absent when DB is unreachable")
	}
	// But the meta-metric must record the skip as a failed scrape so
	// dashboards can tell "DB down" apart from "scraper not configured".
	assertContains(t, body, `gormmetrics_scrape_success{scraper="expensive"} 0`)
	assertContains(t, body, `gormmetrics_scrape_errors{error="connectivity",scraper="expensive"} 1`)
}

// TestPingFailureStillReportsDisabledScrapers exercises the interaction
// between the short-circuit path and the probe-disabled bookkeeping:
// even when ping fails (so we skip the live scrape loop), disabled
// scrapers must continue to report their disabled state. Otherwise
// gormmetrics_scraper_disabled would silently drop off /metrics during
// DB outages — confusing for anyone reading dashboards.
func TestPingFailureStillReportsDisabledScrapers(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing().WillReturnError(errors.New("connection refused"))

	probed := &fakeProbingScraper{
		fakeScraper: fakeScraper{name: "needs-grant", samples: []Sample{{Name: "x", Type: Gauge}}},
		pErr:        errors.New("Access denied"),
	}
	c, err := New(WithDB(db), WithScrapers(probed), WithLogger(NopLogger{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := serveOnce(t, c)
	assertContains(t, body, "gormmetrics_up 0")
	assertContains(t, body, `gormmetrics_scraper_disabled{reason="permission_denied",scraper="needs-grant"} 1`)
}

// TestScrapeTimeoutPropagatedToScraper verifies that the scrapeTimeout
// configured via WithScrapeTimeout is propagated into the context passed
// to each Scraper.Scrape call. The context is derived from
// context.Background(), not the HTTP request context, because HTTP
// request context propagation was intentionally removed to avoid
// concurrency hazards when the Collector is shared across Registries.
func TestScrapeTimeoutPropagatedToScraper(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	captured := &ctxCapturingScraper{name: "capture"}
	c, err := New(WithDB(db), WithScrapers(captured), WithoutProbe(),
		WithCacheTTL(0), WithScrapeTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = serveOnce(t, c)

	if captured.gotCtx == nil {
		t.Fatal("scraper did not receive a context")
	}
	dl, ok := captured.gotCtx.Deadline()
	if !ok {
		t.Fatal("scrape ctx must carry a deadline derived from scrapeTimeout")
	}
	// The deadline should be within (now, now+scrapeTimeout]. A loose
	// upper bound — exact equality would be racy under load.
	if d := time.Until(dl); d <= 0 || d > 2*time.Second+200*time.Millisecond {
		t.Errorf("deadline %v out of expected range (0, 2.2s]", d)
	}
}

// ctxCapturingScraper records the ctx it receives so tests can inspect
// its Deadline / Done channel. Returns one trivial sample so the rest
// of the Collect path runs normally.
type ctxCapturingScraper struct {
	name   string
	gotCtx context.Context
}

func (c *ctxCapturingScraper) Name() string { return c.name }
func (c *ctxCapturingScraper) Scrape(ctx context.Context, _ *sql.DB) ([]Sample, error) {
	c.gotCtx = ctx
	return []Sample{{Name: "capture_x", Type: Gauge, Value: 1}}, nil
}

// TestConstLabelsApplied confirms that WithLabels values appear on every
// emitted metric, including meta-metrics.
func TestConstLabelsApplied(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{name: "fake", samples: []Sample{{Name: "x", Type: Gauge, Value: 1}}}
	c, err := New(
		WithDB(db),
		WithScrapers(scr),
		WithoutProbe(),
		WithCacheTTL(0),
		WithLabels(map[string]string{"cluster": "eu-1", "env": "prod"}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := serveOnce(t, c)

	assertContains(t, body, `x{cluster="eu-1",env="prod"} 1`)
	assertContains(t, body, `gormmetrics_up{cluster="eu-1",env="prod"} 1`)
}

// TestHistogramSample exercises the histogram path: a single sample
// carries the whole pre-bucketed distribution.
func TestHistogramSample(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{
		name: "h",
		samples: []Sample{{
			Name: "latency_seconds",
			Type: Histogram,
			HistogramBuckets: map[float64]uint64{
				0.1: 5,
				0.5: 8,
				1.0: 10,
			},
			HistogramCount: 10,
			HistogramSum:   3.2,
		}},
	}
	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := serveOnce(t, c)

	assertContains(t, body, `latency_seconds_bucket{le="0.1"} 5`)
	assertContains(t, body, `latency_seconds_bucket{le="1"} 10`)
	assertContains(t, body, "latency_seconds_count 10")
	assertContains(t, body, "latency_seconds_sum 3.2")
}

// TestPerSampleLabelOverridesConst documents a subtle but important rule:
// when a Scraper emits a sample whose label key collides with a const
// label, the per-sample value wins.
func TestPerSampleLabelOverridesConst(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{name: "fake", samples: []Sample{{
		Name: "x", Type: Gauge, Value: 1,
		Labels: map[string]string{"cluster": "override"},
	}}}
	c, err := New(
		WithDB(db),
		WithScrapers(scr),
		WithoutProbe(),
		WithCacheTTL(0),
		WithLabels(map[string]string{"cluster": "default"}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := serveOnce(t, c)
	assertContains(t, body, `x{cluster="override"} 1`)
}

// TestPingTimeoutClassifiesAsTimeout verifies that when the DB ping times
// out (context deadline exceeded), the resulting scrape_errors is tagged
// "timeout" rather than "connectivity" — operators should be able to
// distinguish a dead DB from a slow DB.
func TestPingTimeoutClassifiesAsTimeout(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing().WillReturnError(errors.New("context deadline exceeded"))

	scr := &fakeScraper{name: "fake", samples: []Sample{{Name: "x", Type: Gauge}}}
	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := serveOnce(t, c)
	assertContains(t, body, "gormmetrics_up 0")
	assertContains(t, body, `gormmetrics_scrape_errors{error="timeout",scraper="fake"} 1`)
}

// TestNamespacePrefix ensures the namespace option prepends to all metric
// names, including meta-metrics.
func TestNamespacePrefix(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectPing()

	scr := &fakeScraper{name: "fake", samples: []Sample{{Name: "x", Type: Gauge, Value: 1}}}
	c, err := New(WithDB(db), WithScrapers(scr), WithoutProbe(), WithCacheTTL(0), WithNamespace("myapp"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := serveOnce(t, c)
	assertContains(t, body, "myapp_x 1")
	assertContains(t, body, "myapp_gormmetrics_up 1")
}

// TestClassifyErr is a small unit test on the error bucketing helper —
// the categories drive alerting decisions so they need to be stable.
func TestClassifyErr(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"Access denied for user 'app'@'%'":   "permission_denied",
		"must be superuser to read pg_stat":  "permission_denied",
		"dial tcp: connection refused":       "connectivity",
		"context deadline exceeded":          "timeout",
		"context canceled":                   "canceled",
		"syntax error at or near \"FOO\"":    "query",
		"relation \"orders\" does not exist": "query",
		"some random error":                  "other",
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

// serveOnce mounts the Collector's Handler on a single-shot test server,
// fetches /metrics, and returns the body as a string. This is the
// idiomatic way to exercise a prometheus.Collector end-to-end.
func serveOnce(t *testing.T, c *Collector) string {
	t.Helper()
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<15)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func assertContains(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("body missing %q\n--- body ---\n%s", want, body)
	}
}
