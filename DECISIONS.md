# DECISIONS.md — the graveyard and the waiting room

Decisions made during design, recorded so they are not relitigated by
accident. Claude Code: append here whenever you make a call CLAUDE.md
doesn't cover. Format: date, decision, one-line why.

## Rejected (do not build)

- **Guests tier / thread-context promotion / protected_events.json** —
  required storing event ids (violates the state invariant) and duplicated
  the Lord's Chronicle relay. Thread context is Chronicle's job.
- **FETCH_CONTEXT / PARDON_BACKFILL flags** — flags on core behavior are two
  code paths; behaviors were chosen instead (no fetching, no backfill).
- **kind-5 report voids + kind-30000 pardon-list sync** — undo now lives in
  exactly one place: the web UI pardon, through the ledger.
- **Favorite reparenting (cut-branch favorites become the Lord's invitees)** —
  obsolete once elevation became tree-independent. Nothing to rescue.
- **Automatic backfill of all follows/citizens** — a standing network job
  with real bandwidth, disk, and relay-reputation costs. Archival is manual,
  per-member, one-shot (the scribe).
- **Towncrier as a feed (rendering members' notes)** — turns the page into a
  Nostr client; kills the 60KB budget. Rows link to njump instead.
- **Automated diamond-sorting (public reports, follows-of-follows
  whitelists, AI filtering)** — public reports are gameable (NIP-56),
  whitelists kill the open lands, and the raid already IS the moderation:
  spam is outlived, not judged.
- **install.sh editing strfry.conf / compose / proxy configs** — auto-editing
  unknown Umbrel/Portainer stacks bricks relays. Print, never edit.

## Deferred (real ideas, wrong day)

- **NIP-86 admin shim** (banpubkey/allowpubkey/... routed by Content-Type,
  OWNER_PUBKEY only, through the ledger). Demand should precede code;
  the API + towncrier cover every operation.
- **One-click self-update** (Lord-only /api/update → helper container runs
  compose pull && up -d; steward image bundles gatekeeper and refreshes
  /plugin/ on boot). Most delicate code in the project — ship the update
  banner first, add this as its own late phase with a disabled-by-default
  first release.
- **Ward viewer permissions** (a ward_viewers pubkey list checked on
  GET /api/wards). Trivial whenever a concrete need appears — NIP-98 already
  authenticates every request. Until then, every viewer is a leak surface.
- **Random citizen note pulls** ("the castle feels alive": one REQ per cycle
  for a random citizen's recent notes). ~30 lines, harmless traffic, but not
  load-bearing. Considered, deferred.
- **docker-socket-proxy hardening** (tecnativa/docker-socket-proxy limited
  to exec on the strfry container, replacing the raw socket mount).
  Documented in the README as the hardening option; make it the default if
  the project grows an audience.

## Accepted trade-offs (known, intentional)

- **docker.sock mount is root-equivalent** on the host from an
  internet-facing container. Accepted for one-DB-owner simplicity; disclosed
  in README; proxy is the mitigation path.
- **Ward privacy is obscurity, not cryptography.** Whim-timed raids give no
  clean TTL to fingerprint retention against. The threat model that defeats
  this ("someone obsessively measuring one stranger's event lifetimes")
  has better attacks available.
- **Scribe pagination is leaky** (shared timestamps, silent relay caps).
  Best-effort archival by design; no reconciliation engine.
- **Web-first pardons mean unbanning requires a browser with NIP-07** (on
  mobile: an Amber-style signer). Accepted for deleting two sync
  subsystems. Banning still works from any client via kind-1984 reports.
- **Domain bans re-enumerate at raid cadence, not cycle cadence.** A spam
  farm's fresh pubkeys live until the next raid. Acceptable: their events
  die in the same raid that bans them.
