package mysql

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// QueryLatencyScraper emits a histogram of statement latency derived from
// performance_schema.events_statements_summary_global_by_event_name.
// This is the only Histogram-emitting scraper in the default catalog —
// MySQL's pre-bucketed performance_schema buckets translate naturally to
// Prometheus histograms without sampling.
//
// Requires the performance_schema to be enabled (default since 5.6.6)
// and SELECT on performance_schema.* (default since 5.7).
//
// Off by default. Include explicitly with FullPack() or by appending the
// scraper directly. Probed to fail-shut on unavailable schema.
type QueryLatencyScraper struct{}

func (QueryLatencyScraper) Name() string { return "mysql_query_latency" }

// Probe checks the table is accessible. Failure disables the scraper
// rather than producing per-scrape errors.
func (QueryLatencyScraper) Probe(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		"SELECT 1 FROM performance_schema.events_statements_summary_global_by_event_name LIMIT 1")
	return err
}

// MySQL latency buckets are in picoseconds; convert to seconds when emitting.
const picoToSec = 1e-12

// statementBuckets is the bucket set used by performance_schema's wait
// time histograms (BUCKET_NUMBER 0..49). We use a coarse subset
// well-suited to Grafana heatmaps without explosion.
var statementBuckets = []float64{
	0.0001, 0.001, 0.01, 0.1, 0.5, 1, 5, 10, 60,
}

// Scrape produces a single histogram named mysql_query_latency_seconds
// covering all statement digests in aggregate. Per-digest breakdown is
// available but explodes cardinality; users who need it should write a
// userdef.SQLHistogram with their own query and label set.
func (QueryLatencyScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	// performance_schema gives us COUNT_STAR (n observations) and
	// SUM_TIMER_WAIT (picoseconds). For real per-bucket counts we'd need
	// to query events_statements_histogram_global, which exists since
	// 8.0.3. Try that first, fall back to a single-bucket approximation.
	const histQuery = `
		SELECT BUCKET_TIMER_LOW, BUCKET_TIMER_HIGH, COUNT_BUCKET_AND_LOWER
		FROM performance_schema.events_statements_histogram_global
		ORDER BY BUCKET_NUMBER
	`
	rows, err := db.QueryContext(ctx, histQuery)
	if err == nil {
		defer rows.Close()
		return scrapeStatementHistogram(rows)
	}
	// 8.0.3 histogram table not available; fall back to a coarse derivation
	// from the global summary which at least gives us count and sum.
	return scrapeStatementSummary(ctx, db)
}

func scrapeStatementHistogram(rows *sql.Rows) ([]gormmetrics.Sample, error) {
	buckets := map[float64]uint64{}
	var totalCount uint64
	for rows.Next() {
		var low, high, cumCount uint64
		if err := rows.Scan(&low, &high, &cumCount); err != nil {
			return nil, err
		}
		highSec := float64(high) * picoToSec
		// Snap the picosecond upper bound to one of our coarse buckets;
		// when multiple raw buckets fall into the same coarse bucket the
		// higher cumCount wins (cumulative counts are monotonic).
		for _, b := range statementBuckets {
			if highSec <= b {
				if cumCount > buckets[b] {
					buckets[b] = cumCount
				}
				break
			}
		}
		if cumCount > totalCount {
			totalCount = cumCount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []gormmetrics.Sample{{
		Name:             "mysql_query_latency_seconds",
		Help:             "Aggregated statement latency histogram derived from performance_schema.",
		Type:             gormmetrics.Histogram,
		HistogramBuckets: buckets,
		HistogramCount:   totalCount,
		// We deliberately don't compute Sum here — events_statements_histogram_global
		// doesn't expose it; users get count/quantiles which is what they actually want.
		HistogramSum: 0,
	}}, nil
}

func scrapeStatementSummary(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	// Aggregate summary across all event names.
	row := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(COUNT_STAR),0), COALESCE(SUM(SUM_TIMER_WAIT),0)
		FROM performance_schema.events_statements_summary_global_by_event_name
		WHERE EVENT_NAME LIKE 'statement/%'
	`)
	var count uint64
	var sumPico uint64
	if err := row.Scan(&count, &sumPico); err != nil {
		return nil, err
	}
	// Single "+Inf" bucket — no distribution, just count + sum. Better than nothing.
	return []gormmetrics.Sample{{
		Name:             "mysql_query_latency_seconds",
		Help:             "Aggregated statement latency from performance_schema.events_statements_summary_global_by_event_name (count + sum only; install MySQL 8.0.3+ for full histogram).",
		Type:             gormmetrics.Histogram,
		HistogramBuckets: map[float64]uint64{},
		HistogramCount:   count,
		HistogramSum:     float64(sumPico) * picoToSec,
	}}, nil
}
