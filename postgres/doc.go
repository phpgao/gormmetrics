// Package postgres provides gormmetrics Scrapers for PostgreSQL.
//
// Like the mysql sub-package, scrapers here are small and composable.
// Use MinimalPack / StandardPack / FullPack for typical configurations,
// or hand-pick individual scrapers when curating a specific dashboard.
//
//	c, _ := gormmetrics.New(
//	    gormmetrics.WithDB(sqlDB),
//	    gormmetrics.WithScrapers(postgres.StandardPack()...),
//	)
//
// Scrapers that depend on extended grants (pg_stat_replication,
// per-table statistics on schemas the user can't read) implement
// ProbingScraper so the Collector quietly disables them when grants
// are missing.
package postgres
