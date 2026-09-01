package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/tomoconnor/pihole-exporter/internal/pihole"
)

// collectionTimeout bounds how long a single /metrics request will spend
// scraping Pi-hole before it gives up and serves what it has.
const collectionTimeout = 10 * time.Second

// Server is the struct for the HTTP server.
type Server struct {
	httpServer *http.Server
}

// NewServer method initializes a new HTTP server instance and associates
// the different routes that will be used by Prometheus (metrics) or for monitoring (readiness, liveness).
func NewServer(addr string, port uint16, clients []*pihole.Client) *Server {
	mux := http.NewServeMux()
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", addr, port),
		Handler: mux,
	}

	s := &Server{
		httpServer: httpServer,
	}

	mux.HandleFunc("/metrics", func(writer http.ResponseWriter, request *http.Request) {
		log.Debugf("request.Header: %+v\n", request.Header)

		// Bound the whole collection. Deriving from request.Context() means a
		// client that hangs up also cancels the Pi-hole requests.
		ctx, cancel := context.WithTimeout(request.Context(), collectionTimeout)
		defer cancel()

		// Buffered for every client, so a goroutine whose result nobody is
		// waiting for any more can still send and exit rather than leaking.
		type result struct {
			hostname string
			err      error
		}
		results := make(chan result, len(clients))

		for _, client := range clients {
			go func(c *pihole.Client) {
				results <- result{hostname: c.GetHostname(), err: c.CollectMetrics(ctx)}
			}(client)
		}

		for range clients {
			select {
			case res := <-results:
				if res.err != nil {
					log.Warnf("An error occurred while contacting %s: %v", res.hostname, res.err)
				}
			case <-ctx.Done():
				// Out of time. Serve whatever the finished collectors managed
				// to record; cancelling ctx unblocks the stragglers.
				log.Warnf("Metrics collection gave up (%v), serving partial metrics", ctx.Err())
				promhttp.Handler().ServeHTTP(writer, request)
				return
			}
		}

		promhttp.Handler().ServeHTTP(writer, request)
	})

	mux.Handle("/readiness", s.readinessHandler())
	mux.Handle("/liveness", s.livenessHandler())

	return s
}

// Handler exposes the server's routes, so they can be exercised without
// binding a port.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// ListenAndServe method serves HTTP requests.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Stop method stops the HTTP server (so the exporter become unavailable).
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	s.httpServer.Shutdown(ctx)
}

func (s *Server) readinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		status := http.StatusNotFound
		if s.isReady() {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	}
}

func (s *Server) livenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) isReady() bool {
	return s.httpServer != nil
}
