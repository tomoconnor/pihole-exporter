package pihole_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eko/pihole-exporter/internal/metrics"
	"github.com/eko/pihole-exporter/internal/piholetest"
)

func init() { metrics.Init() }

// TestAuthenticationsPerScrape is the regression test for the bug this fork
// exists to fix.
//
// POST /api/auth costs a real Pi-hole v6 1.5-4s of single-threaded CPU: the
// API password is hashed with a deliberately expensive KDF, and upstream
// Pi-hole considers that cost intentional. An already-authenticated query, by
// contrast, costs about 5ms. So the exporter must authenticate at most once
// per scrape, and normally not at all -- the session is reused across
// endpoints and across scrapes.
//
// The failure mode this guards against is authenticating once per API
// endpoint, which turned a ~50ms scrape into a ~30s one.
func TestAuthenticationsPerScrape(t *testing.T) {
	// A scrape hits seven Pi-hole endpoints. Any per-endpoint authentication
	// shows up here as a count far above one.
	const scrapes = 5

	tests := []struct {
		name string
		opts piholetest.Options
		// maxAuths is the total number of POST /api/auth calls permitted
		// across all scrapes in the case.
		maxAuths int64
	}{
		{
			name:     "typical session validity",
			opts:     piholetest.Options{Validity: 300},
			maxAuths: 1,
		},
		{
			name:     "long session validity",
			opts:     piholetest.Options{Validity: 1800},
			maxAuths: 1,
		},
		{
			// Pi-hole reporting no validity must not be read as "expired
			// already". This is the case that produced one authentication per
			// endpoint, i.e. seven per scrape.
			name:     "validity absent or zero",
			opts:     piholetest.Options{Validity: 0},
			maxAuths: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := piholetest.New(t, tc.opts)
			client := fake.Client(t, 5*time.Second)
			defer client.Close()

			for i := range scrapes {
				if err := client.CollectMetrics(context.Background()); err != nil {
					t.Fatalf("scrape %d: %v", i, err)
				}
				if got := fake.AuthCount(); got > tc.maxAuths {
					t.Fatalf("after %d scrape(s): %d authentications, want at most %d "+
						"(the exporter is authenticating per endpoint, not per session)",
						i+1, got, tc.maxAuths)
				}
			}

			if got := fake.AuthCount(); got != tc.maxAuths {
				t.Errorf("after %d scrapes: %d authentications, want exactly %d", scrapes, got, tc.maxAuths)
			}
			t.Logf("%d scrapes, %d fetches, %d authentications", scrapes, fake.FetchCount(), fake.AuthCount())
		})
	}
}

// TestConcurrentScrapesDoNotStampede checks that simultaneous collections from
// a cold start collapse onto one authentication rather than each starting
// their own. Pi-hole hashes passwords on a single thread, so N parallel
// authentications do not cost N times as much -- they cost considerably more.
func TestConcurrentScrapesDoNotStampede(t *testing.T) {
	const collectors = 25

	// A slow /api/auth widens the window in which a stampede could occur.
	fake := piholetest.New(t, piholetest.Options{Validity: 300, AuthDelay: 150 * time.Millisecond})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	var wg sync.WaitGroup
	errs := make(chan error, collectors)

	start := make(chan struct{})
	for range collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := client.CollectMetrics(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent scrape failed: %v", err)
	}

	if got := fake.AuthCount(); got != 1 {
		t.Fatalf("%d concurrent scrapes caused %d authentications, want exactly 1", collectors, got)
	}
}

// TestSessionExpiryTriggersSingleReauth covers the only two reasons we should
// ever authenticate again: a 401 from Pi-hole, or a session we know has
// lapsed. One expiry must cost exactly one authentication, not one per
// endpoint, and the scrape must still succeed.
func TestSessionExpiryTriggersSingleReauth(t *testing.T) {
	fake := piholetest.New(t, piholetest.Options{Validity: 300})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	if err := client.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("initial scrape: %v", err)
	}
	if got := fake.AuthCount(); got != 1 {
		t.Fatalf("initial scrape: %d authentications, want 1", got)
	}

	// Pi-hole restarts, or evicts our session because max_sessions was hit.
	fake.ExpireSession()

	if err := client.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("scrape after session expiry: %v", err)
	}
	if got := fake.AuthCount(); got != 2 {
		t.Fatalf("scrape after session expiry: %d authentications total, want 2 "+
			"(one re-auth, not one per endpoint)", got)
	}

	// And the recovered session is then reused like any other.
	if err := client.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("scrape after recovery: %v", err)
	}
	if got := fake.AuthCount(); got != 2 {
		t.Fatalf("scrape after recovery: %d authentications total, want 2", got)
	}
}

// TestConcurrentScrapesShareOneReauthAfterExpiry is the 401 equivalent of the
// stampede test: many collectors discovering the same dead session at once
// must produce one re-authentication between them.
func TestConcurrentScrapesShareOneReauthAfterExpiry(t *testing.T) {
	const collectors = 25

	fake := piholetest.New(t, piholetest.Options{Validity: 300, AuthDelay: 150 * time.Millisecond})
	client := fake.Client(t, 5*time.Second)
	defer client.Close()

	if err := client.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("initial scrape: %v", err)
	}
	fake.ExpireSession()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = client.CollectMetrics(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	// One authentication to begin with, then at most one more to recover.
	if got := fake.AuthCount(); got > 2 {
		t.Fatalf("%d collectors hitting an expired session caused %d authentications, want at most 2", collectors, got)
	}
}
