# PLAN.md — build plan for the Castle

Read CLAUDE.md first; it is the spec and the source of truth. This file is the
order of operations. Work one phase per session where possible. Every phase
ends with its tests green and a commit. Do not start a phase until the
previous phase's acceptance criteria pass. Resist adding anything not in the
spec — light as a whip.

**Progress tracking:** when a phase's acceptance criteria pass, change its
`[ ]` to `[x]` in this file and commit that edit with the phase. A fresh
session should be able to read this file and know exactly where the build
stands without archaeology.

## [ ] Phase 0 — Skeleton (30 min)
Repo layout per CLAUDE.md, Go module, Makefile with ALL targets from the
CLAUDE.md Commands section (build/test/fuzz/smoke/check-static/check-size —
stub what doesn't exist yet), seed DECISIONS.md with a header, .env.example,
empty test files, CI workflow that runs `make test` and `make check-static`
on push.
**Accept:** `make build` produces static binaries for both arches;
CI green on a trivial test and the ldd static check.

## [ ] Phase 1 — gatekeeper (the plugin)
Pure stdlib. stdin/stdout JSONL loop, hashset checks against banned.json /
citizens.json, Castle Mail recipient rule, per-IP token bucket with idle
eviction, mtime-based hot reload with an injectable poll interval, fail-open
on missing files, malformed-line resilience, themed reject messages.
Unit tests with piped fixtures for every accept/reject row of the tier table
in CLAUDE.md (retention rows are the raid's job, not gatekeeper's). Fixtures
are committed files under `gatekeeper/testdata/` — hand-written banned.json,
citizens.json, and event JSONL. Add a Go native fuzz target on the stdin
loop; wire `make fuzz` into CI with a short -fuzztime. The hot-reload test
uses the injected poll interval — no wall-clock sleeps.
**Accept:** all gatekeeper tests in the CLAUDE.md checklist pass; `make
check-static` passes in CI; a manual smoke against an ad-hoc local strfry in
docker accepts a citizen event and rejects a banned one. Commit + tag v0.1.0.

## [ ] Phase 2 — steward core: ledger + tree (no network yet)
ledger.jsonl append/replay; tree.go with invite/remove/ennoble/ban-cuts-branch,
MAX_INVITES/MAX_DEPTH enforcement; state-file writers (atomic temp+rename,
temp in the same directory); citizens recomputation from
{Lord} ∪ tree ∪ follows-snapshot. This is pure logic — test it exhaustively
now, before networking exists. Property test: ledger replay always
reconstructs identical state.
**Accept:** all tree + ledger tests in the checklist pass.

## [ ] Phase 3a — steward sync: citizenship + bans
go-nostr client work: follows sync with the follows.json last-good snapshot
(load on boot, never shrink on error or across restart — snapshot test with
network faked dead), report intake (spam/illegal/malware only), one-shot
domain enumeration on `ban-domain:` (well-known fetch → ledgered pubkey bans
tagged with the source report; no persistent domain state), kind-5 voids
(including voiding a domain report's enumeration), pardon list, ledger merge
and state-file writes.
**Accept:** the steward rows of the checklist pass with the network faked;
follows-snapshot restart test passes.

## [ ] Phase 3b — steward sync: enforcement + stats
Purge-on-ban via the strfry CLI wrapper over the socket proxy (interface it
so tests can fake it), the full cycle loop on CYCLE_MINUTES, stats.json.
**Accept:** cycle runs against a scratch strfry in docker-compose (with
socket-proxy) with fixture events published via nak; state files and
stats.json come out correct. `make smoke` now covers this.

## [ ] Phase 4 — the raid
Streaming scan-then-delete with the two keep-conditions (citizen author,
Castle Mail recipient), batching, dry-run mode (default ON), ledger logging
of purge counts, RAID_CRON scheduling + a manual trigger hook for the API.
**Accept:** raid tests in the checklist pass, including "dry run deletes
nothing" and "ex-citizen events deleted after branch cut."

## [ ] Phase 5 — HTTP API
NIP-98 verification (sig, u, method, ±60s, replay guard), the six endpoints +
POST /api/raid (Lord only, honors dry-run), immediate state rewrite on
mutation, per-IP rate limit, same-origin CORS, static file serving for
towncrier. /api/ban with a domain runs the same one-shot enumeration path as
report intake.
**Accept:** API tests in the checklist pass; curl + nak-signed headers can
invite, remove, ban, pardon, and trigger a dry-run raid end-to-end against
the compose stack.

## [ ] Phase 6 — towncrier
One index.html, < 60KB, no deps, no build. Public stats sections, the tree as
nested <details>, raid countdown, NIP-11 footer, copy-relay-URL. Then the
NIP-07 layer: sign-in button, invite/remove for members, full controls for
the Lord, branch-fall confirm dialog, graceful no-extension message. Add
`make check-size` to CI in this phase — the budget is enforced from the first
commit of the file, not retrofitted.
**Accept:** renders correctly from steward with real stats.json; a NIP-07
extension can perform an invite in a browser; check-size green in CI.

## [ ] Phase 7 — distribution
Per the Distribution section of CLAUDE.md: release workflow (binaries +
ghcr multi-arch image + checksums), the NON-MUTATING install.sh (downloads,
verifies, places files, prints snippets — never edits strfry.conf, the
compose stack, or proxy config), uninstall.sh, README with screenshot.
Test install.sh against a clean VM/container running a stock strfry compose
stack: follow only what the script prints and end with a working castle.
**Accept:** `curl | bash` plus pasting the printed snippets on a fresh box
yields a working castle with dry-run raids; the rate limiter distinguishes
client IPs through the proxy (realIpHeader verified); uninstall instructions
restore the original strfry.conf.

## Standing orders
- After any change to tier logic, re-run the full gatekeeper fixture suite.
- Never write to stdout in gatekeeper except protocol responses.
- Never let a network failure shrink citizens or forget bans (ledger + the
  follows snapshot are truth; this must hold across restarts too).
- gatekeeper never learns about domains — domain bans become pubkey bans in
  steward, at intake, once.
- Keep towncrier's byte budget: `make check-size` in CI, fail over 60KB.
- When a decision isn't covered by CLAUDE.md, choose the option with less
  code, and note it in DECISIONS.md.
- Features cut on purpose (Guests tier, FETCH_CONTEXT, pardon backfill,
  NIP-86 shim — see CLAUDE.md "Hard-won context") are not gaps to fill.
  Do not reintroduce them.
