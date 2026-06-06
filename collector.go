package gormmetrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ErrNoDB is returned by New when no *sql.DB has been provided via WithDB.
// We refuse to construct a Collector without one to surface configuration
// mistakes at startup rather than at first /metrics request.
var ErrNoDB = errors.New("gormmetrics: WithDB is required")

// Option configures a Collector. Use the With* helpers — the unexported
// applyTo method keeps the option type sealed so we can extend the
// internal config struct without breaking callers.
type Option interface {
	applyTo(*collectorConfig)
}

type optionFunc func(*collectorConfig)

func (f optionFunc) applyTo(c *collectorConfig) { f(c) }

// collectorConfig is the resolved configuration after applying all Options.
type collectorConfig struct {
	db              *sql.DB
	scrapers        []Scraper
	cacheTTL        time.Duration
	labels          map[string]string
	logger          Logger
	namespace       string // optional metric name prefix
	probeTimeout    time.Duration
	scrapeTimeout   time.Duration
	skipProbeOnNew  bool
	errorClassifier ErrorClassifier
}

// WithDB binds the Collector to a database/sql connection pool. This is
// the only required option.
//
// The pool should ideally be dedicated to metric collection (e.g. SetMaxOpenConns(2))
// so that monitoring traffic doesn't compete with business queries.
func WithDB(db *sql.DB) Option {
	return optionFunc(func(c *collectorConfig) { c.db = db })
}

// WithScrapers registers one or more scrapers. Can be called multiple
// times to combine presets with custom scrapers, e.g.:
//
//	WithScrapers(mysql.StandardPack()...),
//	WithScrapers(&userdef.SQLGauge{...}),
func WithScrapers(s ...Scraper) Option {
	return optionFunc(func(c *collectorConfig) { c.scrapers = append(c.scrapers, s...) })
}

// WithCacheTTL sets the per-scraper result cache window. Default 10s.
// Set to 0 to disable caching (every /metrics request triggers a fresh
// scrape — useful for tests, dangerous in production).
func WithCacheTTL(d time.Duration) Option {
	return optionFunc(func(c *collectorConfig) { c.cacheTTL = d })
}

// WithLabels supplies const labels merged into every emitted sample. Use
// this for instance-wide dimensions like cluster, shard, environment.
// Per-sample labels (from Scraper outputs) take precedence on conflict.
func WithLabels(labels map[string]string) Option {
	return optionFunc(func(c *collectorConfig) {
		if c.labels == nil {
			c.labels = make(map[string]string, len(labels))
		}
		for k, v := range labels {
			c.labels[k] = v
		}
	})
}

// WithLogger sets the structured logger used for probe outcomes and
// non-metric errors. Defaults to a stdlib log.Logger that writes to
// stderr with a "gormmetrics" prefix.
func WithLogger(l Logger) Option {
	return optionFunc(func(c *collectorConfig) { c.logger = l })
}

// WithNamespace prefixes every emitted metric name with the given namespace
// plus an underscore. Useful when a single Prometheus instance scrapes
// many gormmetrics-using applications.
func WithNamespace(ns string) Option {
	return optionFunc(func(c *collectorConfig) { c.namespace = ns })
}

// WithProbeTimeout bounds the duration of each Scraper.Probe call. Default
// 5 seconds. Probes are best-effort: a timeout disables the scraper but
// does not fail New.
func WithProbeTimeout(d time.Duration) Option {
	return optionFunc(func(c *collectorConfig) { c.probeTimeout = d })
}

// WithScrapeTimeout bounds the duration of each Scraper.Scrape call.
// Default 10 seconds. Exceeding the timeout returns a context error
// surfaced via the meta-metrics.
func WithScrapeTimeout(d time.Duration) Option {
	return optionFunc(func(c *collectorConfig) { c.scrapeTimeout = d })
}

// WithErrorClassifier replaces the default string-matching error classifier
// with a custom function. Use this to pass driver-specific error types
// (e.g. *mysql.MySQLError, *pgconn.PgError) for precise error categorization
// rather than relying on string matching.
//
// Example:
//
//	WithErrorClassifier(func(err error) string {
//	    if mysqlErr, ok := err.(*mysql.MySQLError); ok {
//	        switch mysqlErr.Number {
//	        case 1045, 1142: return "permission_denied"
//	        case 2003, 2006: return "connectivity"
//	        }
//	    }
//	    return "other"
//	})
func WithErrorClassifier(fn ErrorClassifier) Option {
	return optionFunc(func(c *collectorConfig) { c.errorClassifier = fn })
}

