package postgres

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// TableStatScraper exposes per-table activity from pg_stat_user_tables.
// Cardinality scales with table count — on schemas with thousands of
// tables this can dominate scrape size, which is why it's only in
// FullPack and not Standard.
//
// Requires either ownership of the queried tables, pg_read_all_stats
// (PG 10+), or table-level SELECT. Without the privilege the query
// succeeds but returns zero rows for inaccessible tables.
type TableStatScraper struct {
	// SchemaFilter restricts to a SQL pattern (LIKE syntax). Empty means
	// "public schema only" — the safer default for general-purpose use.
	SchemaFilter string
}

func (TableStatScraper) Name() string { return "postgres_table_stat" }

func (s TableStatScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	schema := s.SchemaFilter
	if schema == "" {
		schema = "public"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT schemaname, relname,
			seq_scan, seq_tup_read,
			idx_scan, idx_tup_fetch,
			n_tup_ins, n_tup_upd, n_tup_del,
			n_live_tup, n_dead_tup
		FROM pg_stat_user_tables
		WHERE schemaname LIKE $1
	`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]gormmetrics.Sample, 0, 64)
	for rows.Next() {
		var schemaname, relname string
		var seqScan, seqTupRead, idxScan, idxTupFetch sql.NullInt64
		var nIns, nUpd, nDel, nLive, nDead sql.NullInt64
		if err := rows.Scan(&schemaname, &relname,
			&seqScan, &seqTupRead, &idxScan, &idxTupFetch,
			&nIns, &nUpd, &nDel, &nLive, &nDead); err != nil {
			return out, err
		}
		labels := map[string]string{"schemaname": schemaname, "relname": relname}

		add := func(name, help string, t gormmetrics.MetricType, v sql.NullInt64) {
			if !v.Valid {
				return
			}
			out = append(out, gormmetrics.Sample{
				Name: name, Help: help, Type: t, Value: float64(v.Int64), Labels: labels,
			})
		}
		add("postgres_table_seq_scan_total", "Sequential scans on the table.", gormmetrics.Counter, seqScan)
		add("postgres_table_seq_tup_read_total", "Live rows read by sequential scans.", gormmetrics.Counter, seqTupRead)
		add("postgres_table_idx_scan_total", "Index scans on the table.", gormmetrics.Counter, idxScan)
		add("postgres_table_idx_tup_fetch_total", "Live rows fetched by index scans.", gormmetrics.Counter, idxTupFetch)
		add("postgres_table_tup_inserted_total", "Rows inserted.", gormmetrics.Counter, nIns)
		add("postgres_table_tup_updated_total", "Rows updated.", gormmetrics.Counter, nUpd)
		add("postgres_table_tup_deleted_total", "Rows deleted.", gormmetrics.Counter, nDel)
		add("postgres_table_live_tuples", "Estimated live rows.", gormmetrics.Gauge, nLive)
		add("postgres_table_dead_tuples", "Estimated dead rows.", gormmetrics.Gauge, nDead)
	}
	return out, rows.Err()
}
