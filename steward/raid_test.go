package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChunkStrings(t *testing.T) {
	items := make([]string, 120)
	for i := range items {
		items[i] = "pk"
	}
	batches := chunkStrings(items, 50)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches of <=50, got %d", len(batches))
	}
	if len(batches[0]) != 50 || len(batches[1]) != 50 || len(batches[2]) != 20 {
		t.Fatalf("unexpected batch sizes: %d %d %d", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if chunkStrings(nil, 50) != nil {
		t.Fatal("chunking an empty slice should yield nil")
	}
}

const dayInSeconds = int64(86400)

func buildRaidState(t *testing.T, entries []Entry) *State {
	t.Helper()
	state, err := BuildState(testOwner, entries, 5, 4)
	if err != nil {
		t.Fatalf("BuildState: %v", err)
	}
	return state
}

// TestRunRaid_CitizenAndElevatedSurvive: the Lord, a tree member, a follow,
// and a ward all survive a raid whose cutoff is well past their events;
// only the stranger's event is targeted for deletion.
func TestRunRaid_CitizenAndElevatedSurvive(t *testing.T) {
	entries := []Entry{
		{Verb: VerbInvite, Pubkey: "member1", InvitedBy: testOwner, Timestamp: 100},
		{Verb: VerbElevate, Pubkey: "ward1", Public: false, Timestamp: 100},
	}
	state := buildRaidState(t, entries)
	follows := []string{"follow1"}

	scanner := &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: testOwner, CreatedAt: 1},
		{Pubkey: "member1", CreatedAt: 1},
		{Pubkey: "follow1", CreatedAt: 1},
		{Pubkey: "ward1", CreatedAt: 1},
		{Pubkey: "stranger1", CreatedAt: 1},
	}}
	cli := &fakeStrfryCLI{}

	now := 100 * dayInSeconds
	result, err := RunRaid(context.Background(), scanner, cli, state, follows, 30, 30, now, false)
	if err != nil {
		t.Fatalf("RunRaid: %v", err)
	}
	if result.Events != 1 {
		t.Fatalf("expected 1 stranger event purged, got %d", result.Events)
	}
	if len(cli.calls) != 1 {
		t.Fatalf("expected exactly one delete call, got %d", len(cli.calls))
	}
	if got := cli.calls[0].Pubkeys; len(got) != 1 || got[0] != "stranger1" {
		t.Fatalf("expected delete targeting only stranger1, got %v", got)
	}
}

// TestRunRaid_YoungerThanCutoffSurvives: a stranger's event newer than the
// cutoff is never even scanned, let alone deleted.
func TestRunRaid_YoungerThanCutoffSurvives(t *testing.T) {
	state := buildRaidState(t, nil)
	now := 100 * dayInSeconds
	scanner := &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "stranger1", CreatedAt: now - 5*dayInSeconds}, // 5 days old, ttl=30 -> survives
	}}
	cli := &fakeStrfryCLI{}

	result, err := RunRaid(context.Background(), scanner, cli, state, nil, 30, 30, now, false)
	if err != nil {
		t.Fatalf("RunRaid: %v", err)
	}
	if result.Events != 0 {
		t.Fatalf("expected 0 events purged, got %d", result.Events)
	}
	if len(cli.calls) != 0 {
		t.Fatalf("expected no delete calls, got %d", len(cli.calls))
	}
}

// TestRunRaid_EvictedSurvivesGraceThenDies: an evicted member's notes
// survive while still inside OUTER_TTL_DAYS of their removal, and are
// purged once the grace window has closed.
func TestRunRaid_EvictedSurvivesGraceThenDies(t *testing.T) {
	evictedAt := int64(0)
	entries := []Entry{
		{Verb: VerbInvite, Pubkey: "member1", InvitedBy: testOwner, Timestamp: -10},
		{Verb: VerbRemove, Pubkey: "member1", Timestamp: evictedAt},
	}
	state := buildRaidState(t, entries)
	scanner := &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "member1", CreatedAt: -100 * dayInSeconds},
	}}

	// Still inside the 30-day grace window (10 days after eviction).
	cli := &fakeStrfryCLI{}
	nowInsideGrace := evictedAt + 10*dayInSeconds
	result, err := RunRaid(context.Background(), scanner, cli, state, nil, 30, 30, nowInsideGrace, false)
	if err != nil {
		t.Fatalf("RunRaid (inside grace): %v", err)
	}
	if result.Events != 0 {
		t.Fatalf("expected evicted member to survive inside grace, got %d events purged", result.Events)
	}

	// Past the 30-day grace window.
	cli2 := &fakeStrfryCLI{}
	nowAfterGrace := evictedAt + 31*dayInSeconds
	result2, err := RunRaid(context.Background(), scanner, cli2, state, nil, 30, 30, nowAfterGrace, false)
	if err != nil {
		t.Fatalf("RunRaid (after grace): %v", err)
	}
	if result2.Events != 1 {
		t.Fatalf("expected evicted member's note purged after grace, got %d", result2.Events)
	}
}

