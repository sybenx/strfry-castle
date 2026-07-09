// Elevation: one mechanism, one visibility flag (favorite = public,
// ward = private), ban-beats-elevation, react-warding. Lands in Phase 2/3a.
// See CLAUDE.md, "The elevation model".
package main

// ElevationRecord is one elevated pubkey: Public true is a favorite
// (starred publicly), Public false is a ward (invisible everywhere public).
// Source is the ledger/report/reaction provenance, never an event id used
// as a retention target (see CLAUDE.md's durable-state invariant).
type ElevationRecord struct {
	Public bool   `json:"public"`
	Source string `json:"source"`
}

// Elevation is tree-independent: elevating a pubkey never requires tree
// membership, and cutting a branch never touches elevation (see tree.go's
// removeSubtree, which does not consult Elevation at all).
type Elevation struct {
	Records map[string]ElevationRecord
}

func NewElevation() *Elevation {
	return &Elevation{Records: make(map[string]ElevationRecord)}
}

func (e *Elevation) IsElevated(pubkey string) bool {
	_, ok := e.Records[pubkey]
	return ok
}

func (e *Elevation) IsFavorite(pubkey string) bool {
	r, ok := e.Records[pubkey]
	return ok && r.Public
}

func (e *Elevation) IsWard(pubkey string) bool {
	r, ok := e.Records[pubkey]
	return ok && !r.Public
}

func (e *Elevation) elevate(pubkey string, public bool, source string) {
	e.Records[pubkey] = ElevationRecord{Public: public, Source: source}
}

func (e *Elevation) lower(pubkey string) {
	delete(e.Records, pubkey)
}

// Bans holds the banned pubkey and domain sets. Domains are steward-side
// only (CLAUDE.md: "gatekeeper never sees domains") — they exist here so
// replay can reconstruct ban-domain/pardon-domain history, but only
// resolved pubkeys ever reach banned.json.
type Bans struct {
	Pubkeys map[string]bool
	Domains map[string]bool
}

func NewBans() *Bans {
	return &Bans{Pubkeys: make(map[string]bool), Domains: make(map[string]bool)}
}

func (b *Bans) IsBanned(pubkey string) bool       { return b.Pubkeys[pubkey] }
func (b *Bans) IsDomainBanned(domain string) bool { return b.Domains[domain] }

func (b *Bans) ban(pubkey string)          { b.Pubkeys[pubkey] = true }
func (b *Bans) pardon(pubkey string)       { delete(b.Pubkeys, pubkey) }
func (b *Bans) banDomain(domain string)    { b.Domains[domain] = true }
func (b *Bans) pardonDomain(domain string) { delete(b.Domains, domain) }
