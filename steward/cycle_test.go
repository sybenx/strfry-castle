package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// fakeFetcher is a NostrFetcher test double: every source of network data
// is a field the test controls directly, so cycle tests never touch a real
// relay.
type fakeFetcher struct {
	kind3ByRelay map[string]*nostr.Event
	kind3Err     error
	kind0ByRelay map[string]map[string]*nostr.Event
	kind0Err     error
}

func (f *fakeFetcher) LatestKind3(ctx context.Context, relayURLs []string, pubkey string) (*nostr.Event, error) {
	if f.kind3Err != nil {
		return nil, f.kind3Err
	}
	var newest *nostr.Event
	for _, url := range relayURLs {
		ev, ok := f.kind3ByRelay[url]
		if !ok {
			continue
		}
		if newest == nil || ev.CreatedAt > newest.CreatedAt {
			newest = ev
		}
	}
	return newest, nil
}

// kind0ByRelay is relay URL -> pubkey -> event, so tests can exercise the
// "own relay first, PUBLIC_RELAYS fallback" order explicitly.
func (f *fakeFetcher) LatestKind0s(ctx context.Context, relayURLs []string, pubkeys []string) (map[string]*nostr.Event, error) {
	if f.kind0Err != nil {
		return nil, f.kind0Err
	}
	result := make(map[string]*nostr.Event)
	remaining := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		remaining[pk] = true
	}
	for _, url := range relayURLs {
		for pk := range remaining {
			if ev, ok := f.kind0ByRelay[url][pk]; ok {
				result[pk] = ev
				delete(remaining, pk)
			}
		}
	}
	return result, nil
}

// fakeScanner is a strfryScanner test double: a fixed in-memory event list,
// so stats tests never touch a real strfry.
type fakeScanner struct {
	events []fakeStoredEvent
}

type fakeStoredEvent struct {
	Pubkey    string
	Kind      int
	CreatedAt int64
}

func (f *fakeScanner) Count(ctx context.Context, filter map[string]any) (int, error) {
	authors, ok := filter["authors"].([]string)
	if !ok {
		return len(f.events), nil
	}
	set := make(map[string]bool, len(authors))
	for _, a := range authors {
		set[a] = true
	}
	n := 0
	for _, e := range f.events {
		if set[e.Pubkey] {
			n++
		}
	}
	return n, nil
}

func (f *fakeScanner) ScanAll(ctx context.Context, fn func(pubkey string, kind int, createdAt int64)) error {
	for _, e := range f.events {
		fn(e.Pubkey, e.Kind, e.CreatedAt)
	}
	return nil
}

func (f *fakeScanner) ScanUntil(ctx context.Context, until int64, fn func(pubkey string, createdAt int64)) error {
	for _, e := range f.events {
		if e.CreatedAt <= until {
			fn(e.Pubkey, e.CreatedAt)
		}
	}
	return nil
}

// fakeStrfryCLI is a strfryCLI test double recording every delete call it
// receives, so raid tests can assert exactly what would be (or was) deleted
// without a live strfry.
type fakeStrfryCLI struct {
	calls []fakeDeleteCall
	err   error
}

type fakeDeleteCall struct {
	Pubkeys []string
	Until   int64
	DryRun  bool
}

func (f *fakeStrfryCLI) DeleteByAuthors(ctx context.Context, pubkeys []string, until int64, dryRun bool) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	cp := append([]string(nil), pubkeys...)
	f.calls = append(f.calls, fakeDeleteCall{Pubkeys: cp, Until: until, DryRun: dryRun})
	return len(pubkeys), nil
}

// fakeReleaseChecker is a ReleaseChecker test double.
type fakeReleaseChecker struct {
	latest string
	err    error
}

func (f *fakeReleaseChecker) LatestRelease(ctx context.Context) (string, error) {
	return f.latest, f.err
}

const testOwner = "lord"

func newTestCycle(t *testing.T, fetcher NostrFetcher) *Cycle {
	t.Helper()
	return &Cycle{
		StateDir:       t.TempDir(),
		Owner:          testOwner,
		OwnRelay:       "ws://own",
		PublicRelays:   []string{"ws://pub1", "ws://pub2"},
		MaxInvites:     5,
		MaxDepth:       4,
		OuterTTLDays:   30,
		RunningVersion: "test",
		Fetcher:        fetcher,
		Scanner:        &fakeScanner{},
		CLI:            &fakeStrfryCLI{},
		ReleaseChecker: &fakeReleaseChecker{},
		Now:            func() time.Time { return time.Unix(1_000_000, 0) },
	}
}

func mustLedger(t *testing.T, c *Cycle) []Entry {
	t.Helper()
	entries, err := ReadLedger(c.ledgerPath())
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	return entries
}

