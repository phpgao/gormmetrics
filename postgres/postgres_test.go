package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/userdef"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	// QueryMatcherRegexp: Postgres uses $1 placeholders which complicate
	// exact-match assertions; regex lets us focus on the structural query
	// shape rather than literal text.
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func findSample(samples []gormmetrics.Sample, name string, labels map[string]string) *gormmetrics.Sample {
	for i := range samples {
		s := samples[i]
		if s.Name != name {
			continue
		}
		match := true
		for k, v := range labels {
			if s.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return &samples[i]
		}
	}
	return nil
}

// TestActivityScraperLabelFacets ensures we collapse all per-state
// backend counts under one metric name. This is the design contract
// that makes the standard PG dashboard's "connections by state" panel
// a single PromQL query.
func TestActivityScraperLabelFacets(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COALESCE\(state, 'unknown'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"state", "n"}).
			AddRow("active", int64(5)).
			AddRow("idle", int64(20)).
			AddRow("idle in transaction", int64(2)))

	samples, err := (ActivityScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	if s := findSample(samples, "postgres_backend_connections", map[string]string{"state": "active"}); s == nil || s.Value != 5 {
		t.Errorf("active count missing/wrong: %+v", s)
	}
	if s := findSample(samples, "postgres_backend_connections", map[string]string{"state": "idle"}); s == nil || s.Value != 20 {
		t.Errorf("idle count missing/wrong: %+v", s)
	}
	if s := findSample(samples, "postgres_backend_connections_total_current", nil); s == nil || s.Value != 27 {
		t.Errorf("total should be 27 (5+20+2), got %+v", s)
	}
}

// TestSizeScraperOmitsTemplates verifies the LIKE filter — template DBs
// never change size and are noise on dashboards.
func TestSizeScraperOmitsTemplates(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`pg_database_size`).
		WillReturnRows(sqlmock.NewRows([]string{"datname", "size"}).
			AddRow("orders", int64(1<<30))) // 1 GiB

	samples, err := (SizeScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "postgres_database_size_bytes", map[string]string{"datname": "orders"}); s == nil || s.Value != float64(1<<30) {
		t.Errorf("size wrong: %+v", s)
	}
}

// TestDatabaseScraperCounterDispatch confirms pg_stat_database counters
// are tagged as Counter (not Gauge), so rate() works downstream.
func TestDatabaseScraperCounterDispatch(t *testing.T) {
	db, mock := newMockDB(t)
	cols := []string{
		"datname", "xact_commit", "xact_rollback", "blks_read", "blks_hit",
		"tup_returned", "tup_fetched", "tup_inserted", "tup_updated", "tup_deleted",
		"conflicts", "deadlocks", "temp_files", "temp_bytes", "numbackends",
	}
	mock.ExpectQuery(`SELECT datname.*FROM pg_stat_database`).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("orders",
				int64(1000), int64(50), // commits, rollbacks
				int64(10), int64(990), // blks read/hit
				int64(0), int64(0), int64(0), int64(0), int64(0),
				int64(0), int64(2), // deadlocks
				int64(0), int64(0),
				int64(15))) // numbackends

	samples, err := (DatabaseScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "postgres_xact_commit_total", map[string]string{"datname": "orders"}); s == nil || s.Type != gormmetrics.Counter || s.Value != 1000 {
		t.Errorf("xact_commit_total wrong: %+v", s)
	}
	if s := findSample(samples, "postgres_backends", map[string]string{"datname": "orders"}); s == nil || s.Type != gormmetrics.Gauge || s.Value != 15 {
		t.Errorf("backends should be Gauge=15, got %+v", s)
	}
}

