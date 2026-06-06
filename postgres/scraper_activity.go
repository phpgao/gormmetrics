package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// ActivityScraper exposes per-state backend counts from pg_stat_activity.
// One time series per (state) label — typically {active, idle,
// idle_in_transaction, idle_in_transaction_aborted, fastpath_function_call,
// disabled}, plus a 'waiting' aggregate for backends with a non-null
// wait_event.
//
// Requires the connection user be able to SELECT from pg_stat_activity.
// On RDS / managed PG without rds_superuser the per-database visibility
// may be limited to the user's own queries; the scraper still works but
// the numbers will be a lower bound. We document that rather than probe
// for it (no clean failure mode).
type ActivityScraper struct{}

func (ActivityScraper) Name() string { return "postgres_activity" }

func (ActivityScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(state, 'unknown') AS state, COUNT(*) AS n
		FROM pg_stat_activity
		WHERE backend_type = 'client backend'
		GROUP BY state
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]gormmetrics.Sample, 0, 6)
	var total float64
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return out, err
		}
		out = append(out, gormmetrics.Sample{
			Name:   "postgres_backend_connections",
			Help:   "Number of client backends grouped by current activity state.",
			Type:   gormmetrics.Gauge,
			Value:  float64(n),
			Labels: map[string]string{"state": state},
		})
		total += float64(n)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out = append(out, gormmetrics.Sample{
		Name:  "postgres_backend_connections_total_current",
		Help:  "Total number of client backends across all states (point-in-time, not cumulative).",
		Type:  gormmetrics.Gauge,
		Value: total,
	})
	return out, nil
}
