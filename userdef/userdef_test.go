package userdef

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/phpgao/gormmetrics"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestSQLGaugeHappyPath: minimal scalar query → one Gauge sample with the
// right type and value. The most basic contract this whole package exists
// to honour.
func TestSQLGaugeHappyPath(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM orders`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(int64(42)))

	g := &SQLGauge{
		MetricName: "orders_pending",
		Query:      "SELECT COUNT(*) FROM orders",
	}
	samples, err := g.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(samples) != 1 || samples[0].Type != gormmetrics.Gauge || samples[0].Value != 42 {
		t.Fatalf("got %+v", samples)
	}
	if want := "userdef:gauge:orders_pending"; g.Name() != "userdef:gauge:orders_pending" {
		t.Errorf("Name() = %q, want %q", g.Name(), want)
	}
}

// TestSQLCounterType ensures the Counter variant tags samples accordingly
// — the type field drives the difference between PromQL rate() working
// and producing garbage.
func TestSQLCounterType(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT n FROM seq`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(int64(1000)))

	c := &SQLCounter{MetricName: "events_total", Query: "SELECT n FROM seq"}
	samples, _ := c.Scrape(context.Background(), db)
	if samples[0].Type != gormmetrics.Counter {
		t.Fatalf("expected Counter, got %v", samples[0].Type)
	}
}

// TestSQLGaugeValidatesConfig exercises the "missing required field"
// guard — fail-fast is much better than emitting silent nonsense.
func TestSQLGaugeValidatesConfig(t *testing.T) {
	db, _ := newMockDB(t)
	g := &SQLGauge{} // no Name or Query
	_, err := g.Scrape(context.Background(), db)
	if err == nil {
		t.Fatal("expected error for empty config")
	}
}

// TestSQLLabeledDefaultValueColumnIsLast covers the "common case" path:
// SELECT label1, label2, count(*) FROM ... GROUP BY label1, label2.
// We don't force users to spell out ValueColumn for this shape.
func TestSQLLabeledDefaultValueColumnIsLast(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT status`).
		WillReturnRows(sqlmock.NewRows([]string{"status", "n"}).
			AddRow("pending", int64(10)).
			AddRow("shipped", int64(50)))

	l := &SQLLabeled{
		MetricName: "orders_by_status",
		Query:      "SELECT status, COUNT(*) FROM orders GROUP BY status",
		Type:       gormmetrics.Gauge,
	}
	samples, err := l.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("want 2 samples, got %d: %+v", len(samples), samples)
	}
	for _, s := range samples {
		if s.Labels["status"] == "pending" && s.Value != 10 {
			t.Errorf("pending value wrong: %+v", s)
		}
		if s.Labels["status"] == "shipped" && s.Value != 50 {
			t.Errorf("shipped value wrong: %+v", s)
		}
	}
}

// TestSQLLabeledExplicitValueColumn lets the user pick the value column
// out of a wider result — useful for "SELECT id, label, count" style
// queries where the id column is junk.
func TestSQLLabeledExplicitValueColumn(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT.*FROM tasks`).
		WillReturnRows(sqlmock.NewRows([]string{"queue", "depth", "age_seconds"}).
			AddRow("emails", int64(7), int64(120)))

	l := &SQLLabeled{
		MetricName:   "queue_depth",
		Query:        "SELECT queue, depth, age_seconds FROM tasks",
		ValueColumn:  "depth",
		LabelColumns: []string{"queue"},
		Type:         gormmetrics.Gauge,
	}
	samples, err := l.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(samples) != 1 || samples[0].Value != 7 || samples[0].Labels["queue"] != "emails" {
		t.Fatalf("wrong: %+v", samples)
	}
}

// TestSQLLabeledRejectsMissingColumn protects against typos that
// would otherwise silently produce empty results.
func TestSQLLabeledRejectsMissingColumn(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT`).
		WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow("x", int64(1)))

	l := &SQLLabeled{
		MetricName:  "x",
		Query:       "SELECT a, b FROM t",
		ValueColumn: "nonexistent",
		Type:        gormmetrics.Gauge,
	}
	_, err := l.Scrape(context.Background(), db)
	if err == nil {
		t.Fatal("expected error for missing ValueColumn")
	}
}

// TestSQLHistogramDerivesCountFromMaxBucket: the typical-use path where
// the user only supplies BucketsQuery and we synthesise count from the
// cumulative tail.
func TestSQLHistogramDerivesCountFromMaxBucket(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT bucket`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "cum"}).
			AddRow(0.1, uint64(5)).
			AddRow(0.5, uint64(8)).
			AddRow(1.0, uint64(10)))

	h := &SQLHistogram{
		MetricName:   "latency_seconds",
		BucketsQuery: "SELECT bucket, cum FROM hist",
	}
	samples, err := h.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(samples) != 1 || samples[0].Type != gormmetrics.Histogram {
		t.Fatalf("got %+v", samples)
	}
	if samples[0].HistogramCount != 10 {
		t.Errorf("count should be derived from largest cumulative bucket (10), got %d", samples[0].HistogramCount)
	}
	if got := samples[0].HistogramBuckets[1.0]; got != 10 {
		t.Errorf("bucket 1.0 should be 10, got %d", got)
	}
}

