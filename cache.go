package gormmetrics

import (
	"sync"
	"time"
)

// scrapeOutcome bundles the result of one scraper invocation along with
// timing data used to populate the meta-metrics family. It is the value
// type stored in scraperCache.
type scrapeOutcome struct {
	samples  []Sample
	err      error
	duration time.Duration
	at       time.Time
}

// scraperCache memoises one scraper's last outcome for at most ttl, and
// serializes concurrent in-flight scrapes for the same scraper via a
// single-flight style mutex so a /metrics-storm doesn't fan out into many
// duplicate DB queries.
//
// Each Scraper gets its own scraperCache (one per scraper, not one per
// Collector) so a slow scraper doesn't block fast ones.
//
// Two mutexes are used intentionally:
//   - mu protects the cached result (readers check cache, writer stores).
//   - flight serializes the actual scrape call (single-flight).
//
// This split allows concurrent cache-hit readers to proceed without
// blocking on each other — only the first cache-miss goroutine acquires
// flight and performs the DB query.
type scraperCache struct {
	ttl time.Duration

	mu      sync.Mutex
	last    scrapeOutcome
	hasLast bool

	// flight protects the actual scrape call: while one goroutine is
	// executing fetch, others block on this mutex and reuse the result.
	flight sync.Mutex
}

func newScraperCache(ttl time.Duration) *scraperCache {
	return &scraperCache{ttl: ttl}
}

// getOrFetch returns the cached outcome if it is fresh enough; otherwise it
// invokes fetch under the flight lock to ensure only one in-flight scrape
// per scraper per cache miss. A ttl of 0 disables caching entirely (useful
// for tests).
func (c *scraperCache) getOrFetch(now time.Time, fetch func() scrapeOutcome) scrapeOutcome {
	if c.ttl > 0 {
		c.mu.Lock()
		if c.hasLast && now.Sub(c.last.at) < c.ttl {
			out := c.last
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
	}

	c.flight.Lock()
	defer c.flight.Unlock()

	// Recheck after acquiring flight: another goroutine may have refreshed
	// the cache while we were waiting.
	if c.ttl > 0 {
		c.mu.Lock()
		if c.hasLast && now.Sub(c.last.at) < c.ttl {
			out := c.last
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
	}

	out := fetch()
	out.at = time.Now()

	c.mu.Lock()
	c.last = out
	c.hasLast = true
	c.mu.Unlock()

	return out
}
