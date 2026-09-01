package pihole

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	MaxResponseSize = 1 * 1024 * 1024 // 1MB (for DoS protection)

	// defaultSessionValidity is used when Pi-hole does not tell us how long
	// the session is good for. It matches Pi-hole's own default. Treating an
	// absent or zero validity as "already expired" is what made the exporter
	// re-authenticate before every single API call.
	defaultSessionValidity = 300 * time.Second

	// sessionRefreshMargin renews a session slightly before it lapses, so a
	// scrape does not discover the expiry halfway through and pay for an
	// authentication mid-collection.
	sessionRefreshMargin = 30 * time.Second

	// minSessionValidity floors how often we are willing to re-authenticate,
	// however short a validity Pi-hole reports. POST /api/auth costs Pi-hole
	// 1.5-4s of single-threaded CPU because the API password is hashed with a
	// deliberately expensive KDF, so this is a load-bearing floor, not a tidy-up.
	minSessionValidity = 30 * time.Second

	// minAuthTimeout gives authentication more headroom than an ordinary
	// query. The default client timeout is 5s, which is fine for a query that
	// costs ~5ms but marginal for one that costs 1.5-4s and spikes past 5s
	// when FTL is busy -- loading gravity at startup, for instance. Observed
	// in the wild: the exporter's first scrape after a restart failing with
	// "context deadline exceeded" on /api/auth while ordinary scrapes were
	// answering in well under a second.
	//
	// The caller's context still bounds this, so a scrape that has run out of
	// budget aborts regardless. This only stops the client timeout from being
	// the tighter of the two.
	minAuthTimeout = 15 * time.Second
)

type APIClient struct {
	BaseURL string
	Client  *http.Client
	// authClient is Client with a longer timeout, used only for /api/auth.
	// It shares Client's transport, so both use the same connection pool.
	authClient *http.Client
	password   string

	mu       sync.Mutex
	sid      string
	expires  time.Time
	inflight *authCall
}

// authCall is one in-flight POST /api/auth that later arrivals wait on rather
// than starting their own.
type authCall struct {
	done chan struct{}
	sid  string
	err  error
}

type authResponse struct {
	Session struct {
		Valid    bool   `json:"valid"`
		SID      string `json:"sid"`
		Validity int    `json:"validity"`
	} `json:"session"`
}

// NewAPIClient initializes and returns a new APIClient.
func NewAPIClient(baseURL string, password string, timeout time.Duration, skipTLSVerification bool) *APIClient {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipTLSVerification,
		},
	}

	return &APIClient{
		BaseURL:  baseURL,
		password: password,
		Client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		authClient: &http.Client{
			Timeout:   max(timeout, minAuthTimeout),
			Transport: transport,
		},
	}
}

// cachedSession returns the current session ID if we hold one that has not
// reached its refresh point.
func (c *APIClient) cachedSession() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sid != "" && time.Now().Before(c.expires) {
		return c.sid, true
	}
	return "", false
}

// sessionLifetime converts the validity Pi-hole reports into how long we will
// actually hold the session for.
func sessionLifetime(validitySeconds int) time.Duration {
	validity := time.Duration(validitySeconds) * time.Second
	if validity <= 0 {
		log.Debugf("Pi-hole reported a session validity of %ds; assuming %s", validitySeconds, defaultSessionValidity)
		validity = defaultSessionValidity
	}

	if lifetime := validity - sessionRefreshMargin; lifetime >= minSessionValidity {
		return lifetime
	}
	return minSessionValidity
}

// Authenticate obtains a session, reusing a cached one when it is still valid.
func (c *APIClient) Authenticate(ctx context.Context) error {
	_, err := c.session(ctx)
	return err
}

// session returns a usable session ID, authenticating only if we do not have
// one. Concurrent callers collapse onto a single authentication.
func (c *APIClient) session(ctx context.Context) (string, error) {
	if sid, ok := c.cachedSession(); ok {
		return sid, nil
	}
	return c.authenticate(ctx, "")
}

