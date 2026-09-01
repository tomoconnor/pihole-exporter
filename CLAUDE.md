# pihole-exporter

Prometheus exporter for Pi-hole v6. This is a **maintained fork** of
[`eko/pihole-exporter`](https://github.com/eko/pihole-exporter), which has had
no release since v1.2.0 (July 2025) and does not merge fixes.

We publish the image, so a broken release here is ours to fix. It is consumed
by `~/camnet` (`vps/pihole/docker-compose.yml`), which pins the tag.

- Module: `github.com/tomoconnor/pihole-exporter`
- Image: `ghcr.io/tomoconnor/pihole-exporter` (linux/amd64, `scratch`, rootless)
- Upstream write-up: [eko/pihole-exporter#332](https://github.com/eko/pihole-exporter/issues/332)

## Invariants — breaking these breaks the consumer

**The metric contract is frozen.** Grafana in `~/camnet` is provisioned against
the 20 `pihole_*` names and their labels as v1.2.0 emitted them. Renaming,
relabelling or removing any of them makes the panels go **empty silently** —
no error, no alert, just blank. Adding new metrics is fine; changing existing
ones is not, unless the dashboard changes in the same release and the release
notes say so explicitly.

The authoritative list is `internal/metrics/metrics.go`. The README table is
checked against it; if you touch either, re-check both.

**Authentication is expensive and must stay rare.** `POST /api/auth` costs a
real Pi-hole v6 **1.5–4 s** of single-threaded CPU, because the API password is
hashed with a deliberately expensive KDF. Upstream Pi-hole considers that cost
intentional and will not change it. An already-authenticated query costs about
**5 ms** — a ratio of several hundred to one.

So: one session, cached, reused across every endpoint in a scrape and across
scrapes, renewed only on a 401 or genuine expiry, with one authentication in
flight at a time. Any change that authenticates more often than that is a
regression even if the tests happen to pass. `TestAuthenticationsPerScrape` is
the guard.

**Never hold per-request state on a `pihole.Client`.** Clients are shared
across every concurrent `/metrics` request for the life of the process. The
bug this fork exists for was exactly this: a status channel hanging off the
client, which let one request consume another's result.

## The two bugs this fork fixes

Worth knowing before changing `internal/server` or `internal/pihole`, because
both are easy to reintroduce.

**1. The `/metrics` hang** (`internal/server/server.go`). Upstream gave each
client a single `chan *ClientChannel` buffered to 1, shared across all
requests. On the collection-timeout path the handler started a *second* reader
of it, so two readers competed for one write. The loser became a ghost reader
that consumed the *next* request's result, and from then on every scrape
blocked until the one after it arrived — one scrape interval per request,
until restart. Symptom in the wild: `/metrics` response times that match the
Prometheus `scrape_interval` to three significant figures.

Fixed by removing the shared channel entirely. The handler owns a per-request
channel buffered for every client, so an abandoned collector can always send
and exit rather than leaking.

**2. Re-authentication per endpoint** (`internal/pihole/api_client.go`).
Upstream set the session expiry to `time.Now().Add(validity)`. When Pi-hole
reports `session.validity` as `0` or absent, that is *now*, so the next call
sees an expired session — seven authentications per scrape. There was also no
`401` handling at all, so once Pi-hole restarted or evicted the session, every
scrape failed until the exporter was restarted.

Fixed with a fallback to Pi-hole's own 300 s default, a refresh margin, one
retry on 401, and an explicit single-flight.

## Layout

```
main.go                        wiring only
config/                        env/flag parsing, multi-host splitting
internal/metrics/metrics.go    every Prometheus metric — the frozen contract
internal/pihole/api_client.go  HTTP, session cache, single-flight auth
internal/pihole/client.go      one scrape: 7 endpoints -> gauges
internal/pihole/model.go       Pi-hole v6 JSON shapes
internal/server/server.go      /metrics, /readiness, /liveness
internal/piholetest/mock.go    fake Pi-hole v6 used by both test packages
```

`internal/piholetest` is a real package, not `_test.go`, so both
`internal/pihole` and `internal/server` can use it. It counts authentications,
can expire a session to force a 401, and can block endpoints to simulate a
wedged Pi-hole.

## Working on it

```bash
go build ./... && go vet ./... && gofmt -l .
go test -race -cover ./...          # -race matters: this is a concurrency fix
docker buildx build --platform linux/amd64 -t pihole-exporter:local --load .
```

**Always `-race`.** Both fixed bugs are concurrency bugs and the tests spawn 25
concurrent collectors on purpose.

**Verify a regression test actually fails against the old code** before
believing it. Two of the four auth tests pass on pre-fix code as well —
upstream held the mutex across the network call, which serialised the check by
accident. The way to check:

```bash
git worktree add /tmp/oldcode <pre-fix-sha>
cp internal/pihole/auth_test.go internal/piholetest/mock.go /tmp/oldcode/...
cd /tmp/oldcode && go test ./internal/pihole/
```

### End-to-end timing

Unit tests will not tell you whether a scrape is fast enough. Run the real
binary against a fake Pi-hole whose `/api/auth` sleeps 3 s and whose other
endpoints answer in 5 ms — the real cost profile — then measure:

```bash
curl -s -o /dev/null -w '%{time_total}\n' http://localhost:9617/metrics
```

Warm scrapes should be well under a second. **Measure five in a row, not one**,
and ignore the first: it legitimately pays for the single authentication.

## Releasing

Tag `vX.Y.Z` on `master`. `.github/workflows/release.yml` publishes
`ghcr.io/tomoconnor/pihole-exporter:X.Y.Z` (plus `X.Y` and `latest`) for
linux/amd64, and binaries with `SHA256SUMS.txt`.

**The image tag has no `v`.** `docker/metadata-action`'s `{{version}}` strips
it, so tag `v1.3.1` publishes image `1.3.1`. Referencing `:v1.3.1` gives
"manifest unknown".

Then bump the tag in `~/camnet/vps/pihole/docker-compose.yml`, `scp` it to
`/opt/pihole/`, and `docker compose up -d`. Never point camnet at `:latest`.

## Taking changes from upstream or other forks

Upstream has ~126 forks; only a handful carry any unique commit, and most of
that is README or dependabot noise. The real work is in open PRs.

- Judge each change on its own. Being ahead of upstream is not evidence of
  being right, and a fork that fixes one thing often breaks another.
- Prefer cherry-picking specific commits over merging branches, which drag in
  vendored churn and rebases against a moved upstream.
- **Preserve authorship** on cherry-picks, and credit the contributor in the
  merge commit and the README.
- Check the change against the metric contract before anything else.

Deliberately skipped so far, so they do not get re-proposed: upstream #316
(confita 0.11 — rewrites the config loader for no benefit), #317 and
RBozydar's fork (both add a query-log fetch to every scrape, i.e. latency on
the exact path this fork exists to fix), #329 (bash secret shim — the runtime
image is `scratch` and has no shell), #296 (Alloy snippet with a wrong port),
#223 (predates the v6 API rewrite).

## Attribution

Fork of work by [Vincent Composieux](https://github.com/eko) and contributors.
`LICENSE` and its copyright line are untouched and must stay that way. Comments
referencing `eko/pihole-exporter#NNN` point at upstream issues on purpose — do
not rewrite those to this repo.
