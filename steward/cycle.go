// Cycle loop: follows sync, ledger merge. Runs every CYCLE_MINUTES. Stats
// (step 3) lands in Phase 3b. See CLAUDE.md, "Cycle loop (every
// CYCLE_MINUTES)".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Cycle holds everything one run of the cycle loop needs. Fetcher, Scanner,
// and ReleaseChecker are interfaces precisely so tests can fake the network
// and strfry without either being live.
type Cycle struct {
	StateDir       string
	Owner          string
	OwnRelay       string
	PublicRelays   []string
	MaxInvites     int
	MaxDepth       int
	OuterTTLDays   int
	RaidCron       string
	RaidDryRun     bool
	RunningVersion string
	Fetcher        NostrFetcher
	Scanner        strfryScanner
	CLI            strfryCLI
	ReleaseChecker ReleaseChecker
	Now            func() time.Time
}

// NewCycle builds a Cycle from config plus the real network/strfry
// dependencies. Used by main.go; tests construct a Cycle literal directly
// with fakes instead.
func NewCycle(cfg config, fetcher NostrFetcher, scanner strfryScanner, cli strfryCLI, releaseChecker ReleaseChecker) *Cycle {
	return &Cycle{
		StateDir:       "/state",
		Owner:          cfg.OwnerPubkey,
		OwnRelay:       ownRelayURL(cfg.StrfryContainer),
		PublicRelays:   cfg.PublicRelays,
		MaxInvites:     cfg.MaxInvites,
		MaxDepth:       cfg.MaxDepth,
		OuterTTLDays:   cfg.OuterTTLDays,
		RaidCron:       cfg.RaidCron,
		RaidDryRun:     cfg.RaidDryRun,
		RunningVersion: buildVersion,
		Fetcher:        fetcher,
		Scanner:        scanner,
		CLI:            cli,
		ReleaseChecker: releaseChecker,
		Now:            time.Now,
	}
}

func (c *Cycle) ledgerPath() string       { return filepath.Join(c.StateDir, "ledger.jsonl") }
func (c *Cycle) followsPath() string      { return filepath.Join(c.StateDir, "follows.json") }
func (c *Cycle) departuresPath() string   { return filepath.Join(c.StateDir, "departures.json") }
func (c *Cycle) citizensPath() string     { return filepath.Join(c.StateDir, "citizens.json") }
func (c *Cycle) treePath() string         { return filepath.Join(c.StateDir, "tree.json") }
func (c *Cycle) statsPath() string        { return filepath.Join(c.StateDir, "stats.json") }
func (c *Cycle) censusPath() string       { return filepath.Join(c.StateDir, "census.json") }
func (c *Cycle) nameCachePath() string    { return filepath.Join(c.StateDir, "name-cache.json") }
func (c *Cycle) releaseCachePath() string { return filepath.Join(c.StateDir, "release-check.json") }

// Run executes one full cycle: follows sync, ledger merge (citizens.json and
// tree.json rewritten atomically), then stats (stats.json, the name cache,
// and the daily release check).
func (c *Cycle) Run(ctx context.Context) error {
	start := time.Now()
	slog.Info("cycle starting")

	entries, err := ReadLedger(c.ledgerPath())
	if err != nil {
		return fmt.Errorf("cycle: read ledger: %w", err)
	}
	state, err := BuildState(c.Owner, entries, c.MaxInvites, c.MaxDepth)
	if err != nil {
		return fmt.Errorf("cycle: build state: %w", err)
	}

	// 1. Follows sync.
	follows, err := c.syncFollows(ctx)
	if err != nil {
		return fmt.Errorf("cycle: sync follows: %w", err)
	}

	// 2. Merge.
	if err := writeJSONAtomic(c.citizensPath(), state.CitizensJSON(follows.Pubkeys)); err != nil {
		return fmt.Errorf("cycle: write citizens.json: %w", err)
	}
	if err := writeJSONAtomic(c.treePath(), state.Tree); err != nil {
		return fmt.Errorf("cycle: write tree.json: %w", err)
	}

	// 3. Stats.
	if err := c.generateStats(ctx, state, follows, entries); err != nil {
		return fmt.Errorf("cycle: generate stats: %w", err)
	}

	slog.Info("cycle complete",
		"duration", time.Since(start),
		"tree_members", len(state.Tree.Members),
		"follows", len(follows.Pubkeys),
	)
	return nil
}

// FollowsSnapshot is follows.json's schema: the Lord's last-good kind-3
// pubkey list plus its source event id and created_at, so a fetch failure
// or a restart mid-outage can never shrink the citizenry (CLAUDE.md,
// "Durable state").
type FollowsSnapshot struct {
	Pubkeys   []string `json:"pubkeys"`
	Source    string   `json:"source"`
	CreatedAt int64    `json:"created_at"`
}

func readFollows(path string) (FollowsSnapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return FollowsSnapshot{}, nil
	}
	if err != nil {
		return FollowsSnapshot{}, err
	}
	var snap FollowsSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return FollowsSnapshot{}, err
	}
	return snap, nil
}

