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
    image: ghcr.io/sybenx/castle-steward:v0.4.0
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
reaches it via `docker exec`, not the network). Then point a reverse proxy
at it — see `deploy/Caddyfile` and `deploy/nginx.conf` for both: route
WebSocket upgrades and `Accept: application/nostr+json` to strfry,
everything else (towncrier + `/api`) to steward, and forward a real client
IP so steward's per-IP rate limit sees actual clients rather than the
proxy.

`docker compose up -d steward` and open the relay's URL in a browser.

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
