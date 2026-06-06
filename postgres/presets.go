package postgres

import "github.com/phpgao/gormmetrics"

// Presets mirror the mysql sub-package's three-tier model.
//
//	Tier         | Scrapers                                      | Grants
//	-------------+-----------------------------------------------+--------------
//	MinimalPack  | Activity + Size                               | none
//	StandardPack | + Database + Locks                            | none (PG defaults)
//	FullPack     | + Replication + TableStat                     | pg_monitor / pg_read_all_stats
//
// As with MySQL, scrapers requiring extra grants implement ProbingScraper
// and silently disable themselves rather than spamming errors.

// MinimalPack: backend state + per-DB size. Useful as a connection-pool
// health overview that works against essentially any PG account.
func MinimalPack() []gormmetrics.Scraper {
	return []gormmetrics.Scraper{
		ActivityScraper{},
		SizeScraper{},
	}
}

// StandardPack: adds pg_stat_database (transactions, cache, deadlocks)
// and lock counts. Still no special grants required — these views are
// public by default.
func StandardPack() []gormmetrics.Scraper {
	return []gormmetrics.Scraper{
		ActivityScraper{},
		SizeScraper{},
		DatabaseScraper{},
		LocksScraper{},
	}
}

// FullPack: includes replication monitoring and per-table activity.
// Both can fan out cardinality significantly — replication scales with
// connected replicas, TableStat with table count. Both are probed and
// auto-disable on permission failure.
func FullPack() []gormmetrics.Scraper {
	return append(StandardPack(),
		ReplicationScraper{},
		TableStatScraper{},
	)
}
