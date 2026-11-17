# mikan-sub

A subscription edge for [#281](https://github.com/Miroshka000/mikan/issues/281).
Run it on a separate server or behind a CDN. It needs no database, administrator
password or panel TLS keys. The relay URL uses the panel’s secret admin path; the
subscription key still cannot access the administrative API. Multiple instances can share one
subscription domain; each can have its own revocable subscription API key.

## Panel setup

1. Build/update the panel with this change (migration 0080 adds the key scope).
2. In **API → New key**, select **Subscriptions**. Store the key securely: it is
   shown once. Ordinary read/full API keys are deliberately refused by the edge API.
3. In subscription settings, enable **External mikan-sub** and set `sub.example.com`.
   This keeps the existing subscription path and puts new links on HTTPS port 443.
   DNS need not point to the panel. The panel no longer orders this domain's certificate.
   Existing links to the panel continue to work. Links already handed out with a different
   port on this subscription domain need that port forwarded on the edge, or replacement
   links. Payment callback URLs stay on the panel’s own subscription listener.

The equivalent session-authenticated settings patch is:

```json
{"sub_domain":"sub.example.com","sub_external":true}
```

## Run on another server

Build with Go 1.27 (or use `/usr/local/bin/mikan-sub` from the project's Docker image):

```sh
go build -o mikan-sub ./cmd/mikan-sub
# Load MIKAN_SUB_TOKEN from a protected environment file or secret manager.
./mikan-sub -upstream 'https://panel.example.com:2053/<admin-path>/api/subscription' \
  -path YOUR_EXISTING_SUB_PATH -domain sub.example.com \
  -email admin@example.com -data /var/lib/mikan-sub/acme
```

Point the domain's A/AAAA records at the edge. Allow inbound TCP 80 and 443 and
outbound HTTPS to the panel and Let's Encrypt. The panel's HTTPS certificate must
be trusted by the edge's OS; for a private CA, install that CA in its trust store.
Keep the ACME directory persistent and writable only by the service account.
Run under a supervisor with a dedicated account; grant binding to low ports or
forward 80/443 to the service. SIGTERM gracefully shuts down both listeners.

The edge stores only its restricted API token, public assets and saved profiles in memory, and its
own ACME account/certificates on disk. The relay lives below the secret admin path on the panel listener only.
Keep the full upstream URL private. With no subscription keys, or without a valid
key, the route uses the same 404/OwnSite response as unknown paths.
Revoke an edge's key in the panel to disconnect it. Do not share ACME directories
between concurrently running instances: terminate TLS centrally or provision each
instance separately. Moving the edge requires changing DNS, not subscription links.

## Small standalone container

This image builds only the edge: no panel, web build, PostgreSQL client, or database.
Build from the repository root and provide the token via a protected environment file:

```sh
docker build -f cmd/mikan-sub/Dockerfile -t mikan-sub .
docker run -d --name mikan-sub --restart unless-stopped \
  --env-file /etc/mikan-sub.env -p 80:80 -p 443:443 \
  -v mikan-sub-acme:/data/acme mikan-sub \
  -upstream 'https://panel.example.com:2053/<admin-path>/api/subscription' \
  -path YOUR_EXISTING_SUB_PATH -domain sub.example.com -email admin@example.com
```

The environment file contains `MIKAN_SUB_TOKEN=...` and should be readable only by
its owner. The container runs as UID 65532; keep `NET_BIND_SERVICE` available for
its low ports. Use a separate ACME volume for each independent instance.

## Behind a CDN or reverse proxy

```sh
./mikan-sub -http -listen 127.0.0.1:8080 \
  -upstream 'https://panel.example.com:2053/<admin-path>/api/subscription' \
  -path YOUR_EXISTING_SUB_PATH
```

Terminate public HTTPS for `sub.example.com` on the CDN/proxy and forward the
subscription path to this listener. Bind to a private interface when the proxy is
on another host and restrict origin access to that proxy. Disable CDN caching for
personalized pages/profiles, `/info`, and POST requests. By default, device IPs record the direct peer. To trust your reverse proxy, set
`-trusted-proxies 10.0.0.0/24 -client-ip-header X-Forwarded-For`, or select
`CF-Connecting-IP` and supply Cloudflare’s current CIDRs. No ranges are trusted by
default; restrict the origin to your proxies. For X-Forwarded-For the edge walks
right to left past trusted hops; untrusted peers cannot spoof this address.
Custom rule headers pass through, except credentials, cookies, forwarding metadata,
`X-Mikan-*` (except the client’s `X-Mikan-Features`) and hop-by-hop headers.

## Behavior and limits

- The existing panel renders formats, branding, HWID-bound profiles and subscriber
  actions. Telegram Mini App requests retain Telegram's signature and origin checks.
- Only known subscription paths/methods are forwarded. Payment provider webhooks
  remain on the panel. Legacy imported subscription paths are not served by the edge;
  existing legacy links continue to use the panel.
- Every profile request first goes to the panel. If the panel is unreachable, returns
  502/503/504, or sends an incomplete response, the edge can return a previously successful
  GET profile for the exact same subscription, query, forwarded headers, host and client
  IP. Browser pages, subscriber information and POST actions are not served offline.
- Saved profiles expire one hour after the last successful fetch, or at the subscription's
  declared expiry, whichever comes first. Configure `-offline-ttl 15m`, for example;
  `-offline-ttl 0` disables this fallback. Offline responses include `X-Mikan-Offline: true`
  and `Cache-Control: private, no-store`. Serving a saved copy does not extend its lifetime.
- Explicit 4xx refusals never use a saved copy and invalidate saved variants of that
  subscription. Only a marked rejection of the relay credential clears all saved profiles;
  ordinary Mini App 401s and device/rule 403s affect no other subscription. Intentional
  response-rule drops close the client connection and never trigger offline delivery.
  Successful subscriber mutations clear affected profiles; authenticated Mini App mutations
  name the account’s subscriptions to invalidate. Read-only Mini App POSTs
  (session, shop, promo history) keep them. A response already
  in flight cannot restore a profile after such invalidation.
- During an outage the edge cannot learn about new blocks, link changes, exhausted
  quotas or revoked device access. The offline TTL bounds this delay. Unknown devices,
  uncached request variants and expired copies still receive 502 when the panel is down.
- Profile storage is in memory only: at most 256 entries, 64 MiB total and 1 MiB per
  profile by default. Configure `-cache-entries 2000 -cache-bytes 268435456` for
  a larger fleet. Variants are evicted in least-recently-used order. Restarting clears it. Public assets use a separate five-minute cache
  (128 entries, 1 MiB each). No subscriber profiles are written to disk.
- The Telegram addon refreshes its menu address within 30 seconds, including port-only
  and external-mode changes; panel outages preserve the last successful menu.
- The dedicated token cannot list users or call the normal panel API. Subscriber
  tokens and Telegram authentication still govern subscriber actions.
- Requests are limited to 1 MiB before forwarding; responses to 8 MiB. At most 16
  upstream requests run concurrently; overload returns 503 with `Retry-After: 1`. Upstream requests time out after
  30 seconds. Redirects are refused so the credential cannot follow another host.

## Fast offline mode and monitoring

After three consecutive failed profile requests, the circuit breaker stops contacting
an unavailable panel for ten seconds and immediately serves matching saved profiles.
After that delay, exactly one request probes recovery; other requests keep using the
cache. A successful/authoritative response closes the circuit; another failure opens
it again. Explicit 4xx responses still invalidate profiles and are not masked by cache.
Subscriber POST actions continue reaching the panel and never execute from cache.
Configure `-failure-threshold 3 -retry-delay 10s`; threshold `0` disables the breaker.
The initial failed requests can still take the normal upstream timeout.

Metrics are opt-in, on a separate loopback-only listener:

```sh
./mikan-sub ... -metrics-listen 127.0.0.1:9091
curl http://127.0.0.1:9091/metrics
```

The Prometheus counters cover requests, upstream attempts/failures, offline responses,
circuit skips and overload rejections. Gauges report valid cached profiles, their bytes,
the oldest valid profile age and circuit state. No subscriber tokens, HWID, IP or URL labels are exported. `/metrics`
is never installed on the public listener. In containers, scrape inside the network
namespace (for example with an agent sharing that namespace); publishing a port does
not expose a loopback-bound metrics server.

## Integration checks

```sh
docker build -f cmd/mikan-sub/Dockerfile -t mikan-sub:ci .
python3 test/subrelay/run.py --image mikan-sub:ci
```

The dedicated CI job runs the full standalone Docker build, then real ACME HTTP-01
orders against a digest-pinned local Pebble CA, using a trusted temporary TLS fixture.
It verifies two edges for the same subscription domain, persisted certificates after
restart, both nodes' offline delivery and recovery, and private metrics. To issue each
node's certificate, the test deliberately moves the domain's Docker DNS alias to that
node. Production deployments likewise need centralized TLS/challenge routing or staged
certificate provisioning; randomly routing HTTP-01 challenges across independent
instances does not work. Each edge keeps its own certificate volume and memory cache.

`-acme-directory https://.../dir` selects a staging/private CA; its HTTPS certificate
must be trusted by the OS. Empty uses Let's Encrypt. The test never disables TLS
verification and never sends an order to a public CA.

The ACME test makes TLS-ALPN unreachable, so success requires HTTP-01. Its
`--break-http-01` negative control also breaks HTTP-01 and requires issuance to fail.
