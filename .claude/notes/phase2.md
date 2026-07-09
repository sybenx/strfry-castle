# Phase 2 notes — for Phase 3a (and beyond)

Handoff trivia for steward's core domain logic. Not a substitute for
CLAUDE.md/PLAN.md/DECISIONS.md.

## What landed

Four files, all `package main` in `steward/`, pure logic, zero network:

- `ledger.go` — `Entry` (the ledger.jsonl line shape, all 12 verbs from
  CLAUDE.md's durable-state list), `AppendLedger`/`ReadLedger` (append-only,
  `"v":1` stamped on write, unknown versions rejected loudly on read), and
  `State` — the full replayed domain state (Tree + Elevation + Bans +
  Evicted timestamps).
- `tree.go` — `Tree`/`Member`, `Invite`/`Ennoble`/`removeSubtree`, depth and
  direct-invite-count helpers. `MarshalJSON` on `*Tree` already produces
  tree.json's exact schema (`{"members": {...}}`).
- `elevation.go` — `Elevation`/`ElevationRecord` (one map, `Public` bool is
  the only visibility bit), `Bans` (pubkeys + domains, domains are
  steward-side bookkeeping only per CLAUDE.md — gatekeeper never sees them).
- `main.go` gained `writeJSONAtomic` (temp file in the same dir + rename).
  Nothing calls it yet — Phase 3a's cycle loop and Phase 5a's API mutations
  are the real callers, writing tree.json/citizens.json/banned.json. It's
  tested standalone (`TestWriteJSONAtomicCreatesAndReplaces`).

## The key design decision: Apply vs. the public State.* methods

`State.Apply(Entry) error` is the **single** place that mutates Tree/
Elevation/Bans structurally from a decided entry. It is used by both:

1. `BuildState(owner, entries, maxInvites, maxDepth)` — replay from
   scratch (what a restart or a round-trip through disk does).
2. The public mutation methods (`State.Invite`, `.Remove`, `.Ennoble`,
   `.BanPubkey`, `.PardonPubkey`, `.BanDomain`, `.PardonDomain`, `.Elevate`,
   `.Lower`, `.RecordArchiveRun`, `.RecordRaidRun`) — each validates
   permission/business rules the ledger doesn't encode (e.g. "member can
   only remove their own invitee" needs a live requester argument that
   isn't stored per-entry), then calls `Apply` and returns the `Entry` that
   was appended so the caller persists it via `AppendLedger`.

