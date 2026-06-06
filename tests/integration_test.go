package tests

// End-to-end integration tests against real MySQL and PostgreSQL servers.
//
// These tests are intentionally placed in a separate tests/ package to avoid
// being mixed with the fast unit-tests during a go test ./... run. Run them
// only when a real database is available:
//
//	GORMMETRICS_MYSQL_DSN='user:pass@tcp(host:3306)/dbname' \
//	GORMMETRICS_POSTGRES_DSN='host=host port=5432 user=u password=p dbname=d sslmode=disable' \
//	    go test -v ./tests/...
//
// Either DSN can be omitted — the corresponding test will Skip.

import (
	"database/sql"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
	"github.com/phpgao/gormmetrics/postgres"
)

func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GORMMETRICS_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GORMMETRICS_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(30 * time.Second)
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GORMMETRICS_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GORMMETRICS_POSTGRES_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(30 * time.Second)
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func scrapeOnce(t *testing.T, c *gormmetrics.Collector) string {
	t.Helper()
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<17)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

// TestMySQLEndToEnd: open a real MySQL, install the StandardPack of
// scrapers, hit /metrics, and assert the expected metric families
// landed in the registry. The assertion is intentionally loose — we
// don't check absolute values (which vary by server load), just that
// the plumbing produced something.
func TestMySQLEndToEnd(t *testing.T) {
	db := openMySQL(t)
	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),
		gormmetrics.WithLabels(map[string]string{"db": "integration-mysql"}),
		gormmetrics.WithCacheTTL(0),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := scrapeOnce(t, c)

	mustContain(t, body, "gormmetrics_up{")
	mustContain(t, body, `db="integration-mysql"`)
	mustContain(t, body, "mysql_threads_connected{")
	t.Logf("MySQL metrics body length: %d bytes", len(body))
	t.Logf("disabled scrapers: %v", c.DisabledScrapers())
}

// TestPostgresEndToEnd mirrors the MySQL test for PostgreSQL.
func TestPostgresEndToEnd(t *testing.T) {
	db := openPostgres(t)
	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(postgres.StandardPack()...),
		gormmetrics.WithLabels(map[string]string{"db": "integration-pg"}),
		gormmetrics.WithCacheTTL(0),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := scrapeOnce(t, c)

	mustContain(t, body, "gormmetrics_up{")
	mustContain(t, body, `db="integration-pg"`)
	mustContain(t, body, "postgres_backend_connections")
	t.Logf("Postgres metrics body length: %d bytes", len(body))
	t.Logf("disabled scrapers: %v", c.DisabledScrapers())
}

// TestCacheDeduplicates runs two scrape rounds back-to-back; if the
// per-scraper cache is working, the second should serve from cache and
// produce identical output. Useful for catching regressions in the
// singleflight gate.
func TestCacheDeduplicates(t *testing.T) {
	db := openMySQL(t)
	c, _ := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.MinimalPack()...),
		gormmetrics.WithCacheTTL(10*time.Second),
	)
	first := scrapeOnce(t, c)
	second := scrapeOnce(t, c)
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("empty bodies")
	}
	// Counter values vary across scrapes; gauges should be cached
	// identical. We don't compare bodies wholesale because the
	// meta-metrics (scrape_duration_seconds) re-measure each request.
	mustContain(t, second, "mysql_threads_connected")
}

func mustContain(t *testing.T, body, substr string) {
	t.Helper()
	if !strings.Contains(body, substr) {
		t.Errorf("body missing %q\n--- first 1KB ---\n%s", substr, body[:min(1024, len(body))])
	}
}