// TestFuncScraperPassThrough: the escape-hatch type must faithfully relay
// whatever the Collect callback produces. Includes an error path to
// confirm errors propagate to the meta-metric layer.
func TestFuncScraperPassThrough(t *testing.T) {
	db, _ := newMockDB(t)
	called := false
	f := &FuncScraper{
		ID: "myapp_widgets",
		Collect: func(_ context.Context, _ *sql.DB) ([]gormmetrics.Sample, error) {
			called = true
			return []gormmetrics.Sample{{Name: "myapp_widgets", Type: gormmetrics.Gauge, Value: 17}}, nil
		},
	}
	samples, err := f.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if !called || len(samples) != 1 || samples[0].Value != 17 {
		t.Fatalf("got %+v (called=%v)", samples, called)
	}

	f2 := &FuncScraper{
		ID: "bad",
		Collect: func(_ context.Context, _ *sql.DB) ([]gormmetrics.Sample, error) {
			return nil, errors.New("simulated failure")
		},
	}
	_, err = f2.Scrape(context.Background(), db)
	if err == nil {
		t.Fatal("expected propagated error")
	}
}

// TestScraperNames exercises every Scraper.Name() implementation, both
// the default form and the ScraperName override. The names show up as
// label values on meta-metrics, so silent typos here would land in
// production dashboards.
func TestScraperNames(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"SQLGauge default", (&SQLGauge{MetricName: "foo"}).Name(), "userdef:gauge:foo"},
		{"SQLGauge override", (&SQLGauge{MetricName: "foo", ScraperName: "custom"}).Name(), "custom"},
		{"SQLCounter default", (&SQLCounter{MetricName: "bar"}).Name(), "userdef:counter:bar"},
		{"SQLCounter override", (&SQLCounter{MetricName: "bar", ScraperName: "x"}).Name(), "x"},
		{"SQLLabeled default", (&SQLLabeled{MetricName: "baz"}).Name(), "userdef:labeled:baz"},
		{"SQLLabeled override", (&SQLLabeled{MetricName: "baz", ScraperName: "y"}).Name(), "y"},
		{"SQLHistogram default", (&SQLHistogram{MetricName: "qux"}).Name(), "userdef:histogram:qux"},
		{"SQLHistogram override", (&SQLHistogram{MetricName: "qux", ScraperName: "z"}).Name(), "z"},
		{"FuncScraper default", (&FuncScraper{ID: "frob"}).Name(), "userdef:func:frob"},
		{"FuncScraper override", (&FuncScraper{ID: "frob", ScraperName: "w"}).Name(), "w"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestSQLLabeledValidatesConfig and TestSQLHistogramValidatesConfig
// cover the fail-fast guards in the remaining userdef types, parallel
// to TestSQLGaugeValidatesConfig already in this file.
func TestSQLLabeledValidatesConfig(t *testing.T) {
	db, _ := newMockDB(t)
	if _, err := (&SQLLabeled{}).Scrape(context.Background(), db); err == nil {
		t.Fatal("missing MetricName+Query should error")
	}
}

func TestSQLHistogramValidatesConfig(t *testing.T) {
	db, _ := newMockDB(t)
	if _, err := (&SQLHistogram{}).Scrape(context.Background(), db); err == nil {
		t.Fatal("missing MetricName+BucketsQuery should error")
	}
}

func TestFuncScraperValidatesConfig(t *testing.T) {
	db, _ := newMockDB(t)
	if _, err := (&FuncScraper{}).Scrape(context.Background(), db); err == nil {
		t.Fatal("missing ID+Collect should error")
	}
}

// TestSQLHistogramWithExplicitCountAndSum verifies the optional
// Count/Sum query path — when provided, they override the derive-from-
// max-bucket fallback.
func TestSQLHistogramWithExplicitCountAndSum(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT bucket`).
		WillReturnRows(sqlmock.NewRows([]string{"b", "c"}).
			AddRow(0.1, uint64(5)).
			AddRow(1.0, uint64(10)))
	mock.ExpectQuery(`SELECT total_count`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(uint64(999))) // override
	mock.ExpectQuery(`SELECT total_sum`).
		WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(float64(42.5)))

	h := &SQLHistogram{
		MetricName:   "x",
		BucketsQuery: "SELECT bucket, cum FROM hist",
		CountQuery:   "SELECT total_count FROM summary",
		SumQuery:     "SELECT total_sum FROM summary",
	}
	samples, err := h.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if samples[0].HistogramCount != 999 {
		t.Errorf("count override ignored, got %d", samples[0].HistogramCount)
	}
	if samples[0].HistogramSum != 42.5 {
		t.Errorf("sum override ignored, got %v", samples[0].HistogramSum)
	}
}

// TestCopyLabelsDoesNotShareBacking guards against a subtle mutation bug:
// if SQLGauge handed the user's map directly into Sample.Labels, a later
// mutation by the caller would silently rewrite already-emitted samples
// retained in the cache.
func TestCopyLabelsDoesNotShareBacking(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(int64(1)))

	src := map[string]string{"a": "1"}
	g := &SQLGauge{MetricName: "x", Query: "SELECT 1", Labels: src}
	samples, err := g.Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	src["a"] = "TAMPERED"
	if samples[0].Labels["a"] != "1" {
		t.Fatalf("sample labels shared backing store with caller's map")
	}
}
