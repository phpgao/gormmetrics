// Example: mix built-in scrapers with custom business metrics. Shows
// SQLGauge for a simple count, SQLLabeled for a GROUP BY, and
// FuncScraper for non-SQL data (here: a disk-size probe).
//
//	go run ./examples/custom_metrics
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"syscall"

	_ "github.com/go-sql-driver/mysql"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
	"github.com/phpgao/gormmetrics/userdef"
)

func main() {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		log.Fatal("set MYSQL_DSN")
	}
	db, _ := sql.Open("mysql", dsn)
	db.SetMaxOpenConns(2)
	defer db.Close()

	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),

		// Business KPI #1: a simple scalar count.
		gormmetrics.WithScrapers(&userdef.SQLGauge{
			MetricName: "orders_pending_count",
			Help:       "Orders awaiting fulfilment.",
			Query:      "SELECT COUNT(*) FROM orders WHERE status='pending'",
		}),

		// Business KPI #2: GROUP BY → one Sample per row, labelled.
		gormmetrics.WithScrapers(&userdef.SQLLabeled{
			MetricName:   "orders_by_status",
			Help:         "Orders count by status.",
			Query:        "SELECT status, COUNT(*) FROM orders GROUP BY status",
			Type:         gormmetrics.Gauge,
			LabelColumns: []string{"status"},
		}),

		// Filesystem probe — anything that needs Go code rather than SQL.
		gormmetrics.WithScrapers(&userdef.FuncScraper{
			ID:   "data_volume_free_bytes",
			Help: "Free bytes on the data volume.",
			Collect: func(_ context.Context, _ *sql.DB) ([]gormmetrics.Sample, error) {
				var stat syscall.Statfs_t
				if err := syscall.Statfs("/", &stat); err != nil {
					return nil, err
				}
				return []gormmetrics.Sample{{
					Name:  "data_volume_free_bytes",
					Type:  gormmetrics.Gauge,
					Value: float64(stat.Bavail) * float64(stat.Bsize),
				}}, nil
			},
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/metrics", c.Handler())
	log.Fatal(http.ListenAndServe(":8080", nil))
}
