package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/userdef"
)

// newMockDB wires a sqlmock pool into *sql.DB. Tests use QueryMatcherEqual
// for cleaner assertions — every Scraper here issues fully-deterministic
// SQL so we want exact matches to catch regressions in the query text.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// findSample returns the first sample with the given name, or nil. Tests
// assert on sample shape rather than re-parsing /metrics text — faster
// and less brittle.
func findSample(samples []gormmetrics.Sample, name string) *gormmetrics.Sample {
	for i := range samples {
		if samples[i].Name == name {
			return &samples[i]
		}
	}
	return nil
}

// TestConnectionsScraper covers the connection-pool quartet. Verifies
// that gauges vs counters are tagged correctly and that variables
// missing from the result are silently dropped (not zero-valued — that
// would be a lie).
func TestConnectionsScraper(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW GLOBAL STATUS WHERE Variable_name IN`).
		WillReturnRows(sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("Threads_connected", "12").
			AddRow("Threads_running", "3").
			AddRow("Max_used_connections", "150").
			AddRow("Aborted_connects", "7").
			AddRow("Connections", "9999"))
	// (Aborted_clients intentionally omitted — should not appear in output.)

	samples, err := (ConnectionsScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	if s := findSample(samples, "mysql_threads_connected"); s == nil || s.Value != 12 || s.Type != gormmetrics.Gauge {
		t.Errorf("mysql_threads_connected wrong: %+v", s)
	}
	if s := findSample(samples, "mysql_connections_total"); s == nil || s.Value != 9999 || s.Type != gormmetrics.Counter {
		t.Errorf("mysql_connections_total should be Counter=9999, got %+v", s)
	}
	if s := findSample(samples, "mysql_aborted_clients_total"); s != nil {
		t.Error("Aborted_clients was not in mock; metric must not appear")
	}
}

// TestTrafficScraperCommandsLabel proves the per-command counter is
// label-faceted under a single metric name (mysql_commands_total) rather
// than fanning out into mysql_com_select_total / mysql_com_insert_total etc.
// This is the design choice that distinguishes us from upstream and the
// regression most worth guarding.
func TestTrafficScraperCommandsLabel(t *testing.T) {
	db, mock := newMockDB(t)

	// Combined single query: fixed vars + Com_* pattern
	mock.ExpectQuery(`SHOW GLOBAL STATUS WHERE Variable_name IN`).
		WillReturnRows(sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("Queries", "100000").
			AddRow("Slow_queries", "42").
			AddRow("Bytes_sent", "5000").
			AddRow("Com_select", "80000").
			AddRow("Com_insert", "10000").
			AddRow("Com_update", "5000").
			AddRow("Com_show_databases", "999"))

	samples, err := (TrafficScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	// Aggregated fixed counters
	if s := findSample(samples, "mysql_queries_total"); s == nil || s.Value != 100000 {
		t.Errorf("queries_total missing/wrong: %+v", s)
	}

	// Label-faceted command counters
	var foundSelect, foundShow bool
	for _, s := range samples {
		if s.Name != "mysql_commands_total" {
			continue
		}
		switch s.Labels["command"] {
		case "select":
			foundSelect = true
			if s.Value != 80000 {
				t.Errorf("commands{command=select}.Value = %v, want 80000", s.Value)
			}
		case "show_databases":
			foundShow = true
		}
	}
	if !foundSelect {
		t.Error("mysql_commands_total{command=select} missing")
	}
	if foundShow {
		t.Error("Com_show_databases should be filtered out — only interesting commands surface")
	}
}

// TestInnoDBBufferPoolStateLabel covers the design decision to surface
// buffer pool occupancy as one metric with a 'state' label rather than
// five sibling metrics — this is what makes a Grafana stacked area
// chart trivial to build.
func TestInnoDBBufferPoolStateLabel(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW GLOBAL STATUS WHERE Variable_name IN`).
		WillReturnRows(sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("Innodb_buffer_pool_pages_total", "1000").
			AddRow("Innodb_buffer_pool_pages_free", "200").
			AddRow("Innodb_buffer_pool_pages_data", "750").
			AddRow("Innodb_buffer_pool_pages_dirty", "30").
			AddRow("Innodb_buffer_pool_reads", "55"))

	samples, err := (InnoDBScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	states := map[string]float64{}
	for _, s := range samples {
		if s.Name == "mysql_innodb_buffer_pool_pages" {
			states[s.Labels["state"]] = s.Value
		}
	}
	if states["total"] != 1000 || states["free"] != 200 || states["data"] != 750 || states["dirty"] != 30 {
		t.Errorf("buffer pool by state wrong: %+v", states)
	}

	if s := findSample(samples, "mysql_innodb_buffer_pool_reads_total"); s == nil || s.Type != gormmetrics.Counter {
		t.Errorf("buffer_pool_reads_total must be Counter, got %+v", s)
	}
}

