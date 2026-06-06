package userdef

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/phpgao/gormmetrics"
)

// FuncScraper is the escape hatch for everything that doesn't fit a SQL
// scraper: filesystem probes, in-memory cache stats, externally-fetched
// metrics, computed values, etc.
//
//	&userdef.FuncScraper{
//	    Name: "sqlite_db_file_size_bytes",
//	    Help: "Size of the SQLite DB file on disk.",
//	    Collect: func(ctx context.Context, _ *sql.DB) ([]gormmetrics.Sample, error) {
//	        fi, err := os.Stat("/var/lib/myapp/foo.db")
//	        if err != nil { return nil, err }
//	        return []gormmetrics.Sample{{
//	            Name:  "sqlite_db_file_size_bytes",
//	            Type:  gormmetrics.Gauge,
//	            Value: float64(fi.Size()),
//	        }}, nil
//	    },
//	}
//
// The *sql.DB argument is the Collector's DB handle, passed through for
// convenience; FuncScraper.Collect is free to ignore it.
type FuncScraper struct {
	// ID is used as the meta-metric label and as part of the default
	// Scraper.Name(). It is not used as a Prometheus metric name —
	// FuncScraper's emitted Samples carry their own Name field.
	ID   string
	Help string

	// Collect returns the samples for this scrape. It is invoked at
	// most once per cache window (see WithCacheTTL) regardless of
	// /metrics request rate.
	Collect func(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error)

	ScraperName string
}

func (f *FuncScraper) Name() string {
	if f.ScraperName != "" {
		return f.ScraperName
	}
	return "userdef:func:" + f.ID
}

func (f *FuncScraper) Scrape(ctx context.Context, db *sql.DB) ([]gormmetrics.Sample, error) {
	if f.ID == "" || f.Collect == nil {
		return nil, fmt.Errorf("FuncScraper requires ID and Collect")
	}
	return f.Collect(ctx, db)
}
