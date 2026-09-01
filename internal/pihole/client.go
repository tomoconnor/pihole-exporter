package pihole

import (
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"

	"github.com/tomoconnor/pihole-exporter/config"
	"github.com/tomoconnor/pihole-exporter/internal/metrics"
)

// Client struct is a Pi-hole client to request an instance of a Pi-hole ad blocker.
//
// A Client holds no per-request state. Collection results are returned to the
// caller rather than published on a channel owned by the Client: the previous
// design shared one buffered channel across every /metrics request, which let
// a late result from one request be consumed by the next one.
type Client struct {
	apiClient APIClient
	config    *config.Config
}

// NewClient method initializes a new Pi-hole client.
func NewClient(config *config.Config, envConfig *config.EnvConfig) *Client {
	err := config.Validate()
	if err != nil {
		log.Fatalf("err: couldn't validate passed Config: %v", err)
	}

	log.Debugf("Creating client for host %s with protocol %s and port %d", config.PIHoleHostname, config.PIHoleProtocol, config.PIHolePort)

	return &Client{
		config:    config,
		apiClient: *NewAPIClient(fmt.Sprintf("%s://%s:%d", config.PIHoleProtocol, config.PIHoleHostname, config.PIHolePort), config.PIHolePassword, envConfig.Timeout, envConfig.SkipTLSVerification),
	}
}

func (c *Client) String() string {
	return c.config.PIHoleHostname
}

// CollectMetrics scrapes the Pi-hole API and updates the Prometheus gauges.
//
// It is synchronous and honours ctx: if ctx is cancelled the in-flight HTTP
// request to Pi-hole is aborted and CollectMetrics returns promptly, so a
// caller that has given up never leaves a goroutine behind.
func (c *Client) CollectMetrics(ctx context.Context) error {
	log.Debugf("Collecting from %s", c.config.PIHoleHostname)

	stats, blockedDomains, permittedDomains, clients, upstreams, piHoleStatus, err := c.getStatistics(ctx)
	if err != nil {
		return err
	}

	c.setMetrics(stats, blockedDomains, permittedDomains, clients, upstreams, piHoleStatus)
	log.Debugf("New tick of statistics from %s: %s", c.config.PIHoleHostname, stats)
	return nil
}

func (c *Client) GetHostname() string {
	return c.config.PIHoleHostname
}

