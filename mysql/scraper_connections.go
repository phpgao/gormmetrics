package mysql

import (
	"context"
	"database/sql"

	"github.com/phpgao/gormmetrics"
)

// ConnectionsScraper exposes the four connection-pool numbers a DBA
// typically watches first: established connections (Threads_connected),
// active threads (Threads_running), historical peak (Max_used_connections),
// and rejected attempts (Aborted_connects).
//
// All four come from SHOW GLOBAL STATUS — no special grants are required
// beyond the basic ability to issue SHOW STATUS.
type ConnectionsScraper struct{}

func (ConnectionsScraper) Name() string { return "mysql_connections" }

func (s ConnectionsScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	vals, err := fetchStatus(ctx, db, []string{
		"Threads_connected",
		"Threads_running",
		"Max_used_connections",
		"Aborted_connects",
		"Aborted_clients",
		"Connections",
	})
	if err != nil {
		return nil, err
	}

	out := make([]gormmetrics.Sample, 0, 6)
	if v, ok := vals["Threads_connected"]; ok {
		out = append(out, makeGauge("mysql_threads_connected",
			"Number of currently open connections.", v, nil))
	}
	if v, ok := vals["Threads_running"]; ok {
		out = append(out, makeGauge("mysql_threads_running",
			"Number of threads that are not sleeping.", v, nil))
	}
	if v, ok := vals["Max_used_connections"]; ok {
		out = append(out, makeGauge("mysql_max_used_connections",
			"Historical maximum number of connections that have been in use simultaneously.", v, nil))
	}
	if v, ok := vals["Aborted_connects"]; ok {
		out = append(out, makeCounter("mysql_aborted_connects_total",
			"Number of failed attempts to connect to the MySQL server.", v, nil))
	}
	if v, ok := vals["Aborted_clients"]; ok {
		out = append(out, makeCounter("mysql_aborted_clients_total",
			"Number of connections aborted because the client died without closing the connection properly.", v, nil))
	}
	if v, ok := vals["Connections"]; ok {
		out = append(out, makeCounter("mysql_connections_total",
			"Total number of connection attempts (successful or not).", v, nil))
	}
	return out, nil
}