// WithoutProbe disables the one-time Probe pass during New. Useful for
// tests that don't want to mock probe queries, or when you know in
// advance that all scrapers are usable.
func WithoutProbe() Option {
	return optionFunc(func(c *collectorConfig) { c.skipProbeOnNew = true })
}

// Collector is the user-facing handle. It implements prometheus.Collector
// so it can be registered into any prometheus.Registry, and offers a
// Handler() convenience for the common "just give me a /metrics endpoint"
// case.
type Collector struct {
	cfg collectorConfig

	// scrapers and their per-scraper caches, in registration order.
	entries []*scrapeEntry

	meta *metaMetrics
}

// scrapeEntry pairs a Scraper with its cache and probe status.
type scrapeEntry struct {
	scraper  Scraper
	cache    *scraperCache
	disabled bool  // set by failed Probe; never re-enabled within process lifetime
	probeErr error // for telemetry
}

// New constructs a Collector. It runs all ProbingScraper probes once
// (subject to WithProbeTimeout) unless WithoutProbe is supplied.
//
// Returns ErrNoDB if no *sql.DB is supplied.
func New(opts ...Option) (*Collector, error) {
	cfg := collectorConfig{
		cacheTTL:      10 * time.Second,
		probeTimeout:  5 * time.Second,
		scrapeTimeout: 10 * time.Second,
		logger:        defaultLogger(),
	}
	for _, o := range opts {
		o.applyTo(&cfg)
	}
	if cfg.db == nil {
		return nil, ErrNoDB
	}

	c := &Collector{
		cfg:     cfg,
		meta:    newMetaMetrics(cfg.namespace, cfg.labels, cfg.errorClassifier),
		entries: make([]*scrapeEntry, 0, len(cfg.scrapers)),
	}

	for _, s := range cfg.scrapers {
		entry := &scrapeEntry{
			scraper: s,
			cache:   newScraperCache(cfg.cacheTTL),
		}
		c.entries = append(c.entries, entry)
	}

	if !cfg.skipProbeOnNew {
		c.runProbes()
	}

	return c, nil
}

// runProbes invokes Probe on every ProbingScraper. Failures mark the entry
// as disabled and emit a one-time INFO log; the Collector itself never
// errors out of New due to a probe failure.
func (c *Collector) runProbes() {
	for _, e := range c.entries {
		probe, ok := e.scraper.(ProbingScraper)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.cfg.probeTimeout)
		err := probe.Probe(ctx, c.cfg.db)
		cancel()
		if err != nil {
			e.disabled = true
			e.probeErr = err
			c.cfg.logger.Infof("scraper %q disabled by probe: %v", e.scraper.Name(), err)
			c.meta.observeDisabled(e.scraper.Name(), err)
		}
	}
}

// Describe implements prometheus.Collector. We use the "unchecked" pattern:
// describing every possible descriptor up front is impractical because each
// Sample can carry arbitrary labels. Emitting nothing here tells Prometheus
// to skip the consistency check, which is the same approach the official
// node_exporter and mysqld_exporter take.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.meta.describe(ch)
}

// Collect implements prometheus.Collector. It scrapes every enabled
// scraper (subject to per-scraper cache) and emits the resulting samples
// along with the meta-metrics.
//
// Defensive ordering:
//  1. PingContext first. If the DB is unreachable, every Scraper would
//     also time out — that's N×scrapeTimeout of wasted blocking on a
//     dead DB. We short-circuit: emit up=0, mark each scrape as failed
//     in meta-metrics with class "connectivity", skip Scrape calls.
//  2. Real scrapes only when up==true. Each still has its own
//     per-scraper timeout as a backstop against one slow query.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	// Fixed timeout context for all DB operations within this Collect cycle.
	// Does not propagate HTTP client disconnect — use scrapeTimeout as the
	// only timeout bound. HTTP request context propagation was removed to
	// eliminate concurrency hazards when the Collector is shared across
	// multiple Registries or goroutines.
	parent, cancel := context.WithTimeout(context.Background(), c.cfg.scrapeTimeout)
	defer cancel()

	pingErr := c.pingUp(parent)
	c.meta.recordUp(pingErr == nil)

	if pingErr != nil {
		// Synthesise a failed scrape outcome for each enabled scraper
		// so operators see exactly which scrapers were skipped — not
		// just "the whole Collector went dark". Disabled scrapers
		// still get their disabled gauge.
		// Use the actual ping error so classifyErr() correctly
		// distinguishes "timeout" from "connectivity".
		skipErr := pingErr
		for _, e := range c.entries {
			if e.disabled {
				c.meta.recordDisabled(e.scraper.Name())
				continue
			}
			c.meta.recordScrape(e.scraper.Name(), scrapeOutcome{
				err:      skipErr,
				duration: 0,
			})
		}
		c.meta.collect(ch)
		return
	}

	for _, e := range c.entries {
		if e.disabled {
			c.meta.recordDisabled(e.scraper.Name())
			continue
		}
		c.scrapeOne(parent, ch, e)
	}

	c.meta.collect(ch)
}

