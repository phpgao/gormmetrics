// Example: choosing a scrape level. Demonstrates the three-tier preset
// model and how to mix presets with hand-picked scrapers when the
// defaults don't quite fit.
//
// Each backend (mysql, postgres) ships MinimalPack / StandardPack /
// FullPack — strict supersets. Picking a level is almost always the
// right move; the manual cherry-pick path exists for edge cases like
// "I want Standard but skip InnoDB because PROCESS is denied" or
// "Standard plus the Full-only ReplicationScraper".
//
//	LEVEL=standard MYSQL_DSN='...' go run ./examples/scrape_levels
//
// LEVEL ∈ {minimal, standard, full, custom}.
package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/go-sql-driver/mysql"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
)

func pickScrapers(level string) []gormmetrics.Scraper {
	switch level {
	case "minimal":
		// MinimalPack: just enough to alert on connection health.
		// Works against any MySQL account regardless of grants.
		return mysql.MinimalPack()

	case "standard":
		// StandardPack: connection health + traffic + InnoDB internals.
		// Recommended default for production dashboards. Scrapers that
		// need PROCESS auto-disable themselves if the privilege is
		// missing — no log noise, exposed via gormmetrics_scraper_disabled.
		return mysql.StandardPack()

	case "full":
		// FullPack: everything in Standard plus replication state and
		// the per-statement performance_schema histogram. Replication
		// requires REPLICATION CLIENT; the perf_schema histogram
		// requires SELECT on performance_schema.
		return mysql.FullPack()

	case "custom":
		// Mix-and-match. Start from StandardPack and add the
		// Full-only ReplicationScraper — convenient pattern when you
		// want most of Full but can opt out of the perf_schema
		// histogram cost.
		s := mysql.StandardPack()
		s = append(s, &mysql.ReplicationScraper{})
		return s

	default:
		log.Fatalf("unknown LEVEL %q (expected minimal/standard/full/custom)", level)
		return nil
	}
}

func main() {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		log.Fatal("set MYSQL_DSN")
	}
	level := os.Getenv("LEVEL")
	if level == "" {
		level = "standard"
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	defer db.Close()

	scrapers := pickScrapers(level)
	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(scrapers...),
		gormmetrics.WithLabels(map[string]string{"level": level}),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Report which scrapers ended up registered (and which got
	// disabled by failed probes) — handy when troubleshooting
	// permission setups.
	log.Printf("level=%q registered=%d scrapers", level, len(c.Scrapers()))
	if disabled := c.DisabledScrapers(); len(disabled) > 0 {
		log.Printf("disabled by probe: %v", disabled)
	}

	http.Handle("/metrics", c.Handler())
	log.Println("serving /metrics on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
