package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/phpgao/gormmetrics"
)

// fetchStatus runs SHOW GLOBAL STATUS filtered to the requested variable
// names and returns a name->float64 map. Variables present in the result
// but unparseable as float64 are silently dropped (MySQL exposes some
// string-typed status values that aren't meaningful as metrics).
func fetchStatus(ctx context.Context, db *sql.DB, names []string) (map[string]float64, error) {
	if len(names) == 0 {
		return map[string]float64{}, nil
	}
	// Build dynamic placeholder list — IN(?) isn't natively expanded by
	// database/sql.
	placeholders := "?" + strings.Repeat(",?", len(names)-1)
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	q := "SHOW GLOBAL STATUS WHERE Variable_name IN (" + placeholders + ")"

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fetchStatus: %w", err)
	}
	defer rows.Close()

	out := make(map[string]float64, len(names))
	for rows.Next() {
		var name, val string
		if err = rows.Scan(&name, &val); err != nil {
			return nil, fmt.Errorf("fetchStatus scan: %w", err)
		}
		f, perr := strconv.ParseFloat(val, 64)
		if perr != nil {
			continue
		}
		out[name] = f
	}
	return out, rows.Err()
}

// mustFloat parses a SHOW STATUS value string. Returns (value, true) on
// success, or (-1, false) when the string cannot be parsed as a number.
// The caller filters non-numeric status values anyway, so -1 is safe as
// a sentinel for parse failures.
func mustFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1, false
	}
	return f, true
}

// makeGauge / makeCounter are tiny helpers to keep scraper code dense.
func makeGauge(name, help string, val float64, labels map[string]string) gormmetrics.Sample {
	return gormmetrics.Sample{Name: name, Help: help, Type: gormmetrics.Gauge, Value: val, Labels: labels}
}

func makeCounter(name, help string, val float64, labels map[string]string) gormmetrics.Sample {
	return gormmetrics.Sample{Name: name, Help: help, Type: gormmetrics.Counter, Value: val, Labels: labels}
}