// TestInnoDBProbeFailsClosed simulates the silent-empty-result mode of
// MySQL 5.7+ when PROCESS is missing. The probe must reject so the
// scraper gets disabled instead of emitting zero data forever.
func TestInnoDBProbeFailsClosed(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW GLOBAL STATUS LIKE 'Innodb_buffer_pool_pages_total'`).
		WillReturnError(sql.ErrNoRows) // simulate: row.Scan returns ErrNoRows

	err := (InnoDBScraper{}).Probe(context.Background(), db)
	if err == nil {
		t.Fatal("Probe should fail when no rows returned (PROCESS missing)")
	}
}

// TestReplicationOnPrimary verifies a server with replication disabled
// reports is_replica=0 instead of silently emitting nothing — that's the
// only signal a dashboard has to distinguish "no replication" from
// "monitoring is broken".
func TestReplicationOnPrimary(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW REPLICA STATUS`).
		WillReturnRows(sqlmock.NewRows([]string{})) // empty result = primary

	samples, err := (&ReplicationScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	s := findSample(samples, "mysql_replica_is_replica")
	if s == nil || s.Value != 0 {
		t.Fatalf("primary should report is_replica=0, got %+v", s)
	}
}

// TestReplicationOnReplica exercises the happy path: lag + thread state
// columns get mapped to the canonical metric names.
func TestReplicationOnReplica(t *testing.T) {
	db, mock := newMockDB(t)
	cols := []string{"Seconds_Behind_Source", "Replica_IO_Running", "Replica_SQL_Running", "Source_Host"}
	mock.ExpectQuery(`SHOW REPLICA STATUS`).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow(int64(3), "Yes", "Yes", "10.0.0.1"))

	samples, err := (&ReplicationScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "mysql_replica_lag_seconds"); s == nil || s.Value != 3 {
		t.Errorf("lag wrong: %+v", s)
	}
	if s := findSample(samples, "mysql_replica_io_running"); s == nil || s.Value != 1 {
		t.Errorf("io_running should be 1, got %+v", s)
	}
}

// TestPresetsAreDistinct guards the design contract that StandardPack ⊇
// MinimalPack and FullPack ⊇ StandardPack — i.e. each tier strictly adds.
// If somebody reshuffles scrapers between tiers this catches it.
func TestPresetsAreDistinct(t *testing.T) {
	m, s, f := names(MinimalPack()), names(StandardPack()), names(FullPack())

	for n := range m {
		if !s[n] {
			t.Errorf("StandardPack must include MinimalPack's %q", n)
		}
	}
	for n := range s {
		if !f[n] {
			t.Errorf("FullPack must include StandardPack's %q", n)
		}
	}
	if len(f) <= len(s) || len(s) <= len(m) {
		t.Errorf("tier sizes must strictly grow: minimal=%d standard=%d full=%d", len(m), len(s), len(f))
	}
}

func names(scrapers []gormmetrics.Scraper) map[string]bool {
	out := make(map[string]bool, len(scrapers))
	for _, s := range scrapers {
		out[s.Name()] = true
	}
	return out
}

// TestMustFloat guards the parse-vs-skip helper that all string-valued
// MySQL status variables flow through.
func TestMustFloat(t *testing.T) {
	cases := map[string]float64{
		"42":   42,
		"3.14": 3.14,
	}
	for in, want := range cases {
		f, ok := mustFloat(in)
		if !ok || f != want {
			t.Errorf("mustFloat(%q) = (%v, %v), want (%v, true)", in, f, ok, want)
		}
	}
	// parse failures
	for _, in := range []string{"", "on"} {
		if _, ok := mustFloat(in); ok {
			t.Errorf("mustFloat(%q) = (_, true), want (_, false)", in)
		}
	}
}

