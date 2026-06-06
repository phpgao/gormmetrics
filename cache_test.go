package gormmetrics

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCacheTTLHit verifies that a second call within the TTL window
// returns the cached outcome without invoking fetch again.
func TestCacheTTLHit(t *testing.T) {
	c := newScraperCache(1 * time.Second)
	calls := 0
	fetch := func() scrapeOutcome {
		calls++
		return scrapeOutcome{samples: []Sample{{Name: "x", Type: Gauge, Value: float64(calls)}}}
	}

	now := time.Now()
	out1 := c.getOrFetch(now, fetch)
	out2 := c.getOrFetch(now.Add(500*time.Millisecond), fetch)

	if calls != 1 {
		t.Fatalf("expected 1 fetch call (TTL hit), got %d", calls)
	}
	if out1.samples[0].Value != out2.samples[0].Value {
		t.Fatalf("cached value mismatch: %v vs %v", out1.samples[0].Value, out2.samples[0].Value)
	}
}

// TestCacheTTLMiss verifies that a call after the TTL has elapsed
// re-invokes fetch and updates the cache.
func TestCacheTTLMiss(t *testing.T) {
	c := newScraperCache(100 * time.Millisecond)
	var calls atomic.Int32
	fetch := func() scrapeOutcome {
		calls.Add(1)
		return scrapeOutcome{samples: []Sample{{Name: "x", Type: Gauge, Value: float64(calls.Load())}}}
	}

	now := time.Now()
	out1 := c.getOrFetch(now, fetch)
	out2 := c.getOrFetch(now.Add(200*time.Millisecond), fetch) // exceeds TTL

	if calls.Load() != 2 {
		t.Fatalf("expected 2 fetch calls (TTL miss), got %d", calls.Load())
	}
	// out2 should have a different value (second fetch returns 2)
	if out2.samples[0].Value == out1.samples[0].Value {
		t.Fatalf("expected fresh value after TTL expiry")
	}
}

// TestCacheTTLZero disables caching: every call must invoke fetch.
func TestCacheTTLZero(t *testing.T) {
	c := newScraperCache(0)
	calls := 0
	fetch := func() scrapeOutcome {
		calls++
		return scrapeOutcome{samples: []Sample{{Name: "x", Type: Gauge, Value: 1}}}
	}

	now := time.Now()
	_ = c.getOrFetch(now, fetch)
	_ = c.getOrFetch(now, fetch)

	if calls != 2 {
		t.Fatalf("expected 2 fetch calls (TTL=0, no caching), got %d", calls)
	}
}

// TestCacheSingleFlight verifies that concurrent calls to getOrFetch
// during a cache miss result in exactly one fetch invocation.
func TestCacheSingleFlight(t *testing.T) {
	c := newScraperCache(1 * time.Second)

	slowFetch := make(chan struct{})
	fetchCalls := atomic.Int32{}
	fetch := func() scrapeOutcome {
		fetchCalls.Add(1)
		<-slowFetch // block until test releases
		return scrapeOutcome{samples: []Sample{{Name: "x", Type: Gauge, Value: 1}}}
	}

	const goroutines = 10
	var wg sync.WaitGroup
	results := make([]scrapeOutcome, goroutines)

	// All goroutines start at roughly the same time (no initial cache).
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = c.getOrFetch(time.Now(), fetch)
		}(i)
	}

	time.Sleep(50 * time.Millisecond) // let all goroutines park on flight.Lock
	close(start)
	time.Sleep(50 * time.Millisecond) // let one goroutine enter fetch
	close(slowFetch)                  // release the one fetch goroutine
	wg.Wait()

	if n := fetchCalls.Load(); n != 1 {
		t.Fatalf("expected exactly 1 fetch call (single-flight), got %d", n)
	}
}

// TestCacheConcurrentMiss verifies that after a TTL expiry, concurrent
// callers only invoke fetch once (the re-fetch single-flight).
func TestCacheConcurrentMiss(t *testing.T) {
	c := newScraperCache(50 * time.Millisecond)
	fetchCalls := atomic.Int32{}
	fetch := func() scrapeOutcome {
		fetchCalls.Add(1)
		time.Sleep(50 * time.Millisecond) // simulate slow query
		return scrapeOutcome{samples: []Sample{{Name: "x", Type: Gauge, Value: float64(fetchCalls.Load())}}}
	}

	// Prime the cache.
	c.getOrFetch(time.Now(), fetch)
	fetchCalls.Store(0) // reset counter after priming

	// Wait for TTL to expire, then hammer concurrently.
	time.Sleep(100 * time.Millisecond)

	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.getOrFetch(time.Now(), fetch)
		}()
	}
	wg.Wait()

	if n := fetchCalls.Load(); n != 1 {
		t.Fatalf("expected exactly 1 re-fetch call after TTL expiry, got %d", n)
	}
}
