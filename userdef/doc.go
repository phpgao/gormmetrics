// Package userdef provides ergonomic Scraper implementations for ad-hoc
// metrics — business KPIs, application-specific health probes, anything
// that doesn't fit a built-in collector.
//
// The four building blocks cover essentially all real-world needs:
//
//   - SQLGauge / SQLCounter: single scalar from a single SQL query.
//   - SQLLabeled: multi-row query → one sample per row, with label columns.
//   - SQLHistogram: bucket counts from a query that already aggregates them.
//   - FuncScraper: arbitrary Go code, for non-SQL data sources (filesystem,
//     external HTTP probe, in-memory cache stats, ...).
//
// Every type honors the framework contracts: it's safe across cache
// windows, surfaces errors via meta-metrics, and inherits the
// Collector's const labels. Users write 5–10 lines per metric instead of
// the 100+ that a from-scratch Scraper requires.
package userdef
