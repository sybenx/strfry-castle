// The ledger: ledger.jsonl append/replay, the durable source of truth for
// invites, removals, bans, pardons, elevation, and raid/archive runs. Every
// line carries "v":1. Lands in Phase 2.
// See CLAUDE.md, "Durable state (the invariant)".
package main