// authenticate performs POST /api/auth, collapsing concurrent callers so that
// only one authentication is ever in flight.
//
// stale, when non-empty, names the session the caller found to be rejected. If
// another goroutine has already replaced it we return the replacement instead
// of authenticating again: that is what stops a burst of 401s -- one per
// endpoint in a scrape -- from becoming a burst of authentications.
func (c *APIClient) authenticate(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()

	if stale != "" && c.sid != stale && c.sid != "" {
		sid := c.sid
		c.mu.Unlock()
		log.Debugf("Session already renewed by another collector, reusing it")
		return sid, nil
	}

	if call := c.inflight; call != nil {
		c.mu.Unlock()
		log.Debugf("Authentication already in flight, waiting for it")
		select {
		case <-call.done:
			return call.sid, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	call := &authCall{done: make(chan struct{})}
	c.inflight = call
	c.mu.Unlock()

	sid, validity, err := c.doAuthenticate(ctx)

	c.mu.Lock()
	if err == nil {
		c.sid = sid
		c.expires = time.Now().Add(sessionLifetime(validity))
	}
	c.inflight = nil
	call.sid, call.err = sid, err
	c.mu.Unlock()

	close(call.done)
	return sid, err
}

// doAuthenticate is the actual network call. Exactly one goroutine at a time
// reaches it for a given APIClient.
func (c *APIClient) doAuthenticate(ctx context.Context) (sid string, validity int, err error) {
	url := fmt.Sprintf("%s/api/auth", c.BaseURL)
	payload, err := json.Marshal(map[string]string{"password": c.password})
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal authentication payload: %w", err)
	}

	log.Debugf("Authenticating to %s", c.BaseURL)

	ctx, cancel := context.WithTimeout(ctx, c.authClient.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create authentication request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.authClient.Do(req)
	if err != nil {
		log.Errorf("Authentication request failed: %v", err)
		return "", 0, fmt.Errorf("authentication request failed: %w", err)
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("authentication failed, status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if err != nil {
		return "", 0, fmt.Errorf("failed to read authentication response: %w", err)
	}

	var authResp authResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", 0, fmt.Errorf("failed to parse authentication response: %w", err)
	}

	if !authResp.Session.Valid {
		return "", 0, fmt.Errorf("authentication unsuccessful")
	}

	log.Debugf("Authentication successful, session valid for %ds", authResp.Session.Validity)
	return authResp.Session.SID, authResp.Session.Validity, nil
}

// FetchData makes a GET request to the specified endpoint and parses the
// response.
//
// The cached session is reused across endpoints and across scrapes. A 401 --
// Pi-hole restarted, or evicted our session because max_sessions was reached --
// triggers exactly one re-authentication and one retry.
//
// The request is bound to ctx as well as to the client timeout, so a caller
// that has given up aborts the in-flight request instead of leaving it running.
func (c *APIClient) FetchData(ctx context.Context, endpoint string, result interface{}) error {
	sid, err := c.session(ctx)
	if err != nil {
		return err
	}

	body, status, err := c.get(ctx, endpoint, sid)
	if err != nil {
		return err
	}

	if status == http.StatusUnauthorized {
		log.Debugf("Session rejected by %s, re-authenticating", c.BaseURL)

		sid, err = c.authenticate(ctx, sid)
		if err != nil {
			return fmt.Errorf("re-authentication after 401 failed: %w", err)
		}

		if body, status, err = c.get(ctx, endpoint, sid); err != nil {
			return err
		}
	}

	if status != http.StatusOK {
		return fmt.Errorf("non-200 status code: %d", status)
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("failed to parse JSON response: %w", err)
	}

	log.Debugf("Successfully fetched data from endpoint: %s", endpoint)
	return nil
}

// get performs a single authenticated GET and returns the body and status. A
// non-200 status is not an error here: FetchData decides what to do with it.
func (c *APIClient) get(ctx context.Context, endpoint, sid string) ([]byte, int, error) {
	url := fmt.Sprintf("%s%s", c.BaseURL, endpoint)
	log.Debugf("Fetching data from %s", url)

	ctx, cancel := context.WithTimeout(ctx, c.Client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-FTL-SID", sid)
	req.Header.Set("X-Content-Type-Options", "nosniff")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch data from %s: %w", url, err)
	}
	defer closeBody(resp)

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, resp.StatusCode, nil
}

func closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		log.Warnf("Failed to close response body: %v", err)
	}
}

// Close cleans up resources used by the API client
func (c *APIClient) Close() {
	// Close the transport to ensure no connection leaks
	if transport, ok := c.Client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
