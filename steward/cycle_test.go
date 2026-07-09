package main

import (
	"context"
	"errors"
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

const testOwner = "lord"

func newTestCycle(t *testing.T, fetcher NostrFetcher) *Cycle {
	t.Helper()
	return &Cycle{
		StateDir:     t.TempDir(),
		Owner:        testOwner,
		OwnRelay:     "ws://own",
		PublicRelays: []string{"ws://pub1", "ws://pub2"},
		MaxInvites:   5,
		MaxDepth:     4,
		Fetcher:      fetcher,
		Now:          func() time.Time { return time.Unix(1_000_000, 0) },
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
