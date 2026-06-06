package userdef

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/phpgao/gormmetrics"
)

// SQLGauge exposes a single scalar from a SQL query as a Gauge. The query
// must return exactly one column of one row that can be cast to float64.
//
//	&userdef.SQLGauge{
//	    Name:  "orders_pending",
//	    Help:  "Number of orders awaiting fulfilment.",
//	    Query: "SELECT COUNT(*) FROM orders WHERE status='pending'",
//	}
//
// Use Args for parameterised queries; the slice is forwarded to db.QueryRow.
// Labels are merged into the emitted sample (and combined with the
// Collector's const labels by the framework).
type SQLGauge struct {
	MetricName string // exported as the Prometheus metric name
	Help       string
	Query      string
	Args       []any
	Labels     map[string]string

	// ScraperName overrides the default Scraper.Name() (which is
	// "userdef:gauge:" + MetricName). Set this if you need a stable
	// meta-metrics label across renames of MetricName.
	ScraperName string
}

// Name returns the scraper identifier used as a label on meta-metrics.
// We namespace user-defined scrapers under "userdef:" to make it obvious
// in dashboards which scrapers are framework-provided vs ad-hoc.
func (s *SQLGauge) Name() string {
	if s.ScraperName != "" {
		return s.ScraperName
	}
	return "userdef:gauge:" + s.MetricName
}

func (s *SQLGauge) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	if s.MetricName == "" || s.Query == "" {
		return nil, fmt.Errorf("SQLGauge requires MetricName and Query")
	}
	var vi interface{}
	if err := db.QueryRowContext(ctx, s.Query, s.Args...).Scan(&vi); err != nil {
		return nil, err
	}
	v, ok := ToFloat(vi)
	if !ok {
		return nil, fmt.Errorf("SQLGauge query %q: value %v cannot be parsed as float64", s.Query, vi)
	}
	return []gormmetrics.Sample{{
		Name:   s.MetricName,
		Help:   s.Help,
		Type:   gormmetrics.Gauge,
		Value:  v,
		Labels: copyLabels(s.Labels),
	}}, nil
}

// SQLCounter is identical to SQLGauge except the emitted metric is a
// Counter. Use this when the underlying value is monotonically
// non-decreasing (e.g. SELECT seq FROM mytable). Prometheus's rate()
// handles counter resets, so a process restart that resets your seq is
// not a correctness problem — but a value that genuinely decreases
// produces meaningless rate() output, so make sure the source is truly
// cumulative.
type SQLCounter struct {
	MetricName  string
	Help        string
	Query       string
	Args        []any
	Labels      map[string]string
	ScraperName string
}

func (s *SQLCounter) Name() string {
	if s.ScraperName != "" {
		return s.ScraperName
	}
	return "userdef:counter:" + s.MetricName
}

func (s *SQLCounter) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	if s.MetricName == "" || s.Query == "" {
		return nil, fmt.Errorf("SQLCounter requires MetricName and Query")
	}
	var vi interface{}
	if err := db.QueryRowContext(ctx, s.Query, s.Args...).Scan(&vi); err != nil {
		return nil, err
	}
	v, ok := ToFloat(vi)
	if !ok {
		return nil, fmt.Errorf("SQLCounter query %q: value %v cannot be parsed as float64", s.Query, vi)
	}
	return []gormmetrics.Sample{{
		Name:   s.MetricName,
		Help:   s.Help,
		Type:   gormmetrics.Counter,
		Value:  v,
		Labels: copyLabels(s.Labels),
	}}, nil
}

// copyLabels defensively copies the user-supplied label map. Without it,
// later mutations on the caller's map would leak into already-emitted
// samples — bad because samples are sometimes retained in a cache.
func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
