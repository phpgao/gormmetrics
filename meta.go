package gormmetrics

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// ErrorClassifier categorises an error into a label used in scrape_errors
// metrics. The default implementation uses string matching; users can
// substitute it via WithErrorClassifier to use driver-specific types
// (e.g. *mysql.MySQLError, *pgconn.PgError) for precise classification.
type ErrorClassifier func(error) string

// DefaultErrorClassifier is the package-level classifier. Users may replace
// it to change classification globally without passing WithErrorClassifier
// to every New call.
var DefaultErrorClassifier ErrorClassifier = classifyErr

// classifyErr is the default ErrorClassifier. It buckets errors into
// coarse categories: timeout, canceled, permission_denied, connectivity,
// query, other. String matching is used so it works with any driver.
func classifyErr(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "context canceled"):
		return "canceled"
	case strings.Contains(msg, "access denied"),
		strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "must be superuser"),
		strings.Contains(msg, "insufficient privilege"):
		return "permission_denied"
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "connection reset"):
		return "connectivity"
	case strings.Contains(msg, "syntax error"),
		strings.Contains(msg, "does not exist"),
		strings.Contains(msg, "unknown column"):
		return "query"
	}
	return "other"
}

// metaMetrics is the always-on telemetry the Collector emits about itself,
// independent of any business scraper. Operators alert on these so they
// can distinguish "DB is broken" from "metric collection is broken".
type metaMetrics struct {
	upDesc       *prometheus.Desc
	successDesc  *prometheus.Desc
	durationDesc *prometheus.Desc
	errorsDesc   *prometheus.Desc
	samplesDesc  *prometheus.Desc
	disabledDesc *prometheus.Desc

	mu               sync.Mutex
	up               float64
	lastOutcomes     map[string]scrapeOutcome
	errorCounts      map[errKey]uint64
	disabledScrapers map[string]string // scraper name -> reason
	classifier       ErrorClassifier
}

type errKey struct {
	scraper string
	class   string
}

func newMetaMetrics(namespace string, constLabels map[string]string, classifier ErrorClassifier) *metaMetrics {
	prefix := "gormmetrics_"
	if namespace != "" {
		prefix = namespace + "_" + prefix
	}
	cl := prometheus.Labels(constLabels)

	return &metaMetrics{
		upDesc: prometheus.NewDesc(
			prefix+"up",
			"Whether the database was reachable on the last scrape (1=up, 0=down).",
			nil, cl,
		),
		successDesc: prometheus.NewDesc(
			prefix+"scrape_success",
			"Whether the named scraper succeeded on the last scrape (1=ok, 0=error).",
			[]string{"scraper"}, cl,
		),
		durationDesc: prometheus.NewDesc(
			prefix+"scrape_duration_seconds",
			"Duration of the last scrape, in seconds.",
			[]string{"scraper"}, cl,
		),
		errorsDesc: prometheus.NewDesc(
			prefix+"scrape_errors",
			"Current number of scrape errors partitioned by class. Resets to zero on process restart.",
			[]string{"scraper", "error"}, cl,
		),
		samplesDesc: prometheus.NewDesc(
			prefix+"scrape_samples",
			"Number of samples produced by the last scrape.",
			[]string{"scraper"}, cl,
		),
		disabledDesc: prometheus.NewDesc(
			prefix+"scraper_disabled",
			"Set to 1 for scrapers permanently disabled by a probe failure.",
			[]string{"scraper", "reason"}, cl,
		),

		lastOutcomes:     make(map[string]scrapeOutcome),
		errorCounts:      make(map[errKey]uint64),
		disabledScrapers: make(map[string]string),
		classifier:       classifier,
	}
}

func (m *metaMetrics) describe(ch chan<- *prometheus.Desc) {
	ch <- m.upDesc
	ch <- m.successDesc
	ch <- m.durationDesc
	ch <- m.errorsDesc
	ch <- m.samplesDesc
	ch <- m.disabledDesc
}

func (m *metaMetrics) recordUp(up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if up {
		m.up = 1
	} else {
		m.up = 0
	}
}

func (m *metaMetrics) recordScrape(scraperName string, out scrapeOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOutcomes[scraperName] = out
	if out.err != nil {
		class := classifyErr(out.err)
		if m.classifier != nil {
			if c := m.classifier(out.err); c != "" {
				class = c
			}
		}
		m.errorCounts[errKey{scraper: scraperName, class: class}]++
	}
}

func (m *metaMetrics) recordDisabled(scraperName string) {
	// Pure "still disabled" recording: no state change, just keeps the
	// gauge visible on every Collect round.
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.disabledScrapers[scraperName]; !ok {
		m.disabledScrapers[scraperName] = "(unknown)"
	}
}

func (m *metaMetrics) observeDisabled(scraperName string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	class := classifyErr(err)
	if m.classifier != nil {
		if c := m.classifier(err); c != "" {
			class = c
		}
	}
	m.disabledScrapers[scraperName] = class
}

func (m *metaMetrics) collect(ch chan<- prometheus.Metric) {
	// Snapshot all mutable state under lock, then emit metrics without
	// holding the lock. This avoids blocking recordScrape/recordUp during
	// metric serialisation.
	m.mu.Lock()
	upVal := m.up
	outcomes := make(map[string]scrapeOutcome, len(m.lastOutcomes))
	for k, v := range m.lastOutcomes {
		outcomes[k] = v
	}
	errors := make(map[errKey]uint64, len(m.errorCounts))
	for k, v := range m.errorCounts {
		errors[k] = v
	}
	disabled := make(map[string]string, len(m.disabledScrapers))
	for k, v := range m.disabledScrapers {
		disabled[k] = v
	}
	m.mu.Unlock()

	ch <- prometheus.MustNewConstMetric(m.upDesc, prometheus.GaugeValue, upVal)

	for name, out := range outcomes {
		ok := 1.0
		if out.err != nil {
			ok = 0
		}
		ch <- prometheus.MustNewConstMetric(m.successDesc, prometheus.GaugeValue, ok, name)
		ch <- prometheus.MustNewConstMetric(m.durationDesc, prometheus.GaugeValue, out.duration.Seconds(), name)
		ch <- prometheus.MustNewConstMetric(m.samplesDesc, prometheus.GaugeValue, float64(len(out.samples)), name)
	}
	for k, v := range errors {
		ch <- prometheus.MustNewConstMetric(m.errorsDesc, prometheus.GaugeValue, float64(v), k.scraper, k.class)
	}
	for name, reason := range disabled {
		ch <- prometheus.MustNewConstMetric(m.disabledDesc, prometheus.GaugeValue, 1, name, reason)
	}
}
