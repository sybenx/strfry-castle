// The ledger: ledger.jsonl append/replay, the durable source of truth for
// invites, removals, elevation, and raid runs. Every line carries "v":1.
// Lands in Phase 2.
// See CLAUDE.md, "Durable state (the invariant)".
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

const ledgerVersion = 1

// Ledger verbs, per CLAUDE.md's durable-state list.
const (
	VerbInvite         = "invite"
	VerbRemove         = "remove"
	VerbEnnoble        = "ennoble"
	VerbElevate        = "elevate"
	VerbLower          = "lower"
	VerbFlipVisibility = "flip-visibility"
	VerbRaidRun        = "raid-run"
)

// Entry is one ledger.jsonl line. Not every field applies to every verb;
// unused fields are omitted from the JSON. Event ids only ever appear here
// as Source (provenance), never as a retention or protection target.
type Entry struct {
	V         int    `json:"v"`
	Verb      string `json:"verb"`
	Timestamp int64  `json:"ts"`
	Source    string `json:"source"`

	Pubkey    string `json:"pubkey,omitempty"`     // invite/remove/ennoble/elevate/lower/flip-visibility (target)
	InvitedBy string `json:"invited_by,omitempty"` // invite
	Label     string `json:"label,omitempty"`      // invite
	Public    bool   `json:"public,omitempty"`     // elevate/flip-visibility
	Purged    int    `json:"purged,omitempty"`     // raid-run
	DryRun    bool   `json:"dry_run,omitempty"`    // raid-run
}

// Citizens is the shape of citizens.json: the effective citizenry (Lord ∪
// tree members ∪ follows ∪ elevated, including wards — this file is a
// shared-volume file, never an API response, so ward inclusion here does
// not violate the ward privacy invariant). It carries no visibility info.
type Citizens struct {
	Pubkeys []string `json:"pubkeys"`
}

