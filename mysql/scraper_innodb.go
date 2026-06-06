package mysql

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// InnoDBScraper covers the InnoDB storage engine fundamentals: buffer
// pool occupancy, page churn, row-level CRUD throughput, and lock
// contention. All values come from SHOW GLOBAL STATUS so no
// information_schema grants are required.
//
// We deliberately keep this one scraper covering the whole InnoDB
// surface area rather than splitting into pool / rows / locks files —
// they share the same SHOW STATUS round-trip and splitting would multiply
// network cost without operator benefit.
type InnoDBScraper struct{}

func (InnoDBScraper) Name() string { return "mysql_innodb" }

// Probe verifies the connection account can read InnoDB status. SHOW
// GLOBAL STATUS on Innodb_* requires the PROCESS privilege on MySQL 5.7+;
// without it the query silently returns an empty result rather than
// erroring, so we issue a non-empty probe to detect this case decisively.
//
// On probe failure the core framework disables this scraper for the
// process lifetime and exposes gormmetrics_scraper_disabled{scraper="mysql_innodb"},
// rather than spamming ERROR logs on every scrape interval.
func (InnoDBScraper) Probe(ctx context.Context, db *sql.DB) error {
	var name, val string
	row := db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Innodb_buffer_pool_pages_total'")
	err := row.Scan(&name, &val)
	switch {
	case err == sql.ErrNoRows:
		// The variable exists on every InnoDB-enabled server. No rows
		// means the user lacks PROCESS (5.7+ filters silently).
		return errInsufficientPrivilege
	case err != nil:
		return err
	}
	return nil
}

// errInsufficientPrivilege drives the meta-metric "permission_denied"
// classification — the framework picks up the string contents.
type insufficientPrivErr struct{}

func (insufficientPrivErr) Error() string {
	return "permission denied: SHOW GLOBAL STATUS for Innodb_* returned no rows (PROCESS grant required on MySQL 5.7+)"
}

var errInsufficientPrivilege = insufficientPrivErr{}

var innodbVars = []string{
	// Buffer pool occupancy
	"Innodb_buffer_pool_pages_total",
	"Innodb_buffer_pool_pages_free",
	"Innodb_buffer_pool_pages_data",
	"Innodb_buffer_pool_pages_dirty",
	"Innodb_buffer_pool_pages_misc",
	// Buffer pool I/O throughput
	"Innodb_buffer_pool_read_requests",
	"Innodb_buffer_pool_reads",
	"Innodb_buffer_pool_write_requests",
	"Innodb_buffer_pool_pages_flushed",
	// Row-level CRUD
	"Innodb_rows_read",
	"Innodb_rows_inserted",
	"Innodb_rows_updated",
	"Innodb_rows_deleted",
	// Lock pressure
	"Innodb_row_lock_waits",
	"Innodb_row_lock_time",
	"Innodb_row_lock_current_waits",
	// Disk I/O
	"Innodb_data_read",
	"Innodb_data_written",
	"Innodb_data_fsyncs",
	// Redo log
	"Innodb_log_writes",
	"Innodb_log_waits",
}

