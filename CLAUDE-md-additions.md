# Append to your repo's CLAUDE.md

## UI language policy

The project is named **Castle**, but the UI uses plain language only. The
medieval theme was deliberately removed (see RENOVATION.md for the full
rename table and rationale). Do not reintroduce themed vocabulary in any
user-facing string.

Canonical terms:

- Owner (never Lord)
- Members / invite tree (never Court)
- Pinned (never favored/ennobled)
- Grace period / removed (never evicted)
- Private protections (never Wards)
- Unprotected events (never Outer Lands)
- Purge (never raid); "Manual only" (never "At the Lord's pleasure")

Themed copy is permitted only in: the project name, logo/favicon, and
optionally empty states or the 404 page — and only when the plain fact is
also stated.

## UI structure invariants

- Page order is a protection gradient (most → least protected):
  Owner → stat band → Members → [Private protections, owner only] →
  Grace period → Unprotected events → footer. Do not reorder.
- Pinned members are tree decoration + an avatar filter row + a stat count.
  Never a standalone section.
- Grace period renders only when non-empty and is the only section allowed
  danger/alarm styling.
- The public view must not reveal that Private protections exist.
- Sentence case; verb-first buttons; destructive actions state their
  concrete consequence.
