package gormmetrics

import (
	"context"
	"database/sql"
)

// Scraper is the unit of metric collection. Each Scrape call is invoked at
// most once per Collector cache window (default 10s) regardless of how many
// concurrent /metrics requests arrive.
//
// Implementations must be safe for concurrent use only insofar as Scrape
// itself may be invoked from multiple goroutines serially across cache
// windows; the framework guarantees no overlapping invocations within a
// single window via singleflight.
//
// Returning a non-nil error does NOT abort sibling scrapers. The error is
// recorded against the scraper's Name() label in the gormmetrics_scrape_*
// meta-metrics family and any successfully-returned Samples are still
// emitted (partial success is honored).
type Scraper interface {
	// Name uniquely identifies the scraper. Used as a label value on
	// meta-metrics; must be stable across process restarts and
	// follow Prometheus label-value conventions.
	Name() string

	// Scrape executes the underlying query (or filesystem probe, etc.)
	// and returns zero or more Samples plus an optional error.
	Scrape(ctx context.Context, db *sql.DB) ([]Sample, error)
}

// ProbingScraper is an optional interface a Scraper can implement to advise
// the Collector that some preliminary check (privilege probe, version
// sniff, etc.) should run once at registration time. If Probe returns a
// non-nil error the scraper is excluded from subsequent scrape rounds and
// the error is surfaced via gormmetrics_scrape_disabled{scraper=...}.
//
// Scrapers that don't implement this interface are assumed always ready.
type ProbingScraper interface {
	Probe(ctx context.Context, db *sql.DB) error
}