// innodbMeta classifies each variable so the scraper knows whether to
// emit it as a Counter (monotonic) or Gauge (point-in-time). Keeping
// the metadata in one table makes it easy to audit.
var innodbMeta = map[string]struct {
	metric string
	typ    gormmetrics.MetricType
	help   string
}{
	// Same Help text on every "_pages" variant — Prometheus rejects a
	// metric family whose samples disagree on Help. We use one unified
	// description; the 'state' label distinguishes total/free/dirty/etc.
	"Innodb_buffer_pool_pages_total":    {"mysql_innodb_buffer_pool_pages", gormmetrics.Gauge, "InnoDB buffer pool pages, partitioned by state (total/free/data/dirty/misc)."},
	"Innodb_buffer_pool_pages_free":     {"mysql_innodb_buffer_pool_pages", gormmetrics.Gauge, "InnoDB buffer pool pages, partitioned by state (total/free/data/dirty/misc)."},
	"Innodb_buffer_pool_pages_data":     {"mysql_innodb_buffer_pool_pages", gormmetrics.Gauge, "InnoDB buffer pool pages, partitioned by state (total/free/data/dirty/misc)."},
	"Innodb_buffer_pool_pages_dirty":    {"mysql_innodb_buffer_pool_pages", gormmetrics.Gauge, "InnoDB buffer pool pages, partitioned by state (total/free/data/dirty/misc)."},
	"Innodb_buffer_pool_pages_misc":     {"mysql_innodb_buffer_pool_pages", gormmetrics.Gauge, "InnoDB buffer pool pages, partitioned by state (total/free/data/dirty/misc)."},
	"Innodb_buffer_pool_read_requests":  {"mysql_innodb_buffer_pool_read_requests_total", gormmetrics.Counter, "Logical read requests against the InnoDB buffer pool."},
	"Innodb_buffer_pool_reads":          {"mysql_innodb_buffer_pool_reads_total", gormmetrics.Counter, "Physical reads that couldn't be satisfied from the InnoDB buffer pool."},
	"Innodb_buffer_pool_write_requests": {"mysql_innodb_buffer_pool_write_requests_total", gormmetrics.Counter, "Writes done to the InnoDB buffer pool."},
	"Innodb_buffer_pool_pages_flushed":  {"mysql_innodb_buffer_pool_pages_flushed_total", gormmetrics.Counter, "Requests to flush dirty pages from the InnoDB buffer pool."},
	"Innodb_rows_read":                  {"mysql_innodb_rows_read_total", gormmetrics.Counter, "Rows read from InnoDB tables."},
	"Innodb_rows_inserted":              {"mysql_innodb_rows_inserted_total", gormmetrics.Counter, "Rows inserted into InnoDB tables."},
	"Innodb_rows_updated":               {"mysql_innodb_rows_updated_total", gormmetrics.Counter, "Rows updated in InnoDB tables."},
	"Innodb_rows_deleted":               {"mysql_innodb_rows_deleted_total", gormmetrics.Counter, "Rows deleted from InnoDB tables."},
	"Innodb_row_lock_waits":             {"mysql_innodb_row_lock_waits_total", gormmetrics.Counter, "Number of times operations on InnoDB tables had to wait for a row lock."},
	"Innodb_row_lock_time":              {"mysql_innodb_row_lock_time_milliseconds_total", gormmetrics.Counter, "Total time spent waiting for InnoDB row locks, in milliseconds."},
	"Innodb_row_lock_current_waits":     {"mysql_innodb_row_lock_current_waits", gormmetrics.Gauge, "Number of row locks currently being waited for."},
	"Innodb_data_read":                  {"mysql_innodb_data_read_bytes_total", gormmetrics.Counter, "Total bytes read by InnoDB (physical I/O)."},
	"Innodb_data_written":               {"mysql_innodb_data_written_bytes_total", gormmetrics.Counter, "Total bytes written by InnoDB."},
	"Innodb_data_fsyncs":                {"mysql_innodb_data_fsyncs_total", gormmetrics.Counter, "Total fsync() calls done by InnoDB."},
	"Innodb_log_writes":                 {"mysql_innodb_log_writes_total", gormmetrics.Counter, "Writes to the InnoDB redo log."},
	"Innodb_log_waits":                  {"mysql_innodb_log_waits_total", gormmetrics.Counter, "Times the redo log buffer was full and a wait was required before continuing."},
}

// bufferPoolStateLabel maps the per-state buffer pool variables to a
// 'state' label value, so they fan out under a single metric name. This
// produces a more useful Grafana view than five sibling metrics.
var bufferPoolStateLabel = map[string]string{
	"Innodb_buffer_pool_pages_total": "total",
	"Innodb_buffer_pool_pages_free":  "free",
	"Innodb_buffer_pool_pages_data":  "data",
	"Innodb_buffer_pool_pages_dirty": "dirty",
	"Innodb_buffer_pool_pages_misc":  "misc",
}

func (s InnoDBScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	vals, err := fetchStatus(ctx, db, innodbVars)
	if err != nil {
		return nil, err
	}
	out := make([]gormmetrics.Sample, 0, len(innodbVars))
	// Iterate over innodbVars slice for deterministic output order,
	// not the vals map which has undefined iteration order.
	for _, name := range innodbVars {
		v, ok := vals[name]
		if !ok {
			continue
		}
		meta := innodbMeta[name]
		var labels map[string]string
		if state, ok := bufferPoolStateLabel[name]; ok {
			labels = map[string]string{"state": state}
		}
		out = append(out, gormmetrics.Sample{
			Name:   meta.metric,
			Help:   meta.help,
			Type:   meta.typ,
			Value:  v,
			Labels: labels,
		})
	}
	return out, nil
}