// AppendLedger appends one entry to path, stamping the current ledger
// version. Append-only: opens for append, never truncates or rewrites.
func AppendLedger(path string, e Entry) error {
	e.V = ledgerVersion
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// ReadLedger reads and parses every line of path. A missing file is an
// empty ledger (not an error) — a fresh castle has no history yet. An
// unknown version is rejected loudly: CLAUDE.md's "v":1 exists precisely
// so a future format change is a migration, not a silent replay break.
func ReadLedger(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", i+1, err)
		}
		if e.V != ledgerVersion {
			return nil, fmt.Errorf("ledger line %d: unknown ledger version %d", i+1, e.V)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

var (
	ErrNoChange    = errors.New("ledger: no-op, nothing changed")
	ErrUnknownVerb = errors.New("ledger: unknown verb")
)

// State is the full replayed-from-ledger domain state: the invite tree,
// elevation records, and eviction timestamps (grace-window starts for
// members who lost citizenship by removal — see Apply's VerbRemove case).
// It is a materialized view; BuildState always reconstructs it fresh from
// a ledger.
type State struct {
	Owner      string
	MaxInvites int
	MaxDepth   int
	Tree       *Tree
	Elevation  *Elevation
	// Evicted maps a pubkey to the timestamp its citizenship ended via
	// removal (tree cut), for the raid's grace window.
	Evicted map[string]int64
}

func NewState(owner string, maxInvites, maxDepth int) *State {
	return &State{
		Owner:      owner,
		MaxInvites: maxInvites,
		MaxDepth:   maxDepth,
		Tree:       NewTree(owner),
		Elevation:  NewElevation(),
		Evicted:    make(map[string]int64),
	}
}

// BuildState replays entries from scratch into a fresh State. This is the
// one and only path that reconstructs domain state — cycle.go and API
// mutations all read the current State via replay, never by hand-patching
// a cached copy, so a restart mid-outage can never drift from the ledger.
func BuildState(owner string, entries []Entry, maxInvites, maxDepth int) (*State, error) {
	s := NewState(owner, maxInvites, maxDepth)
	for _, e := range entries {
		if err := s.Apply(e); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Apply applies an already-decided ledger entry structurally. It trusts
// that permission/business-rule validation happened before the entry was
// appended (that's the job of the State.* mutation methods below); it
// enforces only invariants that must hold no matter how an entry was
// produced, including during replay of a persisted ledger — e.g. the tree's
// MAX_INVITES/MAX_DEPTH shape limits.
func (s *State) Apply(e Entry) error {
	switch e.Verb {
	case VerbInvite:
		return s.Tree.Invite(e.InvitedBy, e.Pubkey, e.Label, e.Timestamp, s.MaxInvites, s.MaxDepth)

	case VerbRemove:
		removed, err := s.Tree.removeSubtree(e.Pubkey)
		if err != nil {
			return err
		}
		for _, pk := range removed {
			s.Evicted[pk] = e.Timestamp
		}
		return nil

	case VerbEnnoble:
		return s.Tree.Ennoble(e.Pubkey, e.Timestamp)

	case VerbElevate, VerbFlipVisibility:
		s.Elevation.elevate(e.Pubkey, e.Public, e.Source)
		return nil

	case VerbLower:
		s.Elevation.lower(e.Pubkey)
		return nil

	case VerbRaidRun:
		return nil // audit-only; no domain-state effect

	default:
		return fmt.Errorf("%w: %q", ErrUnknownVerb, e.Verb)
	}
}

// --- Mutation methods: validate, then delegate structural work to Apply. ---
// Each returns the Entry that was (or would be) appended, so the caller
// persists it via AppendLedger and rewrites the derived state files
// immediately (CLAUDE.md: "no waiting for the next cycle").

func (s *State) Invite(inviter, invitee, label, source string, at int64) (Entry, error) {
	e := Entry{Verb: VerbInvite, Pubkey: invitee, InvitedBy: inviter, Label: label, Source: source, Timestamp: at}
	if err := s.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Remove cuts target's branch. requester == s.Owner may remove anyone;
// anyone else may only remove their own direct invitees.
func (s *State) Remove(requester, target, source string, at int64) (Entry, error) {
	m, ok := s.Tree.Members[target]
	if !ok {
		return Entry{}, ErrNotFound
	}
	if requester != s.Owner && m.InvitedBy != requester {
		return Entry{}, ErrNotOwnInvitee
	}
	e := Entry{Verb: VerbRemove, Pubkey: target, Source: source, Timestamp: at}
	if err := s.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *State) Ennoble(target, source string, at int64) (Entry, error) {
	e := Entry{Verb: VerbEnnoble, Pubkey: target, Source: source, Timestamp: at}
	if err := s.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Elevate favorites (public=true) or wards (public=false) target. Per
// CLAUDE.md: re-elevating SETS the requested visibility rather than
// toggling; if target is already elevated with a different visibility the
// change is ledgered as flip-visibility, not a second elevate. If nothing
// would change, it is a true no-op (ErrNoChange, nothing appended).
func (s *State) Elevate(target string, public bool, source string, at int64) (Entry, error) {
	if r, ok := s.Elevation.Records[target]; ok {
		if r.Public == public {
			return Entry{}, ErrNoChange
		}
		e := Entry{Verb: VerbFlipVisibility, Pubkey: target, Public: public, Source: source, Timestamp: at}
		if err := s.Apply(e); err != nil {
			return Entry{}, err
		}
		return e, nil
	}
	e := Entry{Verb: VerbElevate, Pubkey: target, Public: public, Source: source, Timestamp: at}
	if err := s.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *State) Lower(target, source string, at int64) (Entry, error) {
	if !s.Elevation.IsElevated(target) {
		return Entry{}, ErrNoChange
	}
	e := Entry{Verb: VerbLower, Pubkey: target, Source: source, Timestamp: at}
	if err := s.Apply(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *State) RecordRaidRun(purged int, dryRun bool, source string, at int64) (Entry, error) {
	e := Entry{Verb: VerbRaidRun, Purged: purged, DryRun: dryRun, Source: source, Timestamp: at}
	return e, s.Apply(e)
}

// Citizens computes the effective citizenry per CLAUDE.md: {Lord} ∪ tree
// members ∪ current follows ∪ elevated (favorites and wards both — this
// file carries no visibility info, matching Citizens).
func (s *State) Citizens(follows []string) []string {
	set := map[string]bool{s.Owner: true}
	for pk := range s.Tree.Members {
		set[pk] = true
	}
	for pk := range s.Elevation.Records {
		set[pk] = true
	}
	for _, pk := range follows {
		set[pk] = true
	}
	out := make([]string, 0, len(set))
	for pk := range set {
		out = append(out, pk)
	}
	sort.Strings(out)
	return out
}

func (s *State) CitizensJSON(follows []string) Citizens {
	return Citizens{Pubkeys: s.Citizens(follows)}
}
