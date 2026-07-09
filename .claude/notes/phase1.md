# Phase 1 notes — for Phase 2 (and beyond)

Handoff trivia for gatekeeper. Not a substitute for CLAUDE.md/PLAN.md/
DECISIONS.md.

## What landed

`gatekeeper/main.go` is now the real thing: tier decision (banned → Castle
Mail → citizens → Outer Lands, first match wins, matching CLAUDE.md's
table), hot-reloading `store` for banned.json/citizens.json (mtime-gated,
poll interval injectable, fail-open on missing files), a per-IP token
`limiter` (30/min, burst 60, idle eviction swept at most once/minute), and
`processLine`/`decide` factored out of `main()` so tests and the fuzzer can
drive them directly without a subprocess.

`gatekeeper/testdata/` has committed fixtures: `citizens.json`,
`banned.json`, `events.jsonl` (nine plugin-request lines covering every
tier-table row plus two deliberately malformed lines). Test pubkeys are
recognizable repeated-hex-digit strings (`1111...`=tree citizen,
`2222...`=follow citizen, `3333...`=ward, `9999...`=banned, `aaaa...`=gift
wrap author→citizen, `bbbb...`=zap author→citizen, `cccc...`=gift wrap
author→stranger, `5555...`=stranger) — none of these are real secp256k1
keypairs, they're just distinct strings gatekeeper's string-set matching
doesn't care about the difference. Don't confuse them with the *real*
generated keys used in the manual smoke check below.

`gatekeeper/fuzz_test.go` has `FuzzProcessLine`; ran clean for 30s
(6.7M execs, zero crashes) — see the DECISIONS.md-adjacent note below,
this was run in this environment even though Docker wasn't available.

## UPDATE (Phase 1 remediation, Step 2): Docker debt paid, both items confirmed

Docker became available in this environment (colima + Docker Engine
29.x). Ran the manual smoke check PLAN.md's Phase 1 acceptance bar asks
for, against a real `dockurr/strfry:latest` container with the actual
compiled gatekeeper binary wired in as `writePolicy.plugin`:

1. **Citizen event accepted; banned event rejected** — CONFIRMED. A
   real NIP-01-signed kind-1 note from a citizen pubkey (in citizens.json)
   published successfully; the same from a banned pubkey came back
   `failed: msg: blocked: you have been exiled from these lands` — the
   exact themed reject message, verbatim.
2. **strfry routes ephemeral-kind events through the write policy plugin**
   — CONFIRMED. Published a kind-20001 event authored by the banned
   pubkey; it was rejected with gatekeeper's exact ban message, proving
   strfry invokes the plugin for ephemeral kinds (20000-29999) exactly
   like any other kind. `TestDecide_EphemeralStrangerRidesBucket`'s
   premise holds; DECISIONS.md's ephemeral-kind entry updated from
   UNVERIFIED to CONFIRMED.

Also verified for good measure (not required by the acceptance bar, but
cheap given a live strfry): a gift wrap (kind 1059) from an unrelated,
never-before-seen key, p-tagging the citizen, was accepted — Castle Mail's
recipient-judged rule works end-to-end through a real relay, not just in
gatekeeper's unit tests.

**Bug found and fixed along the way:** `deploy/docker-compose.yml` mounted
`strfry.conf.patch` at `/app/strfry.conf`, but dockurr/strfry's entrypoint
reads its config from `/etc/strfry.conf` (confirmed via `strfry --help`:
`--config=<config>` defaults to `$STRFRY_CONFIG || "/etc/strfry.conf" ||
"./strfry.conf"`). The old mount was dead — `make smoke`'s scratch strfry
was silently booting with the image's own default config (and its own
baked-in `write-policy.py`), never exercising gatekeeper at all. Fixed the
mount path. Note `strfry.conf.patch` is still just a fragment (no
db/dbParams/events/bind stanzas) — a real bootable scratch config for
`make smoke`'s eventual gatekeeper assertions is Phase 3a work, not solved
here. See the recipe below for how to build one ad hoc.

