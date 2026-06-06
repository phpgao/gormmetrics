// Example: register the gormmetrics Collector into a Registry of your
// own choosing instead of the convenience c.Handler() (which builds a
// private registry under the hood).
//
// Two common cases shown side-by-side:
//
//  1. Coexist with application metrics on a shared registry.
//     The application already maintains a *prometheus.Registry where
//     its own counters/gauges live; gormmetrics just adds itself to it
//     so /metrics serves everything together.
//
//  2. Push to Pushgateway instead of pull.
//     For ephemeral processes (cron jobs, batch tasks) that exit
//     before Prometheus can scrape them, send metrics to a Pushgateway.
//     The Collector is still a normal prometheus.Collector — gather
//     it into a Registry, then point a push.Pusher at the Registry.
//
//     go run ./examples/custom_registry
//     curl http://localhost:8080/metrics   # case 1 endpoint
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/push"

	"github.com/phpgao/gormmetrics"
	"github.com/phpgao/gormmetrics/mysql"
)

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

	// Build the Collector once. It implements prometheus.Collector so
	// any *prometheus.Registry (or anything implementing Registerer)
	// can host it.
	c, err := gormmetrics.New(
		gormmetrics.WithDB(db),
		gormmetrics.WithScrapers(mysql.StandardPack()...),
	)
	if err != nil {
		log.Fatal(err)
	}

	// ---- Case 1: shared application registry ---------------------------
	//
	// Build a fresh registry so test runs don't pollute the global
	// default. Register the Go runtime / process collectors that
	// promhttp.Handler() normally bundles for you, plus any
	// application-defined metrics, alongside the gormmetrics Collector.
	appReg := prometheus.NewRegistry()
	appReg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		c, // <-- gormmetrics joins the same registry
	)

	// Hypothetical application metric registered into the same registry —
	// shows that gormmetrics doesn't get in the way of business gauges.
	appReg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
		Name: "myapp_requests_total",
		Help: "Total business requests handled.",
	}))

	http.Handle("/metrics", promhttp.HandlerFor(appReg, promhttp.HandlerOpts{}))
	go func() {
		log.Println("case 1: serving combined registry on :8080/metrics")
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()

	// ---- Case 2: push to a Pushgateway --------------------------------
	//
	// Use a dedicated registry for the push (separating push and pull
	// avoids accidentally pushing the entire shared registry every
	// cycle). Same Collector, different Registry.
	pushReg := prometheus.NewRegistry()
	pushReg.MustRegister(c)

	pusher := push.New("http://pushgateway.local:9091", "orders-batch").
		Gatherer(pushReg).
		Grouping("instance", "orders-1")

	// Push once on startup to demonstrate the shape; production code
	// would push on a schedule, or once at end-of-job.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pusher.PushContext(ctx); err != nil {
		log.Printf("case 2: push failed (expected if no pushgateway): %v", err)
	} else {
		log.Println("case 2: pushed to pushgateway")
	}

	select {} // keep the HTTP server alive
}