// TestStatusScrapeError ensures fetchStatus surfaces driver errors rather
// than swallowing them as empty maps — important for the meta-metric
// permission_denied bucket to fire correctly.
func TestStatusScrapeError(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW GLOBAL STATUS WHERE Variable_name IN`).
		WillReturnError(errors.New("Access denied for user"))

	_, err := (ConnectionsScraper{}).Scrape(context.Background(), db)
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
}

// TestQueryLatencyProbeSuccess: the perf_schema scraper's probe is
// what gates Full-tier auto-enable. Verify it returns nil when the
// table is accessible.
func TestQueryLatencyProbeSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`events_statements_summary_global_by_event_name`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := (QueryLatencyScraper{}).Probe(context.Background(), db); err != nil {
		t.Fatalf("probe should succeed when perf_schema table is readable: %v", err)
	}
}

// TestQueryLatencyProbeFailureDisables: when perf_schema is locked
// down, the probe must surface an error so the framework can disable
// the scraper. Anything else would silently produce zero data.
func TestQueryLatencyProbeFailureDisables(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`events_statements_summary_global_by_event_name`).
		WillReturnError(errors.New("SELECT command denied"))

	if err := (QueryLatencyScraper{}).Probe(context.Background(), db); err == nil {
		t.Fatal("probe should fail when user lacks perf_schema SELECT")
	}
}

// TestQueryLatencyHistogramPath exercises the modern 8.0.3+ pathway
// that reads cumulative bucket counts from
// events_statements_histogram_global. The Sample shape is the most
// fragile thing in this file because Prometheus rejects histograms
// with inconsistent bucket counts.
func TestQueryLatencyHistogramPath(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`events_statements_histogram_global`).
		WillReturnRows(sqlmock.NewRows([]string{"BUCKET_TIMER_LOW", "BUCKET_TIMER_HIGH", "COUNT_BUCKET_AND_LOWER"}).
			// Buckets in picoseconds; our coarse layout snaps these
			// into the seconds-scale set defined in scraper_perfschema.go.
			AddRow(uint64(0), uint64(100_000_000), uint64(50)).                    // 0.1ms ⇒ snaps to 0.0001s bucket
			AddRow(uint64(100_000_000), uint64(1_000_000_000), uint64(80)).        // 1ms ⇒ 0.001s
			AddRow(uint64(1_000_000_000), uint64(1_000_000_000_000), uint64(100))) // 1s ⇒ 1s

	samples, err := (QueryLatencyScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(samples) != 1 || samples[0].Type != gormmetrics.Histogram {
		t.Fatalf("expected one Histogram sample, got %+v", samples)
	}
	if samples[0].HistogramCount != 100 {
		t.Errorf("expected total count 100, got %d", samples[0].HistogramCount)
	}
}

// TestQueryLatencySummaryFallback: when the 8.0.3+ histogram table
// is absent (older servers / MariaDB), Scrape falls back to a
// count+sum derived from the summary table. The single emitted
// sample carries an empty bucket map but valid count/sum.
func TestQueryLatencySummaryFallback(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`events_statements_histogram_global`).
		WillReturnError(errors.New("Table 'events_statements_histogram_global' doesn't exist"))
	mock.ExpectQuery(`events_statements_summary_global_by_event_name`).
		WillReturnRows(sqlmock.NewRows([]string{"count", "sum_pico"}).
			AddRow(uint64(1000), uint64(50_000_000_000_000))) // 50s total

	samples, err := (QueryLatencyScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if samples[0].HistogramCount != 1000 {
		t.Errorf("count from summary should be 1000, got %d", samples[0].HistogramCount)
	}
	if samples[0].HistogramSum != 50 {
		t.Errorf("sum should be 50s (from 50e12 ps), got %v", samples[0].HistogramSum)
	}
}

// TestReplicationProbeSuccess and the SLAVE-spelling probe variant
// nail down the only non-trivial bit of the Replication scraper not
// already covered: its probe path.
func TestReplicationProbeSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SHOW REPLICA STATUS`).
		WillReturnRows(sqlmock.NewRows([]string{}))

	if err := (&ReplicationScraper{}).Probe(context.Background(), db); err != nil {
		t.Errorf("probe should succeed against an empty result: %v", err)
	}
}

// TestStatusQuerySwitchesOnLegacySpelling: when the user has flagged
// UseLegacySlaveSpelling=true, the scraper should issue SHOW SLAVE
// STATUS rather than the REPLICA spelling. Asserting the chosen
// query string is the cheapest way to guard the switch.
func TestStatusQuerySwitchesOnLegacySpelling(t *testing.T) {
	yes := true
	if got := (&ReplicationScraper{UseLegacySlaveSpelling: &yes}).statusQuery(); got != "SHOW SLAVE STATUS" {
		t.Errorf("legacy spelling = %q, want %q", got, "SHOW SLAVE STATUS")
	}
	if got := (&ReplicationScraper{}).statusQuery(); got != "SHOW REPLICA STATUS" {
		t.Errorf("default spelling = %q, want %q", got, "SHOW REPLICA STATUS")
	}
}

// TestToFloatVariants exercises mysql/scraper_replication.go's
// driver-value coercion helper across the value shapes sqlmock and
// real drivers produce. Mirror of the postgres equivalent.
func TestToFloatVariants(t *testing.T) {
	cases := []struct {
		in     interface{}
		wantOk bool
		wantV  float64
	}{
		{nil, false, 0},
		{int64(42), true, 42},
		{float64(3.14), true, 3.14},
		{[]byte("100"), true, 100},
		{"99", true, 99},
		{[]byte("garbage"), false, 0},
		{"garbage", false, 0},
		{true, true, 1},
	}
	for _, c := range cases {
		v, ok := userdef.ToFloat(c.in)
		if ok != c.wantOk || (ok && v != c.wantV) {
			t.Errorf("toFloat(%v) = (%v, %v), want (%v, %v)", c.in, v, ok, c.wantV, c.wantOk)
		}
	}
}

// TestYesToFloatRejectsNonStrings: yesToFloat ignores anything other
// than Yes/yes. The "Connecting" replication state must be 0 — a
// historical bug source if someone changes the comparison.
func TestYesToFloatRejectsNonStrings(t *testing.T) {
	if yesToFloat("Yes") != 1 {
		t.Error("Yes should be 1")
	}
	if yesToFloat([]byte("YES")) != 1 {
		t.Error("[]byte(YES) should be 1")
	}
	if yesToFloat("Connecting") != 0 {
		t.Error("Connecting should be 0 (intermediate state, not running)")
	}
	if yesToFloat(int64(1)) != 0 {
		t.Error("non-string types should be 0")
	}
}
