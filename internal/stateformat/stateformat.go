// Package stateformat defines the on-disk JSON shapes shared between
// steward (the writer) and gatekeeper (the reader) for banned.json and
// citizens.json. It is stdlib-only so gatekeeper's stdlib-only constraint
// (CLAUDE.md, Component 1) is never put at risk by a shared dependency.
package stateformat

// Banned is the shape of banned.json: the set of banned pubkeys.
// Banned domains are resolved to pubkeys by steward before this file is
// written; gatekeeper never sees domains (CLAUDE.md: "Domain checking is
// steward-side only and asynchronous").
type Banned struct {
	Pubkeys []string `json:"pubkeys"`
}

// Citizens is the shape of citizens.json: the effective citizenry
// (Lord ∪ tree members ∪ follows ∪ elevated, including wards — this file
// is a shared-volume file, never an API response, so ward inclusion here
// does not violate the ward privacy invariant). It carries no visibility
// info: gatekeeper cannot and need not distinguish a favorite from a ward.
type Citizens struct {
	Pubkeys []string `json:"pubkeys"`
}