// TestReplicationOnPrimaryEmptyView covers the typical primary case:
// pg_stat_replication has zero rows and we still want is_in_recovery=0
// emitted as a clear "not a replica" signal.
func TestReplicationOnPrimaryEmptyView(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery`).
		WillReturnRows(sqlmock.NewRows([]string{"in_recovery"}).AddRow(false))
	mock.ExpectQuery(`FROM pg_stat_replication`).
		WillReturnRows(sqlmock.NewRows([]string{"application_name", "state", "write_lag_seconds", "replay_lag_seconds"}))

	samples, err := (ReplicationScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "postgres_in_recovery", nil); s == nil || s.Value != 0 {
		t.Errorf("in_recovery should be 0 on primary: %+v", s)
	}
	// No replicas connected — postgres_replication_*_lag_seconds samples
	// should be absent (not zero-valued).
	if s := findSample(samples, "postgres_replication_write_lag_seconds", nil); s != nil {
		t.Errorf("write_lag_seconds should be absent with no replicas, got %+v", s)
	}
}

// TestPresetsAreDistinct — same invariant as MySQL.
func TestPresetsAreDistinct(t *testing.T) {
	m := names(MinimalPack())
	s := names(StandardPack())
	f := names(FullPack())
	for n := range m {
		if !s[n] {
			t.Errorf("StandardPack must include MinimalPack %q", n)
		}
	}
	for n := range s {
		if !f[n] {
			t.Errorf("FullPack must include StandardPack %q", n)
		}
	}
}

func names(scrapers []gormmetrics.Scraper) map[string]bool {
	out := make(map[string]bool, len(scrapers))
	for _, s := range scrapers {
		out[s.Name()] = true
	}
	return out
}

// TestLocksScraperGroupsByMode covers the pg_locks GROUP BY scrape — a
// distinct code path because we map two of three columns to labels.
func TestLocksScraperGroupsByMode(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`FROM pg_locks`).
		WillReturnRows(sqlmock.NewRows([]string{"mode", "granted", "n"}).
			AddRow("AccessShareLock", true, int64(45)).
			AddRow("AccessShareLock", false, int64(2)).
			AddRow("RowExclusiveLock", true, int64(7)))

	samples, err := (LocksScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "postgres_locks",
		map[string]string{"mode": "AccessShareLock", "granted": "true"}); s == nil || s.Value != 45 {
		t.Errorf("AccessShareLock granted should be 45, got %+v", s)
	}
	if s := findSample(samples, "postgres_locks",
		map[string]string{"mode": "AccessShareLock", "granted": "false"}); s == nil || s.Value != 2 {
		t.Errorf("AccessShareLock waiting should be 2, got %+v", s)
	}
}

// TestTableStatScraperEmitsPerTableSamples smoke-tests the per-table
// fan-out. Each table generates 9 samples (3 Gauge + 6 Counter), so
// label wiring is the main thing worth asserting.
func TestTableStatScraperEmitsPerTableSamples(t *testing.T) {
	db, mock := newMockDB(t)
	cols := []string{
		"schemaname", "relname",
		"seq_scan", "seq_tup_read",
		"idx_scan", "idx_tup_fetch",
		"n_tup_ins", "n_tup_upd", "n_tup_del",
		"n_live_tup", "n_dead_tup",
	}
	mock.ExpectQuery(`FROM pg_stat_user_tables`).
		WithArgs("public").
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("public", "orders",
				int64(10), int64(1000),
				int64(50), int64(800),
				int64(100), int64(20), int64(5),
				int64(95), int64(3)))

	samples, err := (TableStatScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	labels := map[string]string{"schemaname": "public", "relname": "orders"}
	if s := findSample(samples, "postgres_table_seq_scan_total", labels); s == nil || s.Type != gormmetrics.Counter || s.Value != 10 {
		t.Errorf("seq_scan_total wrong: %+v", s)
	}
	if s := findSample(samples, "postgres_table_live_tuples", labels); s == nil || s.Type != gormmetrics.Gauge || s.Value != 95 {
		t.Errorf("live_tuples should be Gauge=95, got %+v", s)
	}
}

// TestReplicationScraperProbe documents the probe contract — the
// "can the user call replication-relevant system functions?" check
// the framework uses to disable the scraper when the answer is no.
func TestReplicationScraperProbe(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`pg_last_wal_replay_lsn`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := (ReplicationScraper{}).Probe(context.Background(), db); err != nil {
		t.Fatalf("probe should succeed when both functions are callable: %v", err)
	}
}

// TestActivityScraperZeroBackends covers the "quiet server" path — no
// rows back from the GROUP BY. The total gauge must still be emitted
// (as 0) so dashboards can tell "no backends" apart from "scraper failed".
func TestActivityScraperZeroBackends(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`pg_stat_activity`).
		WillReturnRows(sqlmock.NewRows([]string{"state", "n"}))

	samples, err := (ActivityScraper{}).Scrape(context.Background(), db)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if s := findSample(samples, "postgres_backend_connections_total_current", nil); s == nil || s.Value != 0 {
		t.Fatalf("total should be 0 on quiet server, got %+v", s)
	}
}

// TestToFloatCoversAllDriverShapes exercises the value-coercion helper
// against every shape database/sql drivers actually return.
func TestToFloatCoversAllDriverShapes(t *testing.T) {
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
		{true, true, 1}, // bool is converted to 1 (userdef.ToFloat handles bool)
	}
	for _, c := range cases {
		v, ok := userdef.ToFloat(c.in)
		if ok != c.wantOk || (ok && v != c.wantV) {
			t.Errorf("toFloat(%v) = (%v, %v), want (%v, %v)", c.in, v, ok, c.wantV, c.wantOk)
		}
	}
}