// TestRunRaid_GraceNeverFollowsOverride: a smaller per-raid ttl_days
// override (e.g. sliding to 3 days to kill a spam wave) must not shorten
// an evicted member's grace window — grace always uses graceTTLDays
// (OUTER_TTL_DAYS), independent of the override used for the scan cutoff.
func TestRunRaid_GraceNeverFollowsOverride(t *testing.T) {
	evictedAt := int64(0)
	entries := []Entry{
		{Verb: VerbInvite, Pubkey: "member1", InvitedBy: testOwner, Timestamp: -10},
		{Verb: VerbRemove, Pubkey: "member1", Timestamp: evictedAt},
	}
	state := buildRaidState(t, entries)
	// Evicted 1 day ago; well inside the standing 30-day grace window, but
	// their note is 10 days old, older than a 3-day override cutoff.
	now := evictedAt + 1*dayInSeconds
	scanner := &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "member1", CreatedAt: now - 10*dayInSeconds},
	}}
	cli := &fakeStrfryCLI{}

	result, err := RunRaid(context.Background(), scanner, cli, state, nil, 3, 30, now, false)
	if err != nil {
		t.Fatalf("RunRaid: %v", err)
	}
	if result.Events != 0 {
		t.Fatalf("evicted member inside OUTER_TTL_DAYS grace must survive a raid with a smaller override, got %d events purged", result.Events)
	}
}

// TestRunRaid_DryRunDeletesNothingReturnsNonzeroEvents: the dry-run flag
// reaches the CLI wrapper (which is trusted, per its own contract, not to
// delete when told dryRun=true) and the event count is still reported.
func TestRunRaid_DryRunDeletesNothingReturnsNonzeroEvents(t *testing.T) {
	state := buildRaidState(t, nil)
	now := 100 * dayInSeconds
	scanner := &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "stranger1", CreatedAt: 1},
		{Pubkey: "stranger2", CreatedAt: 1},
	}}
	cli := &fakeStrfryCLI{}

	result, err := RunRaid(context.Background(), scanner, cli, state, nil, 30, 30, now, true)
	if err != nil {
		t.Fatalf("RunRaid: %v", err)
	}
	if result.Events != 2 {
		t.Fatalf("expected 2 events reported, got %d", result.Events)
	}
	if len(cli.calls) != 1 || !cli.calls[0].DryRun {
		t.Fatalf("expected one dry-run delete call, got %+v", cli.calls)
	}
}

// --- Cycle.Raid: orchestration (ttl resolution, dry-run forcing, ledger) ---

func seedLedger(t *testing.T, c *Cycle, entries []Entry) {
	t.Helper()
	for _, e := range entries {
		if err := AppendLedger(c.ledgerPath(), e); err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
	}
}

func TestCycle_Raid_AbsentOverrideUsesDefault(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.OuterTTLDays = 30
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{{Pubkey: "stranger1", CreatedAt: 1}}}

	if _, err := c.Raid(context.Background(), nil, false, "test"); err != nil {
		t.Fatalf("Raid: %v", err)
	}
	entries := mustLedger(t, c)
	if len(entries) != 1 || entries[0].Verb != VerbRaidRun {
		t.Fatalf("expected one raid-run ledger entry, got %+v", entries)
	}
	if entries[0].TTLDays != 30 {
		t.Fatalf("expected default ttl_days 30 recorded, got %d", entries[0].TTLDays)
	}
}

func TestCycle_Raid_OverrideRespectedAndRecorded(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.OuterTTLDays = 30
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{{Pubkey: "stranger1", CreatedAt: 1}}}

	override := 7
	if _, err := c.Raid(context.Background(), &override, false, "test"); err != nil {
		t.Fatalf("Raid: %v", err)
	}
	entries := mustLedger(t, c)
	if len(entries) != 1 {
		t.Fatalf("expected one ledger entry, got %d", len(entries))
	}
	if entries[0].TTLDays != 7 {
		t.Fatalf("expected override ttl_days 7 recorded, got %d", entries[0].TTLDays)
	}
}

func TestCycle_Raid_ZeroAndNegativeRejected(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})

	for _, bad := range []int{0, -1, -30} {
		bad := bad
		if _, err := c.Raid(context.Background(), &bad, false, "test"); !errors.Is(err, ErrInvalidTTLDays) {
			t.Fatalf("ttl_days=%d: expected ErrInvalidTTLDays, got %v", bad, err)
		}
	}
	if entries := mustLedger(t, c); len(entries) != 0 {
		t.Fatalf("a rejected override must run nothing and append nothing, got %+v", entries)
	}
}