func (c *Client) setMetrics(stats *StatsSummary, blockedDomains *TopDomains, permittedDomains *TopDomains, clients *[]PiHoleClient, upstreams *Upstreams, piHoleStatus *BlockingStatus) {
	metrics.DomainsBlocked.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Gravity.DomainsBeingBlocked))
	metrics.DNSQueriesToday.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Total))
	metrics.AdsBlockedToday.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Blocked))
	metrics.AdsPercentageToday.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.PercentBlocked))
	metrics.UniqueDomains.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.UniqueDomains))
	metrics.QueriesForwarded.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Forwarded))
	metrics.QueriesCached.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Cached))
	metrics.RequestRate.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Frequency))
	metrics.ClientsEverSeen.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Clients.Total))
	metrics.UniqueClients.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Clients.Active))
	metrics.DNSQueriesAllTypes.WithLabelValues(c.config.PIHoleHostname).Set(float64(stats.Queries.Total))
	if piHoleStatus.Blocking == "enabled" {
		metrics.Status.WithLabelValues(c.config.PIHoleHostname).Set(1)
	} else {
		metrics.Status.WithLabelValues(c.config.PIHoleHostname).Set(0)
	}

	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "unknown").Set(float64(stats.Queries.Replies.UNKNOWN))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "no_data").Set(float64(stats.Queries.Replies.NODATA))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "nx_domain").Set(float64(stats.Queries.Replies.NXDOMAIN))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "cname").Set(float64(stats.Queries.Replies.CNAME))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "ip").Set(float64(stats.Queries.Replies.IP))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "domain").Set(float64(stats.Queries.Replies.DOMAIN))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "rr_name").Set(float64(stats.Queries.Replies.RRNAME))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "serv_fail").Set(float64(stats.Queries.Replies.SERVFAIL))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "refused").Set(float64(stats.Queries.Replies.REFUSED))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "not_imp").Set(float64(stats.Queries.Replies.NOTIMP))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "other").Set(float64(stats.Queries.Replies.OTHER))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "dnssec").Set(float64(stats.Queries.Replies.DNSSEC))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "none").Set(float64(stats.Queries.Replies.NONE))
	metrics.Reply.WithLabelValues(c.config.PIHoleHostname, "blob").Set(float64(stats.Queries.Replies.BLOB))

	for _, domain := range permittedDomains.Domains {
		metrics.TopQueries.WithLabelValues(c.config.PIHoleHostname, domain.Domain).Set(float64(domain.Count))
	}

	for _, domain := range blockedDomains.Domains {
		metrics.TopAds.WithLabelValues(c.config.PIHoleHostname, domain.Domain).Set(float64(domain.Count))
	}

	for _, client := range *clients {
		metrics.TopSources.WithLabelValues(c.config.PIHoleHostname, client.IP, client.Name).Set(float64(client.Count))
	}

	for _, upstream := range upstreams.Upstreams {
		metrics.ForwardDestinations.WithLabelValues(c.config.PIHoleHostname, upstream.IP, upstream.Name).Set(float64(upstream.Count))
		metrics.ForwardDestinationsResponseTime.WithLabelValues(c.config.PIHoleHostname, upstream.IP, upstream.Name).Set(upstream.Statistics.Response)
		metrics.ForwardDestinationsResponseVariance.WithLabelValues(c.config.PIHoleHostname, upstream.IP, upstream.Name).Set(upstream.Statistics.Variance)
	}

	for queryType, value := range stats.Queries.Types {
		metrics.QueryTypes.WithLabelValues(c.config.PIHoleHostname, queryType).Set(value)
	}
}

func (c *Client) getStatistics(ctx context.Context) (*StatsSummary, *TopDomains, *TopDomains, *[]PiHoleClient, *Upstreams, *BlockingStatus, error) {
	var statsSummary StatsSummary
	var permittedDomains TopDomains
	var blockedDomains TopDomains
	var permittedClients TopClients
	var blockedClients TopClients
	var upstreams Upstreams
	var piHoleStatus BlockingStatus

	err := c.apiClient.FetchData(ctx, "/api/stats/summary", &statsSummary)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching stats summary: %w", err)
	}

	err = c.apiClient.FetchData(ctx, "/api/stats/top_domains?blocked=true&count=10", &blockedDomains)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching blocked domains: %w", err)
	}
	err = c.apiClient.FetchData(ctx, "/api/stats/top_domains?blocked=false&count=10", &permittedDomains)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching permitted domains: %w", err)
	}

	err = c.apiClient.FetchData(ctx, "/api/stats/top_clients?blocked=true&count=10", &blockedClients)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching blocked clients: %w", err)
	}
	err = c.apiClient.FetchData(ctx, "/api/stats/top_clients?blocked=false&count=10", &permittedClients)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching permitted clients: %w", err)
	}

	clients := MergeClients(permittedClients.Clients, blockedClients.Clients)

	err = c.apiClient.FetchData(ctx, "/api/stats/upstreams", &upstreams)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching upstream stats: %w", err)
	}

	err = c.apiClient.FetchData(ctx, "/api/dns/blocking", &piHoleStatus)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching status: %w", err)
	}

	return &statsSummary, &blockedDomains, &permittedDomains, &clients, &upstreams, &piHoleStatus, nil
}

// Close cleans up resources used by the client
func (c *Client) Close() {
	log.Debugf("Closing client %s", c.config.PIHoleHostname)
	c.apiClient.Close() // Close the API client
}
