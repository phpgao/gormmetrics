package mysql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/phpgao/gormmetrics"
)

// TrafficScraper exposes high-level workload counters: total queries,
// slow queries, network bytes, and the four canonical Com_* per-command
// counters that drive any "QPS by operation" dashboard.
//
// One combined query is issued rather than two, so that all values are
// read from the same point-in-time snapshot of global status.
type TrafficScraper struct{}

func (TrafficScraper) Name() string { return "mysql_traffic" }

// fixedTrafficVars are the non-Com_* names we always want. Kept tiny so
// the WHERE IN list stays cheap on the server side.
var fixedTrafficVars = []string{
	"Queries", "Questions", "Slow_queries",
	"Bytes_received", "Bytes_sent",
	"Created_tmp_tables", "Created_tmp_disk_tables",
}

// comInterestingCommands limits the Com_* fan-out to commands that
// actually have observability value. SHOW STATUS exposes hundreds of
// Com_* counters (Com_show_*, Com_admin_*, etc.) that are noise on a
// dashboard; we pre-filter to the ones a typical operator cares about.
var comInterestingCommands = map[string]struct{}{
	"Com_select": {}, "Com_insert": {}, "Com_update": {}, "Com_delete": {},
	"Com_commit": {}, "Com_rollback": {},
	"Com_replace": {}, "Com_insert_select": {}, "Com_update_multi": {},
	"Com_delete_multi": {}, "Com_ddl": {},
}

func (s TrafficScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	// Single query: combine fixed vars + Com_* pattern in one round-trip
	// so all values come from the same status snapshot.
	fixed, com, err := fetchTraffic(ctx, db)
	if err != nil {
		return nil, err
	}

	out := make([]gormmetrics.Sample, 0, 16)

	helps := map[string]string{
		"Queries":                 "Total number of statements executed by the server (excluding stored procedures).",
		"Questions":               "Total number of statements executed by clients (excludes server-side replication and stored procedures).",
		"Slow_queries":            "Number of queries that have taken more than long_query_time seconds.",
		"Bytes_received":          "Bytes received from all clients.",
		"Bytes_sent":              "Bytes sent to all clients.",
		"Created_tmp_tables":      "Internal temporary tables created in memory.",
		"Created_tmp_disk_tables": "Internal temporary tables converted to on-disk storage.",
	}
	for _, v := range fixedTrafficVars {
		val, ok := fixed[v]
		if !ok {
			continue
		}
		out = append(out, makeCounter("mysql_"+strings.ToLower(v)+"_total", helps[v], val, nil))
	}

	for name, val := range com {
		cmd := strings.TrimPrefix(name, "Com_")
		out = append(out, makeCounter(
			"mysql_commands_total",
			"Total number of statements executed, partitioned by command.",
			val,
			map[string]string{"command": cmd},
		))
	}
	return out, nil
}

// fetchTraffic issues a single SHOW GLOBAL STATUS query that captures both
// the fixed traffic variables and all Com_* variables in one snapshot.
func fetchTraffic(ctx context.Context, db *sql.DB) (map[string]float64, map[string]float64, error) {
	// Build: WHERE Variable_name IN (...) OR Variable_name LIKE 'Com_%'
	placeholders := "?" + strings.Repeat(",?", len(fixedTrafficVars)-1)
	args := make([]any, len(fixedTrafficVars))
	for i, n := range fixedTrafficVars {
		args[i] = n
	}
	q := "SHOW GLOBAL STATUS WHERE Variable_name IN (" + placeholders + ") OR Variable_name LIKE 'Com_%'"

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	fixed := make(map[string]float64, len(fixedTrafficVars))
	com := make(map[string]float64)

	for rows.Next() {
		var name, val string
		if err := rows.Scan(&name, &val); err != nil {
			return nil, nil, err
		}
		f, ok := mustFloat(val)
		if !ok {
			continue
		}
		if _, ok := comInterestingCommands[name]; ok {
			com[name] = f
		} else {
			fixed[name] = f
		}
	}
	return fixed, com, rows.Err()
}
