# Prompt: fork and fix pihole-exporter

Paste everything below the line into a fresh Claude Code session opened in the
**fork's** working directory (`github.com/tomoconnor/pihole-exporter`), not in
the camnet repo. The camnet-side integration is the last section and is
deliberately small.

---

I have forked `github.com/eko/pihole-exporter` to
`github.com/tomoconnor/pihole-exporter` and I am adopting it. Upstream is
quasi-abandoned: issues describing the bug below are open and unactioned, and
several people have fixed things in their own forks that were never merged
back.

Your job is to make my fork the maintained one: fix the bug that made me fork
it, sweep the useful work out of other forks and open PRs, and leave the repo
in a state where it builds, tests, and releases a container image I can pull.

## The bug that caused the fork, with measurements

I run Pi-hole v6 and scrape `ekofr/pihole-exporter:v1.2.0`. It was failing 58%
of its scrapes (`avg_over_time(up[24h])` = 0.418) while burning ~29% of a CPU
core on Pi-hole's FTL process. Measured on the live system:

| Operation | Time |
|---|---|
| `POST /api/auth`, correct password, 3 runs | 3.90 s, 2.50 s, 1.51 s |
| `GET /api/stats/summary` reusing that session | **0.005 s** |
| `GET /metrics` on the exporter, 5 consecutive | 27.4 s, 30.0 s, 29.8 s, 29.7 s, >30 s |
| FTL CPU with the exporter running | 28.9% |
| FTL CPU with the exporter stopped | 2.0% |

Pi-hole is not slow. An already-authenticated query costs five milliseconds.
The whole cost is `/api/auth`, because Pi-hole v6 hashes the API password with
a deliberately expensive KDF on a single thread. That cost is intentional and
upstream Pi-hole will not be changing it, so the exporter must stop paying it
repeatedly.

**Ruled out already, do not re-investigate:** session exhaustion (6 of 16
sessions in use, the exporter held 2), Pi-hole being slow, the network path
(connect time ~0.3 ms), and the exporter being entirely broken (it does emit
118 samples — it works, it is just slow).

**Inferred but NOT verified — verify this in the code first:** ~30 s of wall
time divided by 1.5–4 s per auth suggests roughly ten authentications per
`/metrics` request, i.e. it probably re-authenticates once per API endpoint it
collects from. I derived that from timing alone and never read the source.
Confirm or refute it against the actual code before designing the fix; if the
real mechanism is different, fix the real one and tell me my inference was
wrong.

## What "fixed" means

A single authenticated session, cached in memory, reused across scrapes and
across every endpoint within a scrape, re-authenticating **only** on a 401 or
an explicit session expiry. Concurrent scrapes must not stampede into parallel
auths — one in-flight authentication at a time, others wait for it.

**Acceptance criterion, and it is a number, not a vibe:** `curl -s -o /dev/null
-w '%{time_total}\n' http://<exporter>:9617/metrics` must return **under 1
second** on a warm session. Do not report success on "it returns data now" — a
fix that leaves it at 8 s still fails a 10 s scrape timeout under load. Measure
it, five consecutive runs, and show me the numbers.

Add a regression test that fails if the number of `/api/auth` calls per
`/metrics` request exceeds one. A table-driven Go test against an `httptest`
server that counts auth hits is enough. This is the bug; it deserves a test
that catches its return.

## Sweeping the other forks

Enumerate the network of forks and the open PRs on upstream, then triage. Use
the `gh` CLI.

Rules for this, because "merge all the forks" is how a fork becomes worse than
what it replaced:

- Judge each change on its own. A fork being ahead of upstream is not evidence
  that its commits are good, and a fork that fixes the auth bug may also carry
  unrelated breakage.
- **Metric names and labels are a hard compatibility constraint.** I have a
  provisioned Grafana dashboard built on the v1.2.0 output. Any change that
  renames, relabels or removes a metric breaks it silently — the panels just go
  empty. If a fork's change is worth taking but alters the contract, take it
  and tell me explicitly what changed so I can update the dashboard in the same
  release. Do not quietly rename anything. The current surface is:

  ```
  pihole_ads_blocked_today                     pihole_queries_cached
  pihole_ads_percentage_today                  pihole_queries_forwarded
  pihole_clients_ever_seen                     pihole_querytypes
  pihole_dns_queries_all_types                 pihole_reply
  pihole_dns_queries_today                     pihole_request_rate
  pihole_domains_being_blocked                 pihole_status
  pihole_forward_destinations                  pihole_top_ads
  pihole_forward_destinations_responsetime     pihole_top_queries
  pihole_forward_destinations_responsevariance pihole_top_sources
  pihole_unique_clients                        pihole_unique_domains
  ```

- Prefer cherry-picking specific commits over merging whole branches. Whole
  branches drag in vendored dependency churn and rebases against an upstream
  that has since moved.
- Anything you cannot justify, leave out and list it as "considered and
  skipped, because —". I would rather have a short defensible changelog than a
  large one.

Give me the triage table **before** you start merging: fork/PR, what it claims
to fix, your read on whether it is correct, and take/skip. I will approve it.

## Adopting the repo

- Preserve upstream attribution and licence. This is a fork, not a rewrite.
- README: state plainly at the top that this is a maintained fork of
  `eko/pihole-exporter`, why it exists (link the auth issue), and what differs.
- CI that actually runs: build, `go vet`, `go test ./...`, and a container
  image build for **linux/amd64** (the target VPS is x86_64; multi-arch is
  optional and not needed for me).
- Publish the image to GHCR under my namespace and tag a release. Tell me the
  exact `image:` line to use.
- Open an issue upstream linking to the fix, so anyone hitting this finds it.
  Do not expect a response.

## Then, and only then, the camnet side

Separate repo, and small. In `~/camnet`:

1. `vps/pihole/docker-compose.yml` — set the new image, remove
   `profiles: ["disabled"]` from the `pihole-exporter` service.
2. `vps/observability/vm/scrape.yml` — uncomment the `pihole` job.
3. Deploy, then `curl -XPOST http://10.61.0.20:8428/-/reload`.
4. Verify `/metrics` under 1 s and `up{job="pihole"}` at 1.
5. Update the "Pi-hole has no native Prometheus endpoint" invariant in
   `CLAUDE.md` to say the exporter is now mine and why, and close out
   `docs/PIHOLE-EXPORTER.md`.

Steps 1 and 2 go together. Doing either alone either leaves metrics dark or
pages me continuously via `camnet-vps-target-down`.
