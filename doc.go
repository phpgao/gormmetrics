// Package gormmetrics is a lazy-scrape Prometheus exporter for SQL databases,
// designed to be embedded inside Go applications rather than deployed as a
// separate sidecar process.
//
// # Design goals
//
//   - Lazy: metrics are scraped on demand when Prometheus pulls /metrics,
//     not in a background goroutine. A short TTL cache absorbs repeated
//     scrapes from the same Prometheus instance.
//
//   - Stateless: Counters are exposed as their absolute, server-reported
//     value. Prometheus handles deltas and counter resets — this library
//     never needs to remember a previous reading.
//
//   - Composable: scrapers are small, single-responsibility units. Each
//     database's preset is just a slice of scrapers; advanced users pick
//     individual scrapers à la carte.
//
//   - GORM-friendly but not GORM-bound: the core API takes a *sql.DB so
//     it can wrap any database/sql driver. For GORM integration, see the
//     separate gormplugin repository at github.com/phpgao/gormplugin.
//
// # Quick start
//
//	c, _ := gormmetrics.New(
//	    gormmetrics.WithDB(sqlDB),
//	    gormmetrics.WithScrapers(mysql.StandardPack()...),
//	    gormmetrics.WithLabels(map[string]string{"cluster": "prod-eu-1"}),
//	)
//	http.Handle("/metrics", c.Handler())
//
// For extended usage including custom business metrics and GORM integration,
// see the separate gormplugin repository: https://github.com/phpgao/gormplugin
package gormmetrics