**Why this split:** once an entry is in the ledger, replay must reproduce it
unconditionally — replay time isn't the place to re-litigate "was this
requester allowed to do this." Permission checks that need context beyond
the entry itself (who's asking) live only in the mutation-method layer, not
in `Apply`. Structural invariants that must hold no matter how an entry was
produced (MAX_INVITES/MAX_DEPTH inside `Tree.Invite`, OWNER_PUBKEY
unbannable inside `Apply`'s `VerbBan` case) live in `Apply` so replay can't
silently reconstruct an impossible state either.

Phase 5a's `api.go` handlers should call the `State.*` methods directly —
they already are the validated, ledger-appending, ready-to-use mutation
API. Don't reinvent permission checks in `api.go`; do the NIP-98 identity
resolution there and pass the resulting pubkey as `requester`/`inviter` etc.

## Ban-cuts-branch vs. plain removal: two different eviction-grace outcomes

This was the trickiest subtlety and is pinned by
`TestBanningTreeMemberCutsBranchAndGracePeriodsSubtreeOnly`:

- A **plain** `Remove` (member/Lord cutting a branch, nobody's an outlaw)
  grace-periods **every** removed pubkey, including the branch root, via
  `State.Evicted[pk] = timestamp`. That's what lets the raid keep an
  evicted member's notes for `OUTER_TTL_DAYS`.
- A **ban** that happens to hit a tree member cuts the same branch
  structurally, but the banned pubkey itself is **excluded** from
  `Evicted` — CLAUDE.md is explicit that "Explicit bans DO purge
  immediately," so an outlaw must never get a grace window. Its innocent
  *descendants*, who are not themselves banned, still get graced normally.
  Get this backwards and either an outlaw's notes survive 30 days they
  shouldn't, or an innocent descendant's notes get purged immediately when
  they shouldn't.

`Evicted` is in-memory only (part of `State`, rebuilt by replay each time);
there's no `evicted.json`. Phase 3a/4's cycle and raid logic should call
`BuildState` fresh and read `.Evicted` off the result, not try to persist
it separately — CLAUDE.md's raid pseudocode computes the grace window from
"removal timestamps come from the ledger" directly, which is exactly what
replay already gives you.

## Elevate/flip-visibility/no-op semantics

`State.Elevate(target, public, source, at)` decides the verb itself:
not-yet-elevated → `elevate`; already elevated with a *different*
visibility → `flip-visibility` (never a second `elevate` line); already
elevated with the *same* visibility → `ErrNoChange`, nothing appended, no
ledger line written. Phase 5a's `/api/elevate` handler should treat
`ErrNoChange` as a success (200, no-op), not a client error — re-elevating
to the same visibility is exactly the idempotent behavior CLAUDE.md asks
for, it just happens to mean "nothing to persist."

Same idempotent-no-op treatment applies to `State.Lower` when the target
isn't currently elevated.

## Citizens computation excludes banned pubkeys defensively

`State.Citizens(follows []string)` unions `{Owner} ∪ tree ∪ elevated ∪
follows`, then drops anything in `Bans.Pubkeys`. This is belt-and-suspenders
— gatekeeper's Outlaws tier already wins over citizenship regardless of
citizens.json's contents (ban check precedes everything in the tier table)
— but it keeps `citizens.json`/stats counts from double-counting an outlaw
who's still in someone's kind-3 follow list or was never un-elevated before
being banned (impossible via this code path since `Apply`'s `VerbBan` case
always calls `Elevation.lower` too, but follows are external/async — Phase
3a syncs them from relays and can't retroactively edit history).

## What Phase 3a needs to wire up that Phase 2 deliberately left alone

- `follows.json` (last-good kind-3 snapshot) doesn't exist yet — `State.
  Citizens`/`CitizensJSON` take `follows []string` as a plain parameter;
  Phase 3a owns fetching/persisting that slice, Phase 2 just consumes it.
- Nothing calls `writeJSONAtomic` yet. Phase 3a's cycle loop is the first
  real caller for tree.json/citizens.json/banned.json (`State.Tree` already
  has the right `MarshalJSON`; `State.CitizensJSON`/`BannedJSON` produce
  `internal/stateformat` shapes directly).
- No HTTP/API layer exists (`api.go` is still the Phase 0 stub) — that's
  Phase 5a. The mutation methods above are written *for* that layer already.
- React-warding (kind-7 watermark, PUBLIC_RELAYS fallback fetch) isn't
  implemented — `Elevation.elevate`/`lower` are ready to be called by it,
  but the fetch/watermark machinery is all Phase 3a/network.
- No ban/pardon-domain → pubkey resolution exists yet (that's the raid-time
  `.well-known/nostr.json` re-enumeration + kind-0 sweep, Phase 4).
  `Bans.Domains` only tracks which domains are banned; nothing walks kind-0
  events yet.

## Property test note

`TestReplayIsDeterministic` (ledger_test.go) drives 30 trials of 40 random
mutations each through the real `State.*` methods (so only entries that
actually validated get appended — this matters, since replaying a raw
random entry stream would hit `Tree.Invite`'s own MAX_INVITES/MAX_DEPTH
errors for reasons unrelated to what's being tested), then checks that (a)
replaying those entries from a fresh `BuildState` call and (b) round-
tripping them through a real on-disk `AppendLedger`/`ReadLedger` cycle both
reproduce byte-identical Tree/Elevation/Bans/Citizens. It was originally
100 trials × 60 actions (~12s, `AppendLedger`'s `fsync` per line dominates);
trimmed to 30×40 (~3s) for CI friendliness once the property itself had
been exercised at the larger size once, locally, and found no failures.
If you suspect a replay-determinism regression, temporarily bump the
trial/action counts back up rather than assuming 30×40 is exhaustive.
