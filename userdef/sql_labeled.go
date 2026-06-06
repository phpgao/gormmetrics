package userdef

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/phpgao/gormmetrics"
)

// SQLLabeled emits one sample per row of a multi-row query, mapping
// configurable columns to Prometheus labels and one column to the
// numeric value.
//
//	&userdef.SQLLabeled{
//	    Name:         "orders_by_status",
//	    Query:        "SELECT status, COUNT(*) FROM orders GROUP BY status",
//	    LabelColumns: []string{"status"},
//	    ValueColumn:  "count", // or omit and rely on LastColumnIsValue
//	}
//
// Use it for any "group-by" style metric where the label set is small
// and bounded. Beware: cardinality scales with rowcount, so don't point
// it at SELECT order_id, ...
type SQLLabeled struct {
	MetricName string
	Help       string
	Query      string
	Args       []any

	// Type is Gauge by default. Switch to Counter when the value column
	// is monotonic across scrapes (e.g. lifetime sum).
	Type gormmetrics.MetricType

	// LabelColumns names the columns to emit as labels (case-sensitive).
	// Defaults: all columns except the value column.
	LabelColumns []string

	// ValueColumn names the numeric column. If empty, the LAST column
	// in the result set is taken as the value — this matches the
	// shape SELECT label1, label2, ..., COUNT(*) FROM ... GROUP BY ...
	// which is overwhelmingly the common case.
	ValueColumn string

	ConstLabels map[string]string
	ScraperName string
}

func (s *SQLLabeled) Name() string {
	if s.ScraperName != "" {
		return s.ScraperName
	}
	return "userdef:labeled:" + s.MetricName
}

func (s *SQLLabeled) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	if s.MetricName == "" || s.Query == "" {
		return nil, fmt.Errorf("SQLLabeled requires MetricName and Query")
	}
	rows, err := db.QueryContext(ctx, s.Query, s.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(cols) < 2 {
		return nil, fmt.Errorf("SQLLabeled needs at least 2 columns (one label, one value); got %d", len(cols))
	}

	// Resolve which column is the value and which are labels.
	valueIdx := len(cols) - 1
	if s.ValueColumn != "" {
		valueIdx = -1
		for i, c := range cols {
			if c == s.ValueColumn {
				valueIdx = i
				break
			}
		}
		if valueIdx == -1 {
			return nil, fmt.Errorf("SQLLabeled ValueColumn %q not found in result columns %v", s.ValueColumn, cols)
		}
	}

	labelIdxs := make([]int, 0, len(cols)-1)
	labelNames := make([]string, 0, len(cols)-1)
	if len(s.LabelColumns) == 0 {
		// Default: all non-value columns are labels.
		for i, c := range cols {
			if i == valueIdx {
				continue
			}
			labelIdxs = append(labelIdxs, i)
			labelNames = append(labelNames, c)
		}
	} else {
		for _, want := range s.LabelColumns {
			idx := -1
			for i, c := range cols {
				if c == want {
					idx = i
					break
				}
			}
			if idx == -1 {
				return nil, fmt.Errorf("SQLLabeled LabelColumn %q not found in result columns %v", want, cols)
			}
			labelIdxs = append(labelIdxs, idx)
			labelNames = append(labelNames, want)
		}
	}

	out := make([]gormmetrics.Sample, 0, 16)
	rawValues := make([]interface{}, len(cols))
	scanDest := make([]interface{}, len(cols))
	for i := range rawValues {
		scanDest[i] = &rawValues[i]
	}
	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return out, err
		}
		v, ok := ToFloat(rawValues[valueIdx])
		if !ok {
			continue
		}
		labels := make(map[string]string, len(labelIdxs)+len(s.ConstLabels))
		for k, v := range s.ConstLabels {
			labels[k] = v
		}
		for i, idx := range labelIdxs {
			labels[labelNames[i]] = toString(rawValues[idx])
		}
		out = append(out, gormmetrics.Sample{
			Name: s.MetricName, Help: s.Help, Type: s.Type, Value: v, Labels: labels,
		})
	}
	return out, rows.Err()
}
