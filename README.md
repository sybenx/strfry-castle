# The Castle

A sidecar for an existing [strfry](https://github.com/hoytech/strfry) relay
that turns an open relay into a castle: permanent storage for the owner
(**the Lord**) and everyone he elevates — an invite tree of trusted members
(Pyramid-style, like fiatjaf's
[Pyramid](https://github.com/fiatjaf/pyramid) relay), his follows, and his
favorites and wards — plus open lands (**the Outer Lands**) where anyone
may write, raided at the Lord's whim.

Two pieces, one container: **`steward`**, a single Go binary that runs the
invite tree, retention, and a small signed HTTP API, and also serves
**`towncrier`**, one static `index.html` status page. Nothing modifies
strfry itself — no config changes, no plugin volume, no files placed
inside the strfry container. strfry's `writePolicy` plugin slot stays free
for whatever spam filter you want to run there; the castle only acts
*after* storage, at raid time.

## Install

```
curl -fsSL https://raw.githubusercontent.com/sybenx/castle-for-strfry-experiment/main/install.sh | bash
```

This detects your architecture and running strfry container (asking you to
confirm), downloads and checksum-verifies the matching steward release,
creates the `castle-state` docker volume, and writes an `.env` next to
where you ran it. It **prints** — and never applies — the compose service
snippet and reverse-proxy config you need to add by hand; see
[Manual install](#manual-install) for exactly what that snippet looks
like. Re-running it is safe: the docker volume create is a no-op if the
volume exists, and an `.env` that already has `OWNER_PUBKEY` set is left
alone.

First deploy comes up with `RAID_DRY_RUN=true` and `RAID_CRON` empty:
nothing is ever deleted and no raid runs on a schedule until you review a
dry-run raid's log yourself and arm it (see below).

To remove it:

```
curl -fsSL https://raw.githubusercontent.com/sybenx/castle-for-strfry-experiment/main/uninstall.sh | bash
```

which prints what to remove from your compose file, drops the locally
pulled image, and — deliberately — leaves the `castle-state` volume alone
(it holds `ledger.jsonl`, the only durable record of your invite tree and
elevations; strfry itself has no protocol-level memory of admin actions).

## Manual install

Add a `steward` service to your existing strfry compose stack (check the
[releases page](https://github.com/sybenx/castle-for-strfry-experiment/releases)
for a newer tag than the one below):

```yaml
services:
  steward:
    image: ghcr.io/sybenx/castle-steward:v0.4.1
    depends_on:
      - strfry               # your strfry service/container name
    env_file: .env
    volumes:
      - castle-state:/state
      - /var/run/docker.sock:/var/run/docker.sock
    ports:
      - "8787:8787"

volumes:
  castle-state:
    name: castle-state
```

Copy `.env.example` to `.env` and fill in `OWNER_PUBKEY` (hex — the Lord's
pubkey) and `STRFRY_CONTAINER` (the name of your strfry container; steward
reaches it via `docker exec`, not the network). Then set up your reverse
proxy — see [Reverse proxy setup](#reverse-proxy-setup) below.

`docker compose up -d steward` and open the relay's URL in a browser.

## Reverse proxy setup

**The model in one line:** same-origin (one hostname, split by header) is
the default wherever your proxy can route on headers; `RELAY_URL`
(split-domain, two hostnames) is for wherever it can't — see
[Split-domain deployment](#split-domain-deployment) below.

Before castle, your reverse proxy almost certainly sent every request for
the relay's domain straight to `strfry:7777` — there was nothing else to
route to. Adding castle means splitting that one route into two:
WebSocket upgrades and NIP-11 requests (`Accept: application/nostr+json`)
still go to strfry; everything else (towncrier, `/api`) goes to steward.
`deploy/Caddyfile` and `deploy/nginx.conf` show that split, but they're
written as standalone server blocks — in practice you're almost always
merging this routing into a config that already exists for your domain,
not dropping in a new file. Steps:

1. Find the existing block for your relay's domain: the site block in
   your `Caddyfile`, or the nginx `server { }` in `sites-enabled/` (or
   wherever your distro/panel keeps it).
2. Replace whatever currently forwards *all* traffic to strfry with the
   matcher/location logic from `deploy/Caddyfile` or `deploy/nginx.conf`:
   the WebSocket and NIP-11 matchers keep pointing at strfry, and the
   catch-all now points at `steward:8787` instead of `strfry:7777`. Leave
   every other directive in the block alone — TLS, ACME challenge
   locations, existing headers — you're only changing the catch-all's
   destination.
3. Order matters. In nginx, the `if` blocks in `deploy/nginx.conf` each
   end in `break` so they short-circuit before falling through to the
   `steward` `proxy_pass` — keep them ahead of any other `location /` you
   already have. In Caddy, `handle` blocks are evaluated top-to-bottom and
   the first match wins; same idea.
4. Forward the real client IP (`X-Real-IP` / `X-Forwarded-For`, as in the
   examples) so steward's per-IP API rate limit sees actual clients
   instead of the proxy's address.
5. If strfry and steward aren't on the same docker network as your proxy,
   the `strfry:7777` / `steward:8787` hostnames won't resolve — use the
   real container names, or put all three on a shared network.
6. Reload the proxy (`caddy reload`, `nginx -s reload`, or your panel's
   equivalent) and check all three routes land correctly:
   - `curl -sI https://your-domain/` → steward (towncrier HTML)
   - `curl -s -H 'Accept: application/nostr+json' https://your-domain/` →
     strfry's NIP-11 document
   - `nak req -k 1 wss://your-domain` (or any WebSocket client) → strfry

If your strfry install is managed by a platform with its own reverse
proxy (Umbrel, a hosting panel, etc.) rather than a Caddyfile/nginx.conf
you edit by hand, `install.sh` deliberately doesn't touch it for you —
you'll need that platform's own way of adding a second backend on the
same domain. Auto-patching an unknown managed config is exactly the kind
of thing that bricks a relay (see `DECISIONS.md`).

Running raw Traefik and can drop into its dynamic file provider directly?
`deploy/traefik-pangolin.yml` has the same same-hostname split expressed as
Traefik routers with priority (same idea as nginx's `break` / Caddy's
top-to-bottom `handle` ordering above).

## Split-domain deployment

Some reverse proxies can only route by **hostname**, not by header —
Pangolin's UI, Cloudflare Tunnel, Umbrel, and most hosting panels fall into
this bucket. They have no way to say "WebSocket upgrades and
`Accept: application/nostr+json` go to strfry, everything else on this
same hostname goes to steward" — the header split above is unavailable to
them entirely.

For these, give strfry and steward **separate hostnames** instead of
sharing one, and tell steward the relay's real address so towncrier can
still find it:

1. Point a second hostname at steward — e.g. `castle.example.com` →
   `steward:8787` — the same way you already point your relay's hostname
   at `strfry:7777`. No header logic needed; it's a plain one-to-one
   hostname-to-backend mapping, which is exactly what hostname-only
   proxies are good at.
2. Set `RELAY_URL` in steward's environment to the relay's own `wss://`
   address (the hostname strfry is reachable on), e.g.
   `RELAY_URL=wss://relay.example.com`. towncrier fetches this from
   `GET /api/config` at load and uses it everywhere it needs the relay's
   address directly (the displayed relay URL, the NIP-11 fetch); leaving
   `RELAY_URL` unset is same-origin mode, unchanged from before this
   existed.
3. See `deploy/traefik-pangolin.yml` for the concrete Pangolin case,
   including where the two hostnames go in its UI.

**The inherent limitation:** in split-domain mode, a browser opening the
*relay's own* URL (`relay.example.com`) gets strfry's stock landing page,
not towncrier — towncrier only lives at the second hostname. This is
exactly why same-origin (one hostname, "open the relay URL, see the
castle") remains the default and the recommended mode whenever your proxy
can support it; split-domain is the fallback for when it can't. If this
bothers you, you can point strfry's own landing page at a redirect to
towncrier's hostname — but that means editing strfry's config, which is
outside what Castle does for you (see "Nothing modifies strfry itself,"
above).

## The docker.sock trade-off

steward mounts `/var/run/docker.sock` so it can run `docker exec` against
your strfry container (there's no other way to drive the `strfry` CLI for
scans and deletes). **This is root-equivalent access to the host, granted
to a container that also serves an internet-facing HTTP API.** It's
accepted here for one-DB-owner simplicity — a self-hosted relay with one
operator — and disclosed up front rather than buried. If that trade
doesn't work for your setup, put a socket proxy in front of it instead,
e.g. [tecnativa/docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy)
scoped to `exec` only.

## Spam filtering

The castle never gates writes — every event strfry accepts is kept until
the next raid decides what survives (citizens, the grace window, and the
TTL cutoff; see the retention model below). If you want write-path
filtering (rate limits, PoW, banned-word lists, whatever), plug any stock
strfry `writePolicy` plugin into strfry's own plugin slot; the castle has
no opinion about it and no involvement.

## Retention, in short

At raid time, an event survives if its author is a **citizen** (the
Lord, invite-tree members, current follows, or anyone elevated —
favorited or warded), or was evicted less than `OUTER_TTL_DAYS` ago (the
grace window), or is younger than the TTL cutoff. Everything else is
deleted. Losing citizenship is never an instant purge — the grace window
always applies, even on a raid run with a shorter one-off TTL override.
DMs (NIP-59 gift wraps) aren't protected by any of this: they're signed by
random one-time keys, so the relay can't tell the Lord's own DMs from a
stranger's, and they age out at every raid like anything else. See
`CLAUDE.md` for the full spec.

## towncrier

`towncrier/index.html` is the whole frontend: one file, vanilla JS, no
build step, under 60KB. It shows the Lord, the invite tree ("the Court"),
favorites, citizen/event counts, the evicted list, and Outer Lands stats
to anyone. Signing in with a NIP-07 browser extension unlocks invite/
remove for tree members, and for the Lord: ennoble, elevate/lower
(favorites and wards), the raid control, and the Wards view — which is
rendered only for the Lord and fed only by a Lord-signed
`GET /api/wards`, so ward data never reaches a public response.

*(screenshot pending — run `make smoke` or a real deploy and open the
relay's URL to see it live)*

## Development

```
make build      # static steward for linux/amd64 + linux/arm64
make test       # unit + property tests
make smoke      # scratch strfry + fixture events via nak, exercised end to end
make bytecheck  # fails if towncrier/index.html exceeds 60KB
```

`steward/` is the sidecar (Go, `github.com/nbd-wtf/go-nostr`);
`towncrier/index.html` is the page; `deploy/` holds the compose fragment
and reverse-proxy examples. `CLAUDE.md` is the spec and source of truth,
`PLAN.md` the build order, `DECISIONS.md` the record of what was cut or
decided along the way and why.

## Releases

Pushing a `vX.Y.Z` tag builds static `steward` binaries for linux/amd64
and linux/arm64, attaches them to a GitHub Release with `checksums.txt`,
and pushes a multi-arch image to `ghcr.io/sybenx/castle-steward`. See
`.github/workflows/release.yml`.
