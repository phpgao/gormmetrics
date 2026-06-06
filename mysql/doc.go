// Package mysql provides ready-made gormmetrics Scrapers for MySQL and
// MariaDB. Scrapers are intentionally small and composable: each one runs
// a single query and emits a small, well-defined family of metrics.
//
// Use a preset (MinimalPack, StandardPack, FullPack) for typical setups,
// or hand-pick individual scrapers when you need precise control over
// what hits Prometheus.
//
//	c, _ := gormmetrics.New(
//	    gormmetrics.WithDB(sqlDB),
//	    gormmetrics.WithScrapers(mysql.StandardPack()...),
//	)
//
// All metrics are exposed with their server-reported absolute values
// (Counters are CounterValue, not Gauges). Use rate() / increase() in
// PromQL to derive throughput — this means no state is kept inside the
// process, so multi-pod deployments don't suffer counter-reset confusion.
package mysql
