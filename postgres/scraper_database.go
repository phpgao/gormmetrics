package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/userdef"
)

// DatabaseScraper exposes per-database counters from pg_stat_database:
// transaction counts, query counts, conflict counts, deadlocks, and
// cache hit ratio inputs. These are the canonical "PostgreSQL health"
// metrics and every Grafana PG dashboard reads them.
//
// pg_stat_database is readable by any connected user (the row for
// databases you can't CONNECT to is exposed with limited columns), so no
// special grant is required.
type DatabaseScraper struct{}

func (DatabaseScraper) Name() string { return "postgres_database" }

// dbStatVars enumerates the columns we ship as metrics. Ordering doesn't
// matter — we look up the metric metadata by column name.
var dbStatColumns = []string{
	"xact_commit", "xact_rollback",
	"blks_read", "blks_hit",
	"tup_returned", "tup_fetched",
	"tup_inserted", "tup_updated", "tup_deleted",
	"conflicts", "deadlocks",
	"temp_files", "temp_bytes",
	"numbackends",
}

type dbColMeta struct {
	metric string
	typ    gormmetrics.MetricType
	help   string
}

var dbColMetas = map[string]dbColMeta{
	"xact_commit":   {"postgres_xact_commit_total", gormmetrics.Counter, "Transactions committed."},
	"xact_rollback": {"postgres_xact_rollback_total", gormmetrics.Counter, "Transactions rolled back."},
	"blks_read":     {"postgres_blocks_read_total", gormmetrics.Counter, "Disk blocks read in this database."},
	"blks_hit":      {"postgres_blocks_hit_total", gormmetrics.Counter, "Disk blocks satisfied from the buffer cache."},
	"tup_returned":  {"postgres_tuples_returned_total", gormmetrics.Counter, "Rows returned by queries (incl. sequential scan rejects)."},
	"tup_fetched":   {"postgres_tuples_fetched_total", gormmetrics.Counter, "Rows fetched by queries."},
	"tup_inserted":  {"postgres_tuples_inserted_total", gormmetrics.Counter, "Rows inserted."},
	"tup_updated":   {"postgres_tuples_updated_total", gormmetrics.Counter, "Rows updated."},
	"tup_deleted":   {"postgres_tuples_deleted_total", gormmetrics.Counter, "Rows deleted."},
	"conflicts":     {"postgres_conflicts_total", gormmetrics.Counter, "Queries canceled due to recovery conflicts on a standby."},
	"deadlocks":     {"postgres_deadlocks_total", gormmetrics.Counter, "Deadlocks detected."},
	"temp_files":    {"postgres_temp_files_total", gormmetrics.Counter, "Temp files created by queries."},
	"temp_bytes":    {"postgres_temp_bytes_total", gormmetrics.Counter, "Total bytes written to temp files."},
	"numbackends":   {"postgres_backends", gormmetrics.Gauge, "Backends currently connected to this database."},
}

func (DatabaseScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	// We assemble the SELECT column list dynamically — easier to maintain
	// than a giant fixed string when the metric metadata is the source
	// of truth.
	cols := "datname"
	for _, c := range dbStatColumns {
		cols += ", " + c
	}
	rows, err := db.QueryContext(ctx,
		"SELECT "+cols+" FROM pg_stat_database WHERE datname IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]gormmetrics.Sample, 0, len(dbStatColumns)*4)

	values := make([]interface{}, 1+len(dbStatColumns))
	scanDest := make([]interface{}, 1+len(dbStatColumns))
	for i := range values {
		scanDest[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return out, err
		}
		datname, _ := values[0].(string)
		if datname == "" {
			if b, ok := values[0].([]byte); ok {
				datname = string(b)
			}
		}
		labels := map[string]string{"datname": datname}
		for i, c := range dbStatColumns {
			v, ok := userdef.ToFloat(values[1+i])
			if !ok {
				continue
			}
			meta := dbColMetas[c]
			out = append(out, gormmetrics.Sample{
				Name:   meta.metric,
				Help:   meta.help,
				Type:   meta.typ,
				Value:  v,
				Labels: labels,
			})
		}
	}
	return out, rows.Err()
}
