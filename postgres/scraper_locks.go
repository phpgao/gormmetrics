package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// LocksScraper exposes lock counts by mode and grant status. Useful for
// detecting contention without paying the cost of subscribing to
// pg_locks via pg_stat_activity joins (which can be expensive on busy
// servers).
//
// Readable by any user — pg_locks is part of pg_catalog.
type LocksScraper struct{}

func (LocksScraper) Name() string { return "postgres_locks" }

func (LocksScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT mode, granted, COUNT(*)
		FROM pg_locks
		GROUP BY mode, granted
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]gormmetrics.Sample, 0, 8)
	for rows.Next() {
		var mode string
		var granted bool
		var n int64
		if err := rows.Scan(&mode, &granted, &n); err != nil {
			return out, err
		}
		grantedLabel := "false"
		if granted {
			grantedLabel = "true"
		}
		out = append(out, gormmetrics.Sample{
			Name:   "postgres_locks",
			Help:   "Number of locks held or awaited, grouped by mode and granted status.",
			Type:   gormmetrics.Gauge,
			Value:  float64(n),
			Labels: map[string]string{"mode": mode, "granted": grantedLabel},
		})
	}
	return out, rows.Err()
}
