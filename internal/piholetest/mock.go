// Package piholetest provides an in-process fake of the Pi-hole v6 API for
// exercising the exporter end to end.
//
// It exists mainly to make two regression tests possible: one counting how
// often the exporter authenticates (see eko/pihole-exporter#318), and one
// checking that a slow endpoint cannot wedge later /metrics requests
// (see eko/pihole-exporter#328).
package piholetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomoconnor/pihole-exporter/config"
	"github.com/tomoconnor/pihole-exporter/internal/pihole"
)

// Options configures the fake.
type Options struct {
	// Validity is the session lifetime, in seconds, reported by /api/auth.
	// Pi-hole's own default is 300. Zero means "report 0", which is what the
	// exporter used to treat as "expired immediately".
	Validity int

	// AuthDelay simulates the cost of Pi-hole's password KDF. On a real
	// Pi-hole v6 this is 1.5-4s; /api/auth is the only expensive endpoint.
	AuthDelay time.Duration

	// FetchDelay is applied to every non-auth endpoint.
	FetchDelay time.Duration
}

// Server is a fake Pi-hole v6 API.
type Server struct {
	*httptest.Server

	opts Options

	authCount  atomic.Int64
	fetchCount atomic.Int64

	mu      sync.Mutex
	sid     string
	expired bool          // when set, authenticated endpoints answer 401
	gate    chan struct{} // when non-nil, fetches block on it
}

// New starts a fake Pi-hole. The caller must Close it.
func New(t *testing.T, opts Options) *Server {
	t.Helper()

	s := &Server{opts: opts, sid: "sid-1"}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth", func(w http.ResponseWriter, r *http.Request) {
		s.authCount.Add(1)
		time.Sleep(s.opts.AuthDelay)

		s.mu.Lock()
		s.expired = false
		s.sid = fmt.Sprintf("sid-%d", s.authCount.Load())
		sid := s.sid
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"session":{"valid":true,"totp":false,"sid":%q,"csrf":"csrf","validity":%d,"message":"password correct"}}`, sid, s.opts.Validity)
	})

	authed := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			expired, sid, gate := s.expired, s.sid, s.gate
			s.mu.Unlock()

			if expired || r.Header.Get("X-FTL-SID") != sid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			if gate != nil {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}

			s.fetchCount.Add(1)
			time.Sleep(s.opts.FetchDelay)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}
	}

	mux.HandleFunc("/api/stats/summary", authed(`{"queries":{"total":100,"blocked":10,"percent_blocked":10.0,"unique_domains":5,"forwarded":50,"cached":40,"frequency":1.5,"types":{"A":60,"AAAA":40},"replies":{"UNKNOWN":0,"NODATA":1,"NXDOMAIN":2,"CNAME":3,"IP":4,"DOMAIN":5,"RRNAME":6,"SERVFAIL":7,"REFUSED":8,"NOTIMP":9,"OTHER":10,"DNSSEC":11,"NONE":12,"BLOB":13}},"clients":{"active":2,"total":3},"gravity":{"domains_being_blocked":1000,"last_update":0}}`))
	mux.HandleFunc("/api/stats/top_domains", authed(`{"domains":[{"domain":"example.com","count":10}],"total_queries":100,"blocked_queries":10}`))
	mux.HandleFunc("/api/stats/top_clients", authed(`{"clients":[{"ip":"10.0.0.1","name":"host","count":10}],"total_queries":100,"blocked_queries":10}`))
	mux.HandleFunc("/api/stats/upstreams", authed(`{"upstreams":[{"ip":"1.1.1.1","name":"cf","port":53,"count":10,"statistics":{"response":0.1,"variance":0.01}}],"forwarded_queries":10,"total_queries":100}`))
	mux.HandleFunc("/api/dns/blocking", authed(`{"blocking":"enabled","timer":null}`))

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// AuthCount is the number of POST /api/auth calls served so far.
func (s *Server) AuthCount() int64 { return s.authCount.Load() }

// FetchCount is the number of successful authenticated calls served so far.
func (s *Server) FetchCount() int64 { return s.fetchCount.Load() }

// ExpireSession makes every authenticated endpoint answer 401 until the next
// successful /api/auth, mimicking a Pi-hole restart or a session evicted
// because max_sessions was reached.
func (s *Server) ExpireSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired = true
}

// Block makes authenticated endpoints hang until the returned func is called.
func (s *Server) Block() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.gate = nil
			s.mu.Unlock()
			close(gate)
		})
	}
}

// Client returns an exporter client pointed at this fake.
func (s *Server) Client(t *testing.T, timeout time.Duration) *pihole.Client {
	t.Helper()

	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parsing fake Pi-hole URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing fake Pi-hole port: %v", err)
	}

	return pihole.NewClient(
		&config.Config{
			PIHoleProtocol: "http",
			PIHoleHostname: u.Hostname(),
			PIHolePort:     uint16(port),
			PIHolePassword: "secret",
		},
		&config.EnvConfig{Timeout: timeout},
	)
}
