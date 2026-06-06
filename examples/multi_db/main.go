// Example: monitor multiple databases from the same process. Demonstrates
// using one Prometheus registry to host two Collectors, distinguishing
// them by const labels rather than separate /metrics endpoints.
//
// Why one endpoint + labels (rather than two endpoints)? Because that's
// how Prometheus expects multi-instance services to behave — the scrape
// job in prometheus.yml targets ONE endpoint and your dashboards filter
// by the `role` label.
//
//	MYSQL_DSN='primary' MYSQL_REPLICA_DSN='replica' go run ./examples/multi_db
package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
)

func mustCollector(dsn string, labels map[string]string) *gormmetrics.Collector {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),
		gormmetrics.WithLabels(labels),
	)
	if err != nil {
		log.Fatal(err)
	}
	return c
}

func main() {
	primary := mustCollector(os.Getenv("MYSQL_DSN"),
		map[string]string{"role": "primary", "instance": "orders-1"})
	replica := mustCollector(os.Getenv("MYSQL_REPLICA_DSN"),
		map[string]string{"role": "replica", "instance": "orders-1"})

	// Share one Prometheus registry. Same metric NAMES collide if you
	// register both Collectors directly into a default registry without
	// distinguishing labels — but our const labels make the series
	// unique per role, so the registry accepts both.
	reg := prometheus.NewRegistry()
	reg.MustRegister(primary, replica)

	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	log.Fatal(http.ListenAndServe(":8080", nil))
}
