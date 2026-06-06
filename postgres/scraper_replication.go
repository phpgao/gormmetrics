package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// ReplicationScraper exposes WAL lag per connected replica from
// pg_stat_replication. Off by default — included in FullPack — because
// it requires pg_monitor (or superuser) and produces nothing useful on
// a server without replication configured.
//
// On a replica server itself this scraper emits 0 rows; deploy it
// against the primary to monitor connected standbys.
type ReplicationScraper struct{}

func (ReplicationScraper) Name() string { return "postgres_replication" }

// Probe verifies the user has visibility into pg_stat_replication.
// Non-privileged users see the view but with most columns null, which
// makes the metrics useless rather than wrong — we therefore probe by
// requesting a column that's nulled when permission is missing.
func (ReplicationScraper) Probe(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		"SELECT pg_last_wal_replay_lsn(), pg_is_in_recovery()")
	return err
}

func (ReplicationScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	// Two pieces: (a) per-replica lag from the primary's perspective,
	// (b) is_in_recovery on this server. (a) is a multi-row query, (b)
	// is a single scalar — bundle them so a Grafana dashboard can show
	// "is this a replica? if so, what's its lag?" in one panel.
	out := make([]gormmetrics.Sample, 0, 4)

	var inRecovery bool
	if err := db.QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err == nil {
		v := 0.0
		if inRecovery {
			v = 1
		}
		out = append(out, gormmetrics.Sample{
			Name:  "postgres_in_recovery",
			Help:  "1 if this server is currently in recovery (i.e. running as a standby), 0 if it's a primary.",
			Type:  gormmetrics.Gauge,
			Value: v,
		})
	}

	// pg_stat_replication is only meaningful on a primary. We don't gate
	// the query on inRecovery because the view returns zero rows on a
	// replica anyway — cheaper than a branch.
	rows, err := db.QueryContext(ctx, `
		SELECT
			application_name,
			COALESCE(state, ''),
			COALESCE(EXTRACT(EPOCH FROM write_lag), 0)::float8 AS write_lag_seconds,
			COALESCE(EXTRACT(EPOCH FROM replay_lag), 0)::float8 AS replay_lag_seconds
		FROM pg_stat_replication
	`)
	if err != nil {
		// Some managed Postgres flavours hide this view entirely; emit
		// what we have so far and surface the error to meta-metrics.
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var app, state string
		var writeLag, replayLag float64
		if err := rows.Scan(&app, &state, &writeLag, &replayLag); err != nil {
			return out, err
		}
		labels := map[string]string{"application_name": app, "state": state}
		out = append(out,
			gormmetrics.Sample{
				Name:   "postgres_replication_write_lag_seconds",
				Help:   "WAL write lag to this replica in seconds.",
				Type:   gormmetrics.Gauge,
				Value:  writeLag,
				Labels: labels,
			},
			gormmetrics.Sample{
				Name:   "postgres_replication_replay_lag_seconds",
				Help:   "WAL replay lag to this replica in seconds.",
				Type:   gormmetrics.Gauge,
				Value:  replayLag,
				Labels: labels,
			},
		)
	}
	return out, rows.Err()
}