**Also found:** dockurr/strfry's `nofiles = 1000000` default config value
exceeds colima's VM ulimit ceiling (524288) and the container refuses to
start (`Unable to set NOFILES limit`) unless the config value is lowered
and the container's own ulimit is raised to match (`docker create --ulimit
nofile=1048576:1048576`). Environment-specific (colima), not a strfry or
gatekeeper bug, but worth knowing if `make smoke` is built out on a
similarly-constrained host.

**Colima/virtiofs gotcha:** bind-mounting individual host *files* (not
directories) into a fresh container via `-v host/file.conf:/etc/strfry.conf`
raced with virtiofs file-visibility propagation and caused dockerd (running
inside the colima VM) to silently auto-vivify the mount point as an empty
*directory* on the host, clobbering nothing yet but breaking the mount
(`strfry.sh` explicitly checks for and refuses a directory at the config
path). Workaround: `docker create` (unstarted) + `docker cp` each file/dir
in individually + `docker start`, which sidesteps bind-mount source-race
entirely. If `make smoke` ever needs single-file bind mounts on colima,
expect to hit this.

### Old note (superseded by the above, kept for history)

Originally, no Docker was available in this environment, which blocked
both of PLAN.md's Phase 1 acceptance items above. `nak` *was* installed
locally (`/opt/homebrew/bin/nak`, v0.19.9), even though Docker wasn't at
the time. That made a real (non-Docker) verification possible:
built the actual compiled gatekeeper binary, generated genuine secp256k1
keypairs and NIP-01-signed events with `nak key generate` / `nak event
--sec ...`, and fed them through the plugin-protocol stdin/stdout format
directly into the binary (no strfry in the loop, but real signed events
and the real binary, not just Go unit tests). Confirmed by hand:
- citizen-authored note → accept
- banned-authored note → reject with the exact themed message
- gift wrap (kind 1059) from an unrelated random key, p-tagging a citizen
  → accept
- 65 identical stranger events from one source IP → 60 accepted, 5
  rejected with the rate-limit message (exactly the spec'd burst=60)
- hot-reload against the *real* filesystem clock and the *real* default
  1-second poll interval (not the fake-clock unit test): banned a
  previously-accepted pubkey mid-process by rewriting banned.json, waited
  >1s, same pubkey's next event was rejected — on a long-lived running
  process, not a fresh one.

This was throwaway, done in the scratchpad dir, not committed. If you want
to redo it, the recipe is: `nak key generate -q` for a hex seckey, `nak key
public <sec> -q` for the hex pubkey, `nak event --sec <sec> -k <kind> -c
<content> [-p <pubkey>] -q` prints the signed event JSON without
publishing (no relay arg = print only), wrap it as
`{"type":"new","event":<that>,"sourceInfo":"<ip>"}` and pipe lines into the
built binary with `CASTLE_STATE_DIR` — wait, no: **gatekeeper has no env
var for its state dir**, it's hardcoded to `/plugin` (see DECISIONS.md).
For a from-binary manual test like this you have to either symlink/copy a
`/plugin` you can write to (needs root or a mount namespace trick) or
temporarily patch the `stateDir` const — the smoke check above briefly
edited the constant locally, tested, then reverted; it is not left in the
tree. `go test ./gatekeeper/...` and the fuzz target don't need this at
all since they construct `*store` directly against `t.TempDir()`.

## SUPERSEDED: "gatekeeper has zero env vars, on purpose"

This was true against the stale pre-firehose spec Phase 1 was built
against. The final CLAUDE.md inverted the rate-limit design (see
REMEDIATION.md / DECISIONS.md's "Two buckets, inverted from the first
draft" entry): gatekeeper now reads `MAIL_RATE_PER_MIN` (default 10) and
`LANDS_RATE_PER_MIN` (default 0) at startup via `envRate()`. The state
directory (`/plugin`, hardcoded) is still a compile-time constant — that
part of the original reasoning stands, per DECISIONS.md's still-current
"gatekeeper's state directory is hardcoded" entry.

## deploy/smoke.sh is still untouched

Still the Phase 0 placeholder (boots scratch strfry, polls until it
answers, no assertions). Phase 1 remediation's Step 2 (see the UPDATE
section above) did the acceptance-bar verification by hand against an
ad-hoc container instead of through `make smoke`/docker-compose, and found
+ fixed a real bug in `deploy/docker-compose.yml`'s config mount path along
the way. Wiring `make smoke` itself to build gatekeeper into the
castle-state volume, generate a full bootable strfry.conf (strfry.conf.patch
is just a fragment), and drive nak-signed accept/reject/ephemeral-kind
checks against a live compose stack is still Phase 3a work — the ad-hoc
recipe in the UPDATE section above is the starting point for whoever builds
it out.

## internal/stateformat was untouched

Phase 0's `Banned`/`Citizens` structs were already exactly what gatekeeper
needed; no changes. gatekeeper is now the second (and last, per
DECISIONS.md's "one wrapper" spirit for stateformat) real consumer —
steward's writer side is Phase 3a.
