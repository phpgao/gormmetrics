// Example: SQLite + custom collector. SQLite has no "server stats" to
// scrape — instead we show the canonical embedded use case: file size,
// page count from PRAGMA, and table row counts.
//
// Requires github.com/mattn/go-sqlite3 (CGO) — uncomment the driver
// import if you have CGO enabled; otherwise switch to a pure-Go SQLite
// driver like modernc.org/sqlite.
//
//	go run ./examples/sqlite
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	// _ "github.com/mattn/go-sqlite3"  // requires CGO
	// _ "modernc.org/sqlite"           // pure Go alternative

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/userdef"
)

func main() {
	dbPath := os.Getenv("SQLITE_PATH")
	if dbPath == "" {
		dbPath = "/tmp/example.db"
	}

	// driver name depends on which SQLite import you uncommented above
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithLabels(map[string]string{"db_file": dbPath}),

		// PRAGMA queries — these are SQLite's "server stats" equivalent.
		// page_count * page_size = on-disk size; freelist_count tells
		// you how much can be reclaimed by VACUUM.
		gormmetrics.WithScrapers(&userdef.SQLGauge{
			MetricName: "sqlite_page_count",
			Help:       "Number of pages in the SQLite database file.",
			Query:      "PRAGMA page_count",
		}),
		gormmetrics.WithScrapers(&userdef.SQLGauge{
			MetricName: "sqlite_page_size_bytes",
			Help:       "Page size in bytes.",
			Query:      "PRAGMA page_size",
		}),
		gormmetrics.WithScrapers(&userdef.SQLGauge{
			MetricName: "sqlite_freelist_pages",
			Help:       "Pages on the freelist (reclaimable by VACUUM).",
			Query:      "PRAGMA freelist_count",
		}),

		// File-system probe via FuncScraper — bypasses SQLite entirely.
		gormmetrics.WithScrapers(&userdef.FuncScraper{
			ID:   "sqlite_file_size_bytes",
			Help: "Size of the SQLite DB file on disk (bytes).",
			Collect: func(_ context.Context, _ *sql.DB) ([]gormmetrics.Sample, error) {
				fi, err := os.Stat(dbPath)
				if err != nil {
					return nil, err
				}
				return []gormmetrics.Sample{{
					Name:  "sqlite_file_size_bytes",
					Type:  gormmetrics.Gauge,
					Value: float64(fi.Size()),
				}}, nil
			},
		}),

		// Per-table row counts. SQLITE_MASTER lists all user tables; we
		// could also hard-code the SELECT for each table individually.
		gormmetrics.WithScrapers(&userdef.SQLLabeled{
			MetricName:   "sqlite_table_rows",
			Help:         "Approximate row count per table.",
			Query:        "SELECT name, (SELECT COUNT(*) FROM main.sqlite_master m2 WHERE m2.tbl_name = m1.name AND m2.type = 'table') AS rows FROM main.sqlite_master m1 WHERE type='table'",
			Type:         gormmetrics.Gauge,
			LabelColumns: []string{"name"},
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/metrics", c.Handler())
	log.Println("serving /metrics on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