// Departures is departures.json's schema: pubkey -> unix time steward first
// saw them missing from the Lord's follow list. It gives unfollowed pubkeys
// the same OUTER_TTL_DAYS grace the ledger gives evicted members ("Losing
// citizenship is not a purge"). It lives outside the ledger because the
// ledger records admin actions taken through the API; an unfollow happens on
// Nostr, not here. Durable, not derived: follows.json keeps no history, so
// this file cannot be rebuilt.
type Departures struct {
	Departed map[string]int64 `json:"departed"`
}

// readDepartures returns an empty, non-nil map when the file does not exist
// yet. Any other read or parse failure is an error: callers must not treat
// an unreadable grace record as "nobody is in grace".
func readDepartures(path string) (map[string]int64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var d Departures
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Departed == nil {
		d.Departed = map[string]int64{}
	}
	return d.Departed, nil
}

// recordDepartures updates departed in place for a follow-list change from
// prev to next and reports whether anything changed. Anyone in prev but not
// next departs at now (steward's own clock, never the kind-3's created_at,
// so an old event cannot backdate a departure past the grace window). An
// existing entry for someone in prev is overwritten: they were a follow
// until this update, so any older stamp is stale. Anyone in next is
// cleared (re-followed). Entries for pubkeys in neither list are ongoing
// grace windows and are left alone.
func recordDepartures(departed map[string]int64, prev, next []string, now int64) bool {
	inNext := make(map[string]bool, len(next))
	for _, pk := range next {
		inNext[pk] = true
	}
	changed := false
	for _, pk := range next {
		if _, ok := departed[pk]; ok {
			delete(departed, pk)
			changed = true
		}
	}
	for _, pk := range prev {
		if !inNext[pk] {
			departed[pk] = now
			changed = true
		}
	}
	return changed
}

// syncFollows fetches the Lord's newest kind-3 across OwnRelay + PublicRelays
// and replaces follows.json only if it is newer than what's on disk. Any
// fetch failure (or no kind-3 found anywhere) keeps the previous snapshot,
// "never shrink on error", and is logged, never fatal to the cycle.
//
// Fail-safe rules, because a raid deletes whatever stops being a citizen:
//   - An unreadable follows.json or departures.json is an error that halts
//     the cycle (citizens.json keeps its last-written contents) instead of
//     reading as an empty follow list. Raid refuses on the same condition.
//   - A kind-3 with no follows never replaces a non-empty snapshot. Clients
//     publishing a contact list from a cold cache is a known failure mode;
//     it is logged every cycle until a newer, non-empty kind-3 arrives.
//   - Every other change is applied, and pubkeys that dropped out are
//     stamped in departures.json (written before follows.json) so the raid
//     grants them the OUTER_TTL_DAYS grace window.
func (c *Cycle) syncFollows(ctx context.Context) (FollowsSnapshot, error) {
	path := c.followsPath()
	current, err := readFollows(path)
	if err != nil {
		slog.Error("follows.json is unreadable; refusing to treat it as an empty follow list. "+
			"Follows sync and the rest of this cycle are halted, and raids will refuse to run, "+
			"until it is restored from a backup or removed by hand", "path", path, "error", err)
		return FollowsSnapshot{}, fmt.Errorf("read follows.json: %w", err)
	}
	departed, err := readDepartures(c.departuresPath())
	if err != nil {
		slog.Error("departures.json is unreadable; halting follows sync so no grace record is lost. "+
			"Raids will refuse to run until it is restored or removed by hand", "path", c.departuresPath(), "error", err)
		return FollowsSnapshot{}, fmt.Errorf("read departures.json: %w", err)
	}

	relays := append([]string{c.OwnRelay}, c.PublicRelays...)
	latest, err := c.Fetcher.LatestKind3(ctx, relays, c.Owner)
	if err != nil {
		slog.Warn("follows sync failed, keeping previous snapshot", "error", err)
		return current, nil
	}
	if latest == nil || int64(latest.CreatedAt) <= current.CreatedAt {
		return current, nil
	}

	var pubkeys []string
	for _, tag := range latest.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pubkeys = append(pubkeys, tag[1])
		}
	}
	if len(pubkeys) == 0 && len(current.Pubkeys) > 0 {
		slog.Error("newest kind-3 lists no follows; NOT applying it, keeping the previous follow list. "+
			"This is usually a client publishing from an empty cache. Publish a corrected follow list to clear this",
			"source", latest.ID, "created_at", int64(latest.CreatedAt),
			"kept_source", current.Source, "kept_follows", len(current.Pubkeys))
		return current, nil
	}
	sort.Strings(pubkeys)
	next := FollowsSnapshot{Pubkeys: pubkeys, Source: latest.ID, CreatedAt: int64(latest.CreatedAt)}

	if recordDepartures(departed, current.Pubkeys, next.Pubkeys, c.Now().Unix()) {
		if err := writeJSONAtomic(c.departuresPath(), Departures{Departed: departed}); err != nil {
			slog.Warn("write departures.json failed, keeping previous follows", "error", err)
			return current, nil
		}
	}
	if err := writeJSONAtomic(path, next); err != nil {
		slog.Warn("write follows.json failed, keeping previous", "error", err)
		return current, nil
	}
	return next, nil
}
