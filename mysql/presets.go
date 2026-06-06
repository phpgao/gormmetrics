package mysql

import "github.com/phpgao/gormmetrics"

// Presets bundle scrapers into three opinionated tiers. They are functions
// (not vars) so users can mutate the returned slice without affecting
// future calls — important because gormmetrics.WithScrapers retains the
// pointers.
//
//	Tier         | Scrapers                                | Permissions
//	-------------+-----------------------------------------+----------------------
//	MinimalPack  | Connections                             | none (basic SHOW STATUS)
//	StandardPack | + Traffic + InnoDB                      | PROCESS recommended
//	FullPack     | + Replication + QueryLatency            | PROCESS + REPLICATION CLIENT
//	                                                       + perf_schema access
//
// Anything requiring extra privileges is gated by a Probe; failed probes
// surface in gormmetrics_scraper_disabled rather than aborting the
// Collector. So in practice the recommendation is: pass StandardPack(),
// let probes auto-disable what your account can't see, and grant
// privileges later to expose more metrics without changing application
// code.

// MinimalPack returns the smallest useful set: connection-pool health.
// All scrapers here run on any MySQL account with default permissions.
// Suitable for shared MySQL servers where you can't get PROCESS.
func MinimalPack() []gormmetrics.Scraper {
	return []gormmetrics.Scraper{
		ConnectionsScraper{},
	}
}

// StandardPack is the default recommendation: connection-pool health,
// traffic shape, InnoDB internals. About 30–50 series on a typical server.
// Each scraper that needs PROCESS is probed once and silently disabled if
// the privilege is missing — the rest still produce data.
func StandardPack() []gormmetrics.Scraper {
	return []gormmetrics.Scraper{
		ConnectionsScraper{},
		TrafficScraper{},
		InnoDBScraper{},
	}
}

// FullPack adds replication state and statement-latency histograms. Use
// only when the metrics user has been granted REPLICATION CLIENT and
// performance_schema read; otherwise both probes will disable themselves
// you'll just get StandardPack worth of data with extra disabled-meta
// noise.
func FullPack() []gormmetrics.Scraper {
	return append(StandardPack(),
		&ReplicationScraper{},
		QueryLatencyScraper{},
	)
}
