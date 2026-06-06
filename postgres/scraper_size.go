package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// SizeScraper exposes pg_database_size() per database. Cheap query (a
// metadata lookup, not a table scan) so safe to include in StandardPack.
//
// Requires CONNECT on each database queried — Postgres silently omits
// rows for databases the user can't connect to.
type SizeScraper struct{}

func (SizeScraper) Name() string { return "postgres_database_size" }

func (SizeScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT datname, pg_database_size(datname) FROM pg_database WHERE datname NOT LIKE 'template%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]gormmetrics.Sample, 0, 8)
	for rows.Next() {
		var datname string
		var size sql.NullInt64
		if err := rows.Scan(&datname, &size); err != nil {
			return out, err
		}
		if !size.Valid {
			continue // user can't see this database's size
		}
		out = append(out, gormmetrics.Sample{
			Name:   "postgres_database_size_bytes",
			Help:   "On-disk size of the database in bytes (from pg_database_size).",
			Type:   gormmetrics.Gauge,
			Value:  float64(size.Int64),
			Labels: map[string]string{"datname": datname},
		})
	}
	return out, rows.Err()
}