func TestCycle_FollowsNeverShrinkOnFetchError(t *testing.T) {
	fetcher := &fakeFetcher{kind3Err: errors.New("relay unreachable")}
	c := newTestCycle(t, fetcher)

	if err := writeJSONAtomic(c.followsPath(), FollowsSnapshot{
		Pubkeys: []string{"follow1", "follow2"}, Source: "old-kind3", CreatedAt: 500,
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := readFollows(c.followsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pubkeys) != 2 || after.CreatedAt != 500 {
		t.Fatalf("follows.json must survive a fetch failure unchanged, got %+v", after)
	}
}

func TestCycle_FollowsAreCitizens(t *testing.T) {
	fetcher := &fakeFetcher{}
	c := newTestCycle(t, fetcher)

	if err := writeJSONAtomic(c.followsPath(), FollowsSnapshot{
		Pubkeys: []string{"follow1"}, Source: "kind3-a", CreatedAt: 500,
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	entries := mustLedger(t, c)
	state, err := BuildState(c.Owner, entries, c.MaxInvites, c.MaxDepth)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pk := range state.Citizens([]string{"follow1"}) {
		if pk == "follow1" {
			found = true
		}
	}
	if !found {
		t.Fatal("a synced follow must appear in the effective citizenry")
	}
}

// --- follow-list shrink safety: departures grace, empty kind-3, corrupt state ---

func kind3(id string, createdAt int64, follows ...string) *nostr.Event {
	ev := &nostr.Event{ID: id, Kind: 3, CreatedAt: nostr.Timestamp(createdAt)}
	for _, pk := range follows {
		ev.Tags = append(ev.Tags, nostr.Tag{"p", pk})
	}
	return ev
}

func mustDepartures(t *testing.T, c *Cycle) map[string]int64 {
	t.Helper()
	d, err := readDepartures(c.departuresPath())
	if err != nil {
		t.Fatalf("readDepartures: %v", err)
	}
	return d
}

// followRaidFixture: the Lord follows f1 and f2 (snapshot from created_at
// 500), both with an old note that any stranger would lose at a raid.
func followRaidFixture(t *testing.T) (*Cycle, *fakeFetcher, *int64, *fakeStrfryCLI) {
	t.Helper()
	fetcher := &fakeFetcher{kind3ByRelay: map[string]*nostr.Event{}}
	c := newTestCycle(t, fetcher)
	now := 100 * dayInSeconds
	c.Now = func() time.Time { return time.Unix(now, 0) }
	cli := &fakeStrfryCLI{}
	c.CLI = cli
	c.Scanner = &fakeScanner{events: []fakeStoredEvent{
		{Pubkey: "f1", CreatedAt: 1},
		{Pubkey: "f2", CreatedAt: 1},
	}}
	if err := writeJSONAtomic(c.followsPath(), FollowsSnapshot{
		Pubkeys: []string{"f1", "f2"}, Source: "kind3-old", CreatedAt: 500,
	}); err != nil {
		t.Fatal(err)
	}
	return c, fetcher, &now, cli
}

func deletedPubkeys(cli *fakeStrfryCLI) []string {
	var out []string
	for _, call := range cli.calls {
		out = append(out, call.Pubkeys...)
	}
	return out
}

// TestCycle_UnfollowGetsGraceThenPurge: a pubkey dropped from the follow
// list survives raids for OUTER_TTL_DAYS after steward saw it drop, across
// a restart, and is purged only after that window closes. The departure is
// stamped with steward's clock, not the kind-3's (very old) created_at, so
// an old event cannot backdate the window shut.
func TestCycle_UnfollowGetsGraceThenPurge(t *testing.T) {
	c, fetcher, now, cli := followRaidFixture(t)
	departedAt := *now
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-new", 600, "f1")

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := mustDepartures(t, c); len(got) != 1 || got["f2"] != departedAt {
		t.Fatalf("departures = %v, want f2 stamped at steward time %d (not kind-3 created_at 600)", got, departedAt)
	}

	// A fresh Cycle on the same state dir stands in for a steward restart:
	// grace must come from disk, not memory.
	restarted := newTestCycle(t, fetcher)
	restarted.StateDir = c.StateDir
	restarted.CLI = cli
	restarted.Scanner = c.Scanner
	restarted.Now = c.Now

	*now = departedAt + 10*dayInSeconds
	result, err := restarted.Raid(context.Background(), nil, false, "test")
	if err != nil {
		t.Fatalf("Raid inside grace: %v", err)
	}
	if result.Events != 0 || len(cli.calls) != 0 {
		t.Fatalf("unfollowed pubkey inside grace must survive, got %d events, deletes %v", result.Events, deletedPubkeys(cli))
	}

	*now = departedAt + 31*dayInSeconds
	result, err = restarted.Raid(context.Background(), nil, false, "test")
	if err != nil {
		t.Fatalf("Raid after grace: %v", err)
	}
	if got := deletedPubkeys(cli); result.Events != 1 || len(got) != 1 || got[0] != "f2" {
		t.Fatalf("after grace only f2 should be purged, got %d events, deletes %v", result.Events, got)
	}
}

// TestCycle_RefollowCancelsDeparture: re-following inside the window clears
// the departure, and a later unfollow starts a fresh window rather than
// inheriting the old stamp.
func TestCycle_RefollowCancelsDeparture(t *testing.T) {
	c, fetcher, now, _ := followRaidFixture(t)
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-drop", 600, "f1")
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run (unfollow): %v", err)
	}

	*now += 5 * dayInSeconds
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-back", 700, "f1", "f2")
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run (re-follow): %v", err)
	}
	if got := mustDepartures(t, c); len(got) != 0 {
		t.Fatalf("re-follow must cancel the departure, departures = %v", got)
	}

	*now += 20 * dayInSeconds
	secondDeparture := *now
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-drop2", 800, "f1")
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run (unfollow again): %v", err)
	}
	if got := mustDepartures(t, c); got["f2"] != secondDeparture {
		t.Fatalf("second unfollow must start a fresh window at %d, got %v", secondDeparture, got)
	}
}

// TestCycle_EmptyKind3NeverReplacesFollows: a newer kind-3 with no p tags
// (a client publishing from a cold cache) is not applied. follows.json and
// departures.json stay as they were, and a raid deletes nothing of the
// follows, no matter how much time passes.
func TestCycle_EmptyKind3NeverReplacesFollows(t *testing.T) {
	c, fetcher, now, cli := followRaidFixture(t)
	fetcher.kind3ByRelay["ws://pub1"] = kind3("kind3-empty", 600)

	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, err := readFollows(c.followsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pubkeys) != 2 || after.Source != "kind3-old" || after.CreatedAt != 500 {
		t.Fatalf("an empty kind-3 must not replace follows.json, got %+v", after)
	}
	if got := mustDepartures(t, c); len(got) != 0 {
		t.Fatalf("an empty kind-3 must record no departures, got %v", got)
	}

	*now += 365 * dayInSeconds
	result, err := c.Raid(context.Background(), nil, false, "test")
	if err != nil {
		t.Fatalf("Raid: %v", err)
	}
	if result.Events != 0 || len(cli.calls) != 0 {
		t.Fatalf("follows must survive after an empty kind-3, got %d events, deletes %v", result.Events, deletedPubkeys(cli))
	}
}

// TestCycle_EmptyKind3AcceptedWhenNothingToLose: with no previous follows,
// an empty kind-3 is just the truth and is recorded normally.
func TestCycle_EmptyKind3AcceptedWhenNothingToLose(t *testing.T) {
	fetcher := &fakeFetcher{kind3ByRelay: map[string]*nostr.Event{"ws://own": kind3("kind3-empty", 600)}}
	c := newTestCycle(t, fetcher)
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, err := readFollows(c.followsPath())
	if err != nil {
		t.Fatal(err)
	}
	if after.Source != "kind3-empty" || len(after.Pubkeys) != 0 {
		t.Fatalf("expected the empty kind-3 recorded on a fresh install, got %+v", after)
	}
}

// TestCycle_CorruptFollowsFailsSafe: an unparseable follows.json halts the
// cycle (citizens.json is not rewritten, the corrupt file is left for a
// human, even when a newer kind-3 is available) and the raid refuses to
// run, deleting nothing and appending nothing.
func TestCycle_CorruptFollowsFailsSafe(t *testing.T) {
	c, fetcher, _, cli := followRaidFixture(t)
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-new", 600, "f1")
	corrupt := []byte(`{"pubkeys": ["f1", "f2"`)
	if err := os.WriteFile(c.followsPath(), corrupt, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.Run(context.Background()); err == nil {
		t.Fatal("Run must fail on a corrupt follows.json, not continue with an empty follow list")
	}
	if _, err := os.Stat(c.citizensPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("citizens.json must not be rewritten from an empty follow list, stat err = %v", err)
	}
	if data, _ := os.ReadFile(c.followsPath()); string(data) != string(corrupt) {
		t.Fatalf("the corrupt follows.json must be left untouched for inspection, got %q", data)
	}

	if _, err := c.Raid(context.Background(), nil, false, "test"); err == nil {
		t.Fatal("Raid must refuse to run on a corrupt follows.json")
	}
	if len(cli.calls) != 0 {
		t.Fatalf("no delete may run, got %v", deletedPubkeys(cli))
	}
	if entries := mustLedger(t, c); len(entries) != 0 {
		t.Fatalf("a refused raid must append nothing, got %+v", entries)
	}
}

// TestCycle_CorruptDeparturesFailsSafe: an unreadable grace record must not
// read as "nobody is in grace". Cycle and raid both refuse.
func TestCycle_CorruptDeparturesFailsSafe(t *testing.T) {
	c, fetcher, _, cli := followRaidFixture(t)
	fetcher.kind3ByRelay["ws://own"] = kind3("kind3-new", 600, "f1")
	if err := os.WriteFile(c.departuresPath(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.Run(context.Background()); err == nil {
		t.Fatal("Run must fail on a corrupt departures.json")
	}
	after, err := readFollows(c.followsPath())
	if err != nil {
		t.Fatal(err)
	}
	if after.Source != "kind3-old" {
		t.Fatalf("follows.json must not advance while departures.json is unreadable, got %+v", after)
	}
	if _, err := c.Raid(context.Background(), nil, false, "test"); err == nil {
		t.Fatal("Raid must refuse to run on a corrupt departures.json")
	}
	if len(cli.calls) != 0 {
		t.Fatalf("no delete may run, got %v", deletedPubkeys(cli))
	}
}
