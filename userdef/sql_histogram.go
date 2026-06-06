package userdef

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/phpgao/gormmetrics"
)

// SQLHistogram emits a Prometheus histogram derived from a pre-bucketed
// SQL query. The query must return rows of the shape:
//
//	(bucket_upper_bound float, cumulative_count uint64)
//
// ordered ascending by bucket_upper_bound. The +Inf bucket can either
// be supplied explicitly with a row whose bound is sentinel-large (e.g.
// 1e18) or omitted — SQLHistogram synthesises it from the maximum
// observed count.
//
// # CountQuery and SumQuery
//
// Both are optional but affect PromQL accuracy differently:
//
//   - CountQuery: if omitted, count is derived from the largest
//     cumulative bucket. This is usually correct.
//   - SumQuery: if omitted, sum is reported as 0. PromQL
//     histogram_quantile() still works for approximate quantiles, but
//     the sum of observations is wrong — burn-rate calculations and
//     average-latency queries will be incorrect. Supply SumQuery
//     whenever the underlying data source can provide it.
//
// Example: latency buckets from your application's own log table:
//
//	&userdef.SQLHistogram{
//	    Name:  "myapp_request_duration_seconds",
//	    Help:  "Application-recorded request latency.",
//	    BucketsQuery: `
//	        SELECT bucket_upper_sec, cum_count FROM request_buckets
//	        ORDER BY bucket_upper_sec`,
//	    CountQuery: "SELECT total_count FROM request_summary",
//	    SumQuery:   "SELECT total_seconds FROM request_summary",
//	}
type SQLHistogram struct {
	MetricName string
	Help       string

	BucketsQuery string
	CountQuery   string // optional
	SumQuery     string // optional

	Labels      map[string]string
	ScraperName string
}

func (h *SQLHistogram) Name() string {
	if h.ScraperName != "" {
		return h.ScraperName
	}
	return "userdef:histogram:" + h.MetricName
}

func (h *SQLHistogram) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	if h.MetricName == "" || h.BucketsQuery == "" {
		return nil, fmt.Errorf("SQLHistogram requires MetricName and BucketsQuery")
	}

	buckets := map[float64]uint64{}
	bounds := []float64{}

	rows, err := db.QueryContext(ctx, h.BucketsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bound float64
		var cum uint64
		if err := rows.Scan(&bound, &cum); err != nil {
			return nil, err
		}
		buckets[bound] = cum
		bounds = append(bounds, bound)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Float64s(bounds)

	var count uint64
	if h.CountQuery != "" {
		if err := db.QueryRowContext(ctx, h.CountQuery).Scan(&count); err != nil {
			return nil, fmt.Errorf("SQLHistogram count query: %w", err)
		}
	} else if len(bounds) > 0 {
		// Cumulative bucket counts are monotonic, so the largest is the
		// total observation count.
		count = buckets[bounds[len(bounds)-1]]
	}

	var sum float64
	if h.SumQuery != "" {
		if err := db.QueryRowContext(ctx, h.SumQuery).Scan(&sum); err != nil {
			return nil, fmt.Errorf("SQLHistogram sum query: %w", err)
		}
	}

	return []gormmetrics.Sample{{
		Name:             h.MetricName,
		Help:             h.Help,
		Type:             gormmetrics.Histogram,
		HistogramBuckets: buckets,
		HistogramCount:   count,
		HistogramSum:     sum,
		Labels:           copyLabels(h.Labels),
	}}, nil
}
