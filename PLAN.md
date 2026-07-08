# PLAN.md — build plan for the Castle

Read CLAUDE.md first; it is the spec and the source of truth. This file is
the order of operations. Work one phase per session where possible; Phases 3
and 5 are pre-split because they won't fit in one. Every phase ends with its
tests green and a commit. Do not start a phase until the previous phase's
acceptance criteria pass. Check off each phase here (`[x]`) when accepted.
Resist adding anything not in the spec — light as a whip.

## [ ] Phase 0 — Skeleton (30 min)
Repo layout per CLAUDE.md, Go module, Makefile (build/test/smoke/bytecheck,
cross-compile linux/amd64 + linux/arm64), .env.example (RAID_DRY_RUN=true,
RAID_CRON empty), empty test files, seeded DECISIONS.md, CI workflow that
runs `make test` on push and asserts both gatekeeper binaries are static
(`ldd` says "not a dynamic executable").
**Accept:** `make build` produces static binaries for both arches; CI green
on a trivial test including the static check.

## [ ] Phase 1 — gatekeeper (the plugin)
Pure stdlib. stdin/stdout JSONL loop, hashset checks against banned.json /
citizens.json, Castle Mail recipient rule, per-IP token bucket with idle
eviction, mtime hot reload with injectable poll interval, fail-open on
missing files, malformed-line resilience, a native fuzz target on the stdin
loop, themed reject messages. Committed fixtures in gatekeeper/testdata/.
Unit tests for every gatekeeper row of the CLAUDE.md checklist.
**Accept:** all gatekeeper checklist tests pass; fuzz target runs clean for
30s; manual smoke against an ad-hoc local strfry in docker accepts a citizen
event and rejects a banned one using the committed fixtures. Commit + tag
v0.1.0.

## [ ] Phase 2 — steward core: ledger, tree, elevation (no network)
ledger.jsonl append/replay with all verbs (invite/remove/ennoble/ban/pardon/
ban-domain/pardon-domain/elevate/lower/flip-visibility/archive-run/raid-run);
tree.go with invite/remove/ennoble/ban-cuts-branch, MAX_INVITES/MAX_DEPTH;
elevation.go (one set + visibility flag, ban-beats-elevation, no
reparenting); eviction timestamps recorded on removal; citizens
recomputation ({Lord} ∪ tree ∪ follows ∪ elevated); atomic state-file
writers (temp in same dir + rename). Pure logic — test exhaustively now.
Property test: ledger replay always reconstructs identical tree + elevation
+ ban state.
**Accept:** all tree + elevation + ledger checklist tests pass, including
"wards absent from every public projection."

## [ ] Phase 3a — steward cycle: sync + intake
go-nostr client work: follows sync with follows.json last-good snapshot
(never shrink on error, survive restart, only replace with newer kind 3);
kind-1984 report intake (spam/illegal/malware pubkey bans, `ban-domain:`
lines; NO kind-5 voids, NO pardon-list sync); react-warding from kind 7
with watermark; ledger merge; purge-on-ban via the docker-exec strfry
wrapper (interfaced so tests fake it).
**Accept:** steward checklist tests for sync/intake/react-warding pass;
cycle runs against a scratch strfry in docker-compose with fixture events
published via nak; banned.json/citizens.json/tree.json come out correct.

## [ ] Phase 3b — stats + update check
stats.json per the schema (public counts exclude wards; raids.next null when
manual), batched `strfry scan --count`, daily GitHub release check feeding
`version` in stats.json.
**Accept:** stats.json validates against the schema from a live compose
stack; ward count appears nowhere.

## [ ] Phase 4 — the raid
Domain re-enumeration + local kind-0 nip05 sweep at raid time; streaming
scan-then-delete with the three keep-conditions (citizen, Castle Mail,
eviction grace); batching; RAID_DRY_RUN honored (default ON); ledger logging
of purge counts; optional RAID_CRON scheduling + the manual trigger hook the
API will call.
**Accept:** raid checklist tests pass, including "dry run deletes nothing,"
"stranger-to-stranger gift wrap past TTL deleted, citizen-addressed gift
wrap survives at any age," and "evicted member's notes survive the grace
window, die after."

## [ ] Phase 5a — HTTP API
NIP-98 verification (sig, u, method, ±60s, 5-min replay guard), all
endpoints from CLAUDE.md including /api/wards (Lord only), /api/elevate,
/api/lower, /api/archive, /api/raid; immediate state rewrite on mutation;
per-IP rate limit; same-origin CORS; static file serving for towncrier;
NIP-05 serving when NIP05_DOMAIN is set.
**Accept:** API checklist tests pass; curl + nak-signed headers can invite,
remove, elevate, ban, pardon, and trigger a dry-run raid end-to-end against
the compose stack; /api/wards refuses a non-Lord signature.

## [ ] Phase 5b — the scribe
POST /api/archive one-shot job: paginated REQ backfill (until-cursor,
limit 500, polite pacing, backoff on CLOSED), replay into the castle over a
local websocket, one job at a time, own goroutine/failure domain, counts
logged to ledger.
**Accept:** scribe checklist tests pass against a fixture relay; killing the
scribe mid-job leaves the cycle untouched.

## [ ] Phase 6 — towncrier
One index.html, < 60KB (bytecheck added to CI in this phase), no deps, no
build. Public sections per CLAUDE.md: Lord, Court (tree as nested <details>
with stars), Favored, Citizenry, Vault, Evicted (struck-through + expiry),
Wild West ("at the Lord's pleasure" when manual), Exiled, NIP-11 footer,
copy-relay-URL, njump profile links. Then the NIP-07 layer: sign-in, member
invite/remove with branch-fall confirm, full Lord controls including star
toggles, ban/pardon with domain field, archive buttons, raid-now, update
banner, double-confirm on banning the elevated, and the Lord-only Wards
section fed exclusively by authenticated /api/wards.
**Accept:** renders correctly from steward with real stats.json; a NIP-07
extension performs an invite and a ward-add in a browser; a non-Lord
sign-in shows no ward UI and no ward data appears in any response it can
trigger; payload under budget in CI.

## [ ] Phase 7 — distribution
Release workflow (binaries + checksums + ghcr multi-arch image), install.sh
(print-don't-edit for strfry.conf / compose / proxy), uninstall.sh, README
with screenshot and the docker.sock disclosure. Test install.sh against a
clean container running a stock strfry compose stack.
**Accept:** following install.sh's printed instructions on a fresh box
yields a working castle with manual dry-run raids; uninstall leaves the
stock setup intact.

## Standing orders
- After any change to tier or elevation logic, re-run the full gatekeeper
  fixture suite AND the elevation privacy tests.
- Never write to stdout in gatekeeper except protocol responses.
- Never let a network failure shrink citizens or forget bans (ledger +
  follows.json are truth).
- Wards in public output = release-blocking bug. Grep public projections in
  tests, not by eye.
- Keep towncrier's byte budget: `wc -c` in CI, fail over 60KB.
- steward state is pubkeys, timestamps, admin actions — never event ids. Any
  feature that wants a stored event id gets written to DECISIONS.md as
  rejected, not implemented.
- When a decision isn't covered by CLAUDE.md, choose the option with less
  code, and note it in DECISIONS.md.
