package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/userdef"
)

// ReplicationScraper exposes the handful of SHOW REPLICA STATUS columns
// that are alertable: lag in seconds, IO thread up, SQL thread up. It is
// gated behind a Probe that requires the REPLICATION CLIENT (or
// equivalent) grant — fail-open is wrong here because the same query
// silently returns zero rows on a primary, and we want operators to know
// whether they're seeing "primary, no replication" vs "replica, broken".
//
// Off by default; include it explicitly:
//
//	gormmetrics.WithScrapers(append(mysql.StandardPack(), &mysql.ReplicationScraper{})...)
type ReplicationScraper struct {
	// UseLegacySlaveSpelling forces SHOW SLAVE STATUS. Auto-detected
	// when nil — true on MariaDB and MySQL < 8.0.22.
	UseLegacySlaveSpelling *bool

	// Private: set by Probe on first run.
	detectedLegacy bool
}

func (ReplicationScraper) Name() string { return "mysql_replication" }

// Probe verifies the user can run the replication status query at all.
// We don't try to distinguish "no permission" from "not a replica" here —
// either way the scraper has no useful data to emit.
//
// On first run, it auto-detects whether the server prefers the legacy
// SHOW SLAVE STATUS syntax (MariaDB / older MySQL) by observing the
// syntax error from SHOW REPLICA STATUS.
func (s *ReplicationScraper) Probe(ctx context.Context, db *sql.DB) error {
	q := s.statusQuery()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		// Auto-detect: if modern syntax fails with syntax error,
		// retry with legacy spelling and cache the result.
		if s.shouldAutoDetectLegacy(err) {
			s.detectedLegacy = true
			rows, err = db.QueryContext(ctx, "SHOW SLAVE STATUS")
		}
		if err != nil {
			return fmt.Errorf("probe %s: %w", q, err)
		}
	}
	rows.Close()
	return nil
}

// shouldAutoDetectLegacy returns true when the given error suggests the
// server doesn't understand SHOW REPLICA STATUS (MySQL < 8.0.22 or MariaDB).
// We check for syntax error specifically rather than matching the full
// error message string to avoid locale/version fragility.
func (s *ReplicationScraper) shouldAutoDetectLegacy(err error) bool {
	if s.UseLegacySlaveSpelling != nil {
		return false // user forced one spelling; don't auto-detect
	}
	if s.detectedLegacy {
		return false // already resolved
	}
	// "syntax error" is the MySQL error message for unsupported command.
	// Check lowercase to be locale-insensitive.
	return strings.Contains(strings.ToLower(err.Error()), "syntax")
}

func (s ReplicationScraper) statusQuery() string {
	if s.detectedLegacy || (s.UseLegacySlaveSpelling != nil && *s.UseLegacySlaveSpelling) {
		return "SHOW SLAVE STATUS"
	}
	// MySQL 8.0.22+ deprecated SHOW SLAVE STATUS in favour of REPLICA.
	// We default to the modern spelling and let users force legacy on
	// MariaDB.
	return "SHOW REPLICA STATUS"
}

func (s ReplicationScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	q := s.statusQuery()
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		// Try the legacy spelling once before giving up. Useful on
		// MariaDB which never adopted REPLICA.
		if strings.Contains(strings.ToLower(err.Error()), "syntax") {
			rows, err = db.QueryContext(ctx, "SHOW SLAVE STATUS")
		}
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// Primary — no rows is the documented "not a replica" signal.
		return []gormmetrics.Sample{
			{
				Name:  "mysql_replica_is_replica",
				Help:  "1 if this server is acting as a replica (SHOW REPLICA STATUS returned a row), 0 otherwise.",
				Type:  gormmetrics.Gauge,
				Value: 0,
			},
		}, nil
	}

	raw := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}

	out := []gormmetrics.Sample{{
		Name:  "mysql_replica_is_replica",
		Help:  "1 if this server is acting as a replica (SHOW REPLICA STATUS returned a row), 0 otherwise.",
		Type:  gormmetrics.Gauge,
		Value: 1,
	}}

	for i, c := range cols {
		switch c {
		case "Seconds_Behind_Source", "Seconds_Behind_Master":
			if v, ok := userdef.ToFloat(raw[i]); ok {
				out = append(out, gormmetrics.Sample{
					Name:  "mysql_replica_lag_seconds",
					Help:  "Replication lag behind the source/master in seconds. Null on a primary.",
					Type:  gormmetrics.Gauge,
					Value: v,
				})
			}
		case "Replica_IO_Running", "Slave_IO_Running":
			out = append(out, gormmetrics.Sample{
				Name:  "mysql_replica_io_running",
				Help:  "1 if the replica I/O thread is running.",
				Type:  gormmetrics.Gauge,
				Value: yesToFloat(raw[i]),
			})
		case "Replica_SQL_Running", "Slave_SQL_Running":
			out = append(out, gormmetrics.Sample{
				Name:  "mysql_replica_sql_running",
				Help:  "1 if the replica SQL thread is running.",
				Type:  gormmetrics.Gauge,
				Value: yesToFloat(raw[i]),
			})
		}
	}
	return out, nil
}

func yesToFloat(v interface{}) float64 {
	var s string
	switch x := v.(type) {
	case []byte:
		s = string(x)
	case string:
		s = x
	default:
		return 0
	}
	if strings.EqualFold(s, "Yes") {
		return 1
	}
	return 0
}
