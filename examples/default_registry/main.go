// Example: register the Collector into prometheus.DefaultRegisterer so
// it shows up under the standard promhttp.Handler() endpoint alongside
// any other library that uses the default registry (e.g. promauto,
// client_golang's process/Go collectors, third-party libs).
//
// This is the simplest way to "wire it up like everything else does"
// without managing your own *prometheus.Registry. The trade-off: the
// default registry is process-global state, so two libraries trying to
// register the same metric name will panic at registration time.
//
//	go run ./examples/default_registry
//	curl http://localhost:8080/metrics
package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/go-sql-driver/mysql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
)

// appRequests is a typical application-level counter registered via
// promauto, which uses prometheus.DefaultRegisterer under the hood.
// Showing it here proves that gormmetrics on the default registry
// coexists cleanly with code that already uses promauto idioms.
var appRequests = promauto.NewCounter(prometheus.CounterOpts{
	Name: "myapp_requests_total",
	Help: "Total business requests handled.",
})

func main() {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		log.Fatal("set MYSQL_DSN")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	defer db.Close()

	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Register the Collector into the package-global default registry.
	// MustRegister panics on duplicate registration — desirable here
	// because installing twice is a bug, not a recoverable condition.
	prometheus.MustRegister(c)

	// Pretend the application processed a request — the counter shows
	// up at /metrics alongside the gormmetrics_* series.
	appRequests.Inc()

	// promhttp.Handler() serves prometheus.DefaultGatherer, which is
	// the read side of prometheus.DefaultRegisterer. Both the
	// Collector's metrics and any promauto-registered metric appear
	// at the same endpoint.
	http.Handle("/metrics", promhttp.Handler())
	log.Println("serving /metrics on :8080 (default registry)")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
