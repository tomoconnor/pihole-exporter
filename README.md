# Pi-hole Prometheus Exporter

[![CI](https://github.com/tomoconnor/pihole-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/tomoconnor/pihole-exporter/actions/workflows/ci.yml)
[![GoReportCard](https://goreportcard.com/badge/github.com/tomoconnor/pihole-exporter)](https://goreportcard.com/report/github.com/tomoconnor/pihole-exporter)

> ### This is a maintained fork of [`eko/pihole-exporter`](https://github.com/eko/pihole-exporter)
>
> Upstream has had no release since [v1.2.0](https://github.com/eko/pihole-exporter/releases/tag/v1.2.0)
> (July 2025). The bug described below is reported in
> [#318](https://github.com/eko/pihole-exporter/issues/318),
> [#327](https://github.com/eko/pihole-exporter/issues/327) and
> [#289](https://github.com/eko/pihole-exporter/issues/289), and a fix was
> offered in [#328](https://github.com/eko/pihole-exporter/pull/328) and
> again in [#330](https://github.com/eko/pihole-exporter/pull/330). None of
> them have been actioned.
>
> All credit for the original exporter goes to
> [Vincent Composieux](https://github.com/eko) and its contributors. This
> fork keeps the original MIT licence and copyright, and **does not rename,
> relabel or remove any metric** — it is a drop-in replacement for
> `ekofr/pihole-exporter:v1.2.0`.

## Why this fork exists

On Pi-hole v6 the upstream exporter degrades until `/metrics` stops
answering within a scrape timeout, and only a restart clears it. Two
independent faults cause it.

**1. One slow scrape wedged every scrape after it.** Each Pi-hole client
owned a single buffered channel shared by every `/metrics` request for the
life of the process. On the timeout path the handler started a *second*
reader of that channel, so two readers competed for one result. The loser
became a ghost reader that consumed the *next* request's result, and from
then on each scrape blocked until the one after it arrived — roughly one
scrape interval per request, forever. Measured against the pre-fix code:
a scrape given a 300 ms deadline took 5.0 s, and the scrape after it never
returned at all.

**2. The session was thrown away and re-bought constantly.** `POST /api/auth`
costs a real Pi-hole v6 **1.5–4 seconds** of single-threaded CPU, because the
API password is hashed with a deliberately expensive KDF; upstream Pi-hole
considers that cost intentional and will not change it. An
already-authenticated query costs about **5 ms**. The old client treated a
session validity of zero as "expired already", so when Pi-hole reported no
validity it authenticated once per API endpoint — seven times per scrape.
It also had no handling for a `401` at all, so once Pi-hole restarted or
evicted the session, every scrape failed until the exporter was restarted.

### What changed

| | Before | After |
|---|---|---|
| `/metrics`, warm session | 21.1 s cold, then stalls indefinitely | **3.06 s cold, 0.045–0.049 s warm** |
| Authentications per scrape | up to 7 | **0** (1 on first scrape, then reused) |
| Concurrent scrapes | each blocked for the full auth | collapse onto one authentication |
| Pi-hole restarts / session evicted | fails until exporter restart | one re-auth, scrape succeeds |
| A scrape that times out | wedges all later scrapes | no effect on later scrapes |

Measured end to end against a fake Pi-hole whose `/api/auth` sleeps 3 s, the
rest of it answering in 5 ms — i.e. the real cost profile. Both the hang and
the authentication count have regression tests
(`internal/server/server_test.go`, `internal/pihole/auth_test.go`) that fail
against the pre-fix code.

### Also swept in from unmerged upstream work

- `PIHOLE_PASSWORD_FILE`, for Docker/Kubernetes secrets — [#331](https://github.com/eko/pihole-exporter/pull/331) by [@gramtech](https://github.com/gramtech)
- CA certificates in the container image, so `PIHOLE_PROTOCOL=https` works — [#299](https://github.com/eko/pihole-exporter/pull/299) by [@mnlhfr](https://github.com/mnlhfr)
- Diagnosis of the `/metrics` hang — [#328](https://github.com/eko/pihole-exporter/pull/328) by [@nicjansma](https://github.com/nicjansma), rebased in [#330](https://github.com/eko/pihole-exporter/pull/330) by [@iSkrumpie](https://github.com/iSkrumpie)
- Dependency bumps [#319](https://github.com/eko/pihole-exporter/pull/319), [#304](https://github.com/eko/pihole-exporter/pull/304), [#301](https://github.com/eko/pihole-exporter/pull/301), [#320](https://github.com/eko/pihole-exporter/pull/320), [#313](https://github.com/eko/pihole-exporter/pull/313)

---

This is a Prometheus exporter for [Pi-hole](https://pi-hole.net/)'s Raspberry PI ad blocker.

![Grafana dashboard](https://raw.githubusercontent.com/eko/pihole-exporter/master/dashboard.jpg)

Available Grafana Dasboards:

- Prometheus: [Grafana Labs](https://grafana.com/grafana/dashboards/10176-pi-hole-exporter/) / [JSON/Github](https://raw.githubusercontent.com/eko/pihole-exporter/master/grafana/dashboard.json) --> [Preview](https://raw.githubusercontent.com/eko/pihole-exporter/master/dashboard.jpg)
- InfluxDB 2 (Flux): [Grafana Labs](https://grafana.com/grafana/dashboards/17094-pi-hole-exporter-influxdb-2/) / [JSON/Github](https://raw.githubusercontent.com/eko/pihole-exporter/master/grafana/dashboard-influxdb2.json) --> [Preview](https://raw.githubusercontent.com/eko/pihole-exporter/master/dashboard-influxdb2.png)

## Prerequisites

- [Go](https://golang.org/doc/)

## Installation

### Download binary

Binaries and a `SHA256SUMS.txt` are attached to each
[release](https://github.com/tomoconnor/pihole-exporter/releases/latest):

| OS | Architecture | File |
|---|---|---|
| Linux | amd64 | [`pihole_exporter-linux-amd64`](https://github.com/tomoconnor/pihole-exporter/releases/latest/download/pihole_exporter-linux-amd64) |
| Linux | arm64 | [`pihole_exporter-linux-arm64`](https://github.com/tomoconnor/pihole-exporter/releases/latest/download/pihole_exporter-linux-arm64) |
| Linux | arm (32-bit, ARMv6/7) | [`pihole_exporter-linux-arm`](https://github.com/tomoconnor/pihole-exporter/releases/latest/download/pihole_exporter-linux-arm) |
| macOS | amd64 | [`pihole_exporter-darwin-amd64`](https://github.com/tomoconnor/pihole-exporter/releases/latest/download/pihole_exporter-darwin-amd64) |
| macOS | arm64 | [`pihole_exporter-darwin-arm64`](https://github.com/tomoconnor/pihole-exporter/releases/latest/download/pihole_exporter-darwin-arm64) |

The `linux-arm` build is 32-bit ARM; `linux-arm64` is 64-bit. Upstream named
these ambiguously, which is [eko/pihole-exporter#321](https://github.com/eko/pihole-exporter/issues/321).
Windows and i386 builds are not published here — ask if you want them back.

Or with Go installed:

```bash
$ go install github.com/tomoconnor/pihole-exporter@latest
```

### Using Docker

The exporter is published to GHCR for **linux/amd64**:

```
ghcr.io/tomoconnor/pihole-exporter:latest
```

Pin a version rather than tracking `latest`. You can run it using the
following example and pass configuration environment variables:

```
$ docker run \
  -e 'PIHOLE_HOSTNAME=192.168.1.2' \
  -e 'PIHOLE_PASSWORD=mypassword' \
  -e 'PORT=9617' \
  -p 9617:9617 \
  ghcr.io/tomoconnor/pihole-exporter:latest
```

Or use PiHole's `WEBPASSWORD` as an API token instead of the password

```bash
$ API_TOKEN=$(awk -F= -v key="WEBPASSWORD" '$1==key {print $2}' /etc/pihole/setupVars.conf)
$ docker run \
  -e 'PIHOLE_HOSTNAME=192.168.1.2' \
  -e "PIHOLE_PASSWORD=$API_TOKEN" \
  -e 'PORT=9617' \
  -p 9617:9617 \
  ghcr.io/tomoconnor/pihole-exporter:latest
```

If you are running pi-hole behind https, you must both set the `PIHOLE_PROTOCOL` environment variable
as well as include your ssl certificates to the docker image as it does not have any baked in:

```
$ docker run \
  -e 'PIHOLE_PROTOCOL=https' \
  -e 'PIHOLE_HOSTNAME=192.168.1.2' \
  -e 'PIHOLE_PASSWORD=mypassword' \
  -e 'PORT=9617' \
  -v '/etc/ssl/certs:/etc/ssl/certs:ro' \
  -p 9617:9617 \
  ghcr.io/tomoconnor/pihole-exporter:latest
```

If you want to skip SSL certificate verification, pass in the following to either your Docker Compose file (-e) or the file holding environment variables (e.g. `.env`):

```text
SKIP_TLS_VERIFICATION=true
```

A single instance of pihole-exporter can monitor multiple pi-holes instances.
To do so, you can specify a list of hostnames, protocols, passwords/API tokens and ports by separating them with commas in their respective environment variable:

```
$ docker run \
  -e 'PIHOLE_PROTOCOL=http,http,http" \
  -e 'PIHOLE_HOSTNAME=192.168.1.2,192.168.1.3,192.168.1.4"' \
  -e "PIHOLE_PASSWORD=$API_TOKEN1,$API_TOKEN2,$API_TOKEN3" \
  -e "PIHOLE_PORT=8080,8081,8080" \
  -e 'PORT=9617' \
  -p 9617:9617 \
  ghcr.io/tomoconnor/pihole-exporter:latest
```

If port, protocol and API token/password is the same for all instances, you can specify them only once:

```
$ docker run \
  -e 'PIHOLE_PROTOCOL=http" \
  -e 'PIHOLE_HOSTNAME=192.168.1.2,192.168.1.3,192.168.1.4"' \
  -e "PIHOLE_PASSWORD=$API_TOKEN" \
  -e "PIHOLE_PORT=8080" \
  -e 'PORT=9617' \
  -p 9617:9617 \
  ghcr.io/tomoconnor/pihole-exporter:latest
```

Instead of putting the password/API token directly in an environment variable, you can point
`PIHOLE_PASSWORD_FILE` at a file (e.g. a Docker/Kubernetes secret) containing it. `PIHOLE_PASSWORD_FILE`
follows the same comma-separated rule as `PIHOLE_PASSWORD`: give it a single path to use for every host,
or one path per host. It cannot be combined with a non-empty `PIHOLE_PASSWORD`. Mount the secret read-only:

```yaml
services:
  pihole-exporter:
    image: ghcr.io/tomoconnor/pihole-exporter:latest
    environment:
      PIHOLE_HOSTNAME: 192.168.1.2
      PIHOLE_PASSWORD_FILE: /run/secrets/pihole_password
    volumes:
      - ./secrets/pihole_password.txt:/run/secrets/pihole_password:ro
    ports:
      - "9617:9617"
```

### From sources

Optionally, you can download and build it from the sources. You have to retrieve the project sources by using one of the following way:

```bash
$ go install github.com/eko/pihole-exporter@latest
# or
$ git clone https://github.com/eko/pihole-exporter.git
```

Install the needed vendors:

```
$ go mod vendor
```

Then, build the binary (here, an example to run on Raspberry PI ARM architecture):

```bash
$ GOOS=linux GOARCH=arm GOARM=7 go build -o pihole_exporter .
```

## Usage

In order to run the exporter, type the following command (arguments are optional):

Using a password

```bash
$ ./pihole_exporter -pihole_hostname 192.168.1.10 -pihole_password azerty
```

Or use PiHole's `WEBPASSWORD` as an API token instead of the password

```bash
$ API_TOKEN=$(awk -F= -v key="WEBPASSWORD" '$1==key {print $2}' /etc/pihole/setupVars.conf)
$ ./pihole_exporter -pihole_hostname 192.168.1.10 -pihole_password $API_TOKEN
```

#### Debug logging

You can enable verbose output either by environment variable or CLI flag:

Both options set logrus to `debug` level (shown below); otherwise the exporter logs at `info`.

___

```bash
2019/05/09 20:19:52 ------------------------------------
2019/05/09 20:19:52 -  Pi-hole exporter configuration  -
2019/05/09 20:19:52 ------------------------------------
2019/05/09 20:19:52 PIHoleHostname : 192.168.1.10
2019/05/09 20:19:52 PIHolePassword : azerty
2019/05/09 20:19:52 Port : 9617
2019/05/09 20:19:52 Timeout : 5s
2019/05/09 20:19:52 ------------------------------------
2019/05/09 20:19:52 New Prometheus metric registered: domains_blocked
2019/05/09 20:19:52 New Prometheus metric registered: dns_queries_today
2019/05/09 20:19:52 New Prometheus metric registered: ads_blocked_today
2019/05/09 20:19:52 New Prometheus metric registered: ads_percentag_today
2019/05/09 20:19:52 New Prometheus metric registered: unique_domains
2019/05/09 20:19:52 New Prometheus metric registered: queries_forwarded
2019/05/09 20:19:52 New Prometheus metric registered: queries_cached
2019/05/09 20:19:52 New Prometheus metric registered: clients_ever_seen
2019/05/09 20:19:52 New Prometheus metric registered: unique_clients
2019/05/09 20:19:52 New Prometheus metric registered: dns_queries_all_types
2019/05/09 20:19:52 New Prometheus metric registered: reply
2019/05/09 20:19:52 New Prometheus metric registered: top_queries
2019/05/09 20:19:52 New Prometheus metric registered: top_ads
2019/05/09 20:19:52 New Prometheus metric registered: top_sources
2019/05/09 20:19:52 New Prometheus metric registered: forward_destinations
2019/05/09 20:19:52 New Prometheus metric registered: querytypes
2019/05/09 20:19:52 New Prometheus metric registered: status
2019/05/09 20:19:52 New Prometheus metric registered: queries_last_10min
2019/05/09 20:19:52 New Prometheus metric registered: ads_last_10min
2019/05/09 20:19:52 Starting HTTP server
2019/05/09 20:19:54 New tick of statistics: 648 ads blocked / 66796 total DNS queries
...
```

Once the exporter is running, you also have to update your `prometheus.yml` configuration to let it scrape the exporter:

```yaml
scrape_configs:
    - job_name: "pihole"
      static_configs:
          - targets: ["localhost:9617"]
```

## Available CLI options

```bash
# Hostname of the host(s) where Pi-hole is installed
  -pihole_hostname string (optional) (default "127.0.0.1")

# Password defined on the Pi-hole interface
  -pihole_password string (optional)

# Timeout to connect and retrieve data from a Pi-hole instance
  -timeout duration (optional) (default 5s)

# WEBPASSWORD / api token defined on the Pi-hole interface at `/etc/pihole/setupVars.conf`
  -pihole_password string (optional)

# Address to be used for the exporter
  -bind_addr string (optional) (default "0.0.0.0")

# URL Context (first segments of URL path) to the PI-hole admin application
  -pihole_admin_context string (optional) (default "admin")

# Port to be used for the exporter
  -port string (optional) (default "9617")

# Disabling TLS verification
  disabling TLS verification accepts any certificate 
    and skips hostname checks - 
    do NOT use on untrusted networks!!

  -skip_tls_verification true

# Enable debug (verbose) output
  -debug
```





## Available Prometheus metrics

All 20 metrics below are unchanged from upstream v1.2.0, in both name and
labels. Every metric carries a `hostname` label; some carry more.

| Metric name | Extra labels | Description |
| --- | --- | --- |
| `pihole_domains_being_blocked` | | Number of domains being blocked |
| `pihole_dns_queries_today` | | Number of DNS queries made over the current day |
| `pihole_ads_blocked_today` | | Number of ads blocked over the current day |
| `pihole_ads_percentage_today` | | Percentage of ads blocked over the current day |
| `pihole_unique_domains` | | Number of unique domains seen |
| `pihole_queries_forwarded` | | Number of queries forwarded |
| `pihole_queries_cached` | | Number of queries cached |
| `pihole_clients_ever_seen` | | Number of clients ever seen |
| `pihole_unique_clients` | | Number of unique clients seen |
| `pihole_dns_queries_all_types` | | Number of DNS queries made for all types |
| `pihole_reply` | `type` | Number of replies made, by reply type |
| `pihole_top_queries` | `domain` | Top permitted queries, by domain |
| `pihole_top_ads` | `domain` | Top blocked queries, by domain |
| `pihole_top_sources` | `source`, `source_name` | Top requesting clients |
| `pihole_forward_destinations` | `destination`, `destination_name` | Queries by upstream destination |
| `pihole_forward_destinations_responsetime` | `destination`, `destination_name` | Mean response time per upstream |
| `pihole_forward_destinations_responsevariance` | `destination`, `destination_name` | Response time variance per upstream |
| `pihole_querytypes` | `type` | Queries by DNS query type |
| `pihole_request_rate` | | Query frequency reported by Pi-hole |
| `pihole_status` | | 1 if Pi-hole blocking is enabled, 0 otherwise |

Two metrics that earlier READMEs listed, `queries_last_10min` and
`ads_last_10min`, have not been emitted since the Pi-hole v6 API rewrite.
They are removed from this table rather than from the code — there is
nothing in the code to remove. See
[eko/pihole-exporter#314](https://github.com/eko/pihole-exporter/issues/314).

## Pihole-Exporter Helm Chart

[Link](https://github.com/SiM22/pihole-exporter-helm-chart)

This is a simple Helm Chart to deploy the exporter in a kubernetes cluster.
