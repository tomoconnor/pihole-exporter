package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tomoconnor/pihole-exporter/internal/metrics"
	"github.com/tomoconnor/pihole-exporter/internal/pihole"
	"github.com/tomoconnor/pihole-exporter/internal/piholetest"
	"github.com/tomoconnor/pihole-exporter/internal/server"
)

func init() { metrics.Init() }

// scrape issues one /metrics request against h, giving up after deadline, and
// reports how long the handler took to respond.
func scrape(t *testing.T, h http.Handler, deadline time.Duration) (time.Duration, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading /metrics body: %v", err)
	}
	return elapsed, string(body)
}

// TestMetricsDoesNotHangAfterTimeout is the regression test for
// eko/pihole-exporter#318 and #328.
//
// A /metrics request that timed out used to spawn a second reader on the
// client's shared status channel. That ghost reader consumed the *next*
// request's result, so the next request blocked until the one after it
// arrived. Every subsequent scrape then stalled for roughly one scrape
// interval, for the lifetime of the process, and only a restart cleared it.
//
// The shape that matters: after one timed-out scrape, the following scrape
// must complete on its own.
func TestMetricsDoesNotHangAfterTimeout(t *testing.T) {
	fake := piholetest.New(t, piholetest.Options{Validity: 1800})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	h := server.NewServer("127.0.0.1", 9617, []*pihole.Client{client}).Handler()

	// Wedge Pi-hole, then scrape. The handler should give up at its deadline.
	release := fake.Block()
	if elapsed, _ := scrape(t, h, 300*time.Millisecond); elapsed > 2*time.Second {
		t.Fatalf("timed-out scrape took %s, expected it to abandon collection promptly", elapsed)
	}
	release()

	// The bug: this one used to block until a third request showed up, which
	// in a test means blocking until its own deadline.
	elapsed, body := scrape(t, h, 5*time.Second)
	if elapsed > 2*time.Second {
		t.Fatalf("scrape after a timeout took %s; the handler is hanging on a stale result", elapsed)
	}
	if !strings.Contains(body, "pihole_domains_being_blocked") {
		t.Fatalf("scrape after a timeout returned no Pi-hole metrics; body was:\n%s", body)
	}
}

// TestMetricsRepeatedScrapesStayFast covers the cascade rather than the single
// hop: ten scrapes in a row, each of which must stand on its own.
func TestMetricsRepeatedScrapesStayFast(t *testing.T) {
	fake := piholetest.New(t, piholetest.Options{Validity: 1800})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	h := server.NewServer("127.0.0.1", 9617, []*pihole.Client{client}).Handler()

	for i := range 10 {
		elapsed, body := scrape(t, h, 5*time.Second)
		if elapsed > 2*time.Second {
			t.Fatalf("scrape %d took %s", i, elapsed)
		}
		if !strings.Contains(body, "pihole_domains_being_blocked") {
			t.Fatalf("scrape %d returned no Pi-hole metrics", i)
		}
	}
}

// TestMetricsAbandonedCollectorsDoNotLeak checks that giving up on a scrape
// tears the collection down rather than parking a goroutine on a channel send
// forever. The old handler leaked one goroutine per timed-out scrape.
func TestMetricsAbandonedCollectorsDoNotLeak(t *testing.T) {
	fake := piholetest.New(t, piholetest.Options{Validity: 1800})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	h := server.NewServer("127.0.0.1", 9617, []*pihole.Client{client}).Handler()

	// Warm the session so the baseline doesn't include auth machinery.
	scrape(t, h, 5*time.Second)
	settle()
	baseline := runtime.NumGoroutine()

	release := fake.Block()
	for range 20 {
		scrape(t, h, 100*time.Millisecond)
	}
	release()
	settle()

	if grown := runtime.NumGoroutine() - baseline; grown > 5 {
		t.Fatalf("20 abandoned scrapes leaked %d goroutines (baseline %d)", grown, baseline)
	}
}

// settle gives cancelled goroutines a moment to unwind before counting.
func settle() {
	for range 20 {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}