func TestCycle_Raid_LedgerRecordsPurgeCount(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.Now = func() time.Time { return time.Unix(100*dayInSeconds, 0) }
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "stranger1", CreatedAt: 1},
		{Pubkey: "stranger2", CreatedAt: 1},
		{Pubkey: "stranger3", CreatedAt: 1},
	}}

	result, err := c.Raid(context.Background(), nil, false, "test")
	if err != nil {
		t.Fatalf("Raid: %v", err)
	}
	if result.Events != 3 {
		t.Fatalf("expected 3 events in result, got %d", result.Events)
	}
	entries := mustLedger(t, c)
	if entries[0].Purged != 3 {
		t.Fatalf("expected ledger to record purged=3, got %d", entries[0].Purged)
	}
}

func TestCycle_Raid_DryRunRequestDoesNotDelete(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.Now = func() time.Time { return time.Unix(100*dayInSeconds, 0) }
	cli := &fakeStrfryCLI{}
	c.CLI = cli
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{{Pubkey: "stranger1", CreatedAt: 1}}}

	result, err := c.Raid(context.Background(), nil, true, "test")
	if err != nil {
		t.Fatalf("Raid: %v", err)
	}
	if result.Events != 1 {
		t.Fatalf("expected nonzero event count from dry run, got %d", result.Events)
	}
	if len(cli.calls) != 1 || !cli.calls[0].DryRun {
		t.Fatalf("expected the CLI to see dry_run=true, got %+v", cli.calls)
	}
	entries := mustLedger(t, c)
	if !entries[0].DryRun {
		t.Fatalf("expected the ledger entry to record dry_run=true")
	}
}

// TestCycle_Raid_RaidDryRunEnvForcesDryRun: RAID_DRY_RUN=true (the default
// on first deploy) must force every raid to dry-run even when the request
// itself says dry_run=false ("the armed raid itself also only dry-runs").
func TestCycle_Raid_RaidDryRunEnvForcesDryRun(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.Now = func() time.Time { return time.Unix(100*dayInSeconds, 0) }
	c.RaidDryRun = true
	cli := &fakeStrfryCLI{}
	c.CLI = cli
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{{Pubkey: "stranger1", CreatedAt: 1}}}

	if _, err := c.Raid(context.Background(), nil, false, "test"); err != nil {
		t.Fatalf("Raid: %v", err)
	}
	if len(cli.calls) != 1 || !cli.calls[0].DryRun {
		t.Fatalf("expected RAID_DRY_RUN=true to force dry_run regardless of request, got %+v", cli.calls)
	}
}

// TestCycle_Raid_UsesCurrentCitizensNotStaleFollows: a follow synced after
// the ledger was built must still be honored — Raid always re-reads
// follows.json and replays the ledger fresh rather than trusting a cached
// snapshot.
func TestCycle_Raid_UsesCurrentCitizensNotStaleFollows(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.Now = func() time.Time { return time.Unix(100*dayInSeconds, 0) }
	if err := writeJSONAtomic(c.followsPath(), FollowsSnapshot{
		Pubkeys: []string{"follow1"}, Source: "kind3-1", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "follow1", CreatedAt: 1},
		{Pubkey: "stranger1", CreatedAt: 1},
	}}

	result, err := c.Raid(context.Background(), nil, false, "test")
	if err != nil {
		t.Fatalf("Raid: %v", err)
	}
	if result.Events != 1 {
		t.Fatalf("expected the follow to survive and only the stranger purged, got %d events", result.Events)
	}
}

// --- RAID_CRON: next-run computation for stats.json ---

func TestNextRaidTime_EmptyCronIsNil(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.RaidCron = ""
	if got := c.nextRaidTime(time.Unix(1_000_000, 0)); got != nil {
		t.Fatalf("expected nil next-raid time for empty RAID_CRON, got %v", *got)
	}
}

func TestNextRaidTime_InvalidCronIsNil(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.RaidCron = "not a cron expression"
	if got := c.nextRaidTime(time.Unix(1_000_000, 0)); got != nil {
		t.Fatalf("expected nil next-raid time for invalid RAID_CRON, got %v", *got)
	}
}

func TestNextRaidTime_ComputesNextFromCron(t *testing.T) {
	c := newTestCycle(t, &fakeFetcher{})
	c.RaidCron = "0 0 * * *" // daily at midnight
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	got := c.nextRaidTime(now)
	if got == nil {
		t.Fatal("expected a next-raid time for a valid RAID_CRON")
	}
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	if *got != want {
		t.Fatalf("expected next raid at %d, got %d", want, *got)
	}
}