// pingUp issues a quick SELECT 1 (database-agnostic) to surface basic
// connectivity in the gormmetrics_up gauge. We bypass the cache for this
// because it's the canonical "is the DB reachable right now?" signal.
//
// Returns nil on success, or the underlying driver error on failure.
// The error is propagated as-is into scrape_errors so classifyErr()
// can distinguish "timeout" from "connectivity".
func (c *Collector) pingUp(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, c.cfg.scrapeTimeout)
	defer cancel()
	return c.cfg.db.PingContext(ctx)
}

func (c *Collector) scrapeOne(parent context.Context, ch chan<- prometheus.Metric, e *scrapeEntry) {
	out := e.cache.getOrFetch(time.Now(), func() scrapeOutcome {
		ctx, cancel := context.WithTimeout(parent, c.cfg.scrapeTimeout)
		defer cancel()
		start := time.Now()
		samples, err := e.scraper.Scrape(ctx, c.cfg.db)
		return scrapeOutcome{
			samples:  samples,
			err:      err,
			duration: time.Since(start),
		}
	})

	c.meta.recordScrape(e.scraper.Name(), out)

	for _, s := range out.samples {
		m, mErr := c.toMetric(e.scraper.Name(), s)
		if mErr != nil {
			c.cfg.logger.Infof("scraper %q sample %q discarded: %v", e.scraper.Name(), s.Name, mErr)
			continue
		}
		ch <- m
	}
}

// toMetric builds the prometheus.Desc on the fly per sample. We don't
// pre-register descriptors because scrapers may emit samples with
// scraper-provided labels we can't predict.
func (c *Collector) toMetric(scraperName string, s Sample) (prometheus.Metric, error) {
	if s.Name == "" {
		return nil, fmt.Errorf("sample missing Name")
	}

	fqName := s.Name
	if c.cfg.namespace != "" {
		fqName = c.cfg.namespace + "_" + fqName
	}

	// Merge const labels with per-sample labels. Per-sample wins on conflict.
	mergedConst := make(map[string]string, len(c.cfg.labels))
	for k, v := range c.cfg.labels {
		mergedConst[k] = v
	}

	// Variable labels = per-sample labels not already const.
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		// Per-sample overrides const: move it from const-bag to variable-bag
		// so the value can vary per Collect round.
		delete(mergedConst, k) // no-op if key absent — safe and shorter than guard
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, len(keys))
	for i, k := range keys {
		values[i] = s.Labels[k]
	}

	desc := prometheus.NewDesc(fqName, s.Help, keys, mergedConst)
	return s.toPromMetric(desc, values)
}

// Handler returns a ready-to-mount http.Handler that exposes this Collector
// alone (i.e. backed by a private prometheus.Registry — your application's
// other metrics are not affected).
//
// All DB queries are bound by scrapeTimeout — HTTP client disconnect does not
// propagate to DB queries. Use WithScrapeTimeout to tune the timeout.
func (c *Collector) Handler() http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// Scrapers returns the scrapers registered on this Collector in
// registration order. Read-only — mutating the returned slice does NOT
// affect the Collector.
func (c *Collector) Scrapers() []Scraper {
	out := make([]Scraper, len(c.entries))
	for i, e := range c.entries {
		out[i] = e.scraper
	}
	return out
}

// DisabledScrapers lists scrapers whose Probe rejected them, along with
// the error returned. Stable across the Collector's lifetime.
func (c *Collector) DisabledScrapers() map[string]error {
	out := make(map[string]error)
	for _, e := range c.entries {
		if e.disabled {
			out[e.scraper.Name()] = e.probeErr
		}
	}
	return out
}

// Static guard: Collector must satisfy prometheus.Collector.
var _ prometheus.Collector = (*Collector)(nil)
