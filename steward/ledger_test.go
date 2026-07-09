package main

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestAppendAndReadLedgerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	want := []Entry{
		{Verb: VerbInvite, Pubkey: "a", InvitedBy: owner, Label: "friend", Source: "req1", Timestamp: 1},
		{Verb: VerbBan, Pubkey: "b", Source: "report1", Timestamp: 2},
		{Verb: VerbElevate, Pubkey: "c", Public: true, Source: "req2", Timestamp: 3},
	}
	for _, e := range want {
		if err := AppendLedger(path, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		want[i].V = ledgerVersion
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadLedgerMissingFileIsEmpty(t *testing.T) {
	entries, err := ReadLedger(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing ledger should not error: %v", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
}

func TestReadLedgerRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	if err := AppendLedger(path, Entry{Verb: VerbBan, Pubkey: "a", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	// Hand-craft a future-version line and make sure replay refuses it
	// loudly instead of silently misinterpreting it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":2,"verb":"ban","pubkey":"b","ts":2,"source":"x"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := ReadLedger(path); err == nil {
		t.Fatal("expected an error for an unknown ledger version")
	}
}

func TestBanningTreeMemberCutsBranchAndGracePeriodsSubtreeOnly(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Invite(owner, "a", "", "src", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invite("a", "b", "", "src", 1); err != nil {
		t.Fatal(err)
	}

	if _, err := s.BanPubkey("a", "report1", 100); err != nil {
		t.Fatal(err)
	}
	if s.Tree.IsMember("a") || s.Tree.IsMember("b") {
		t.Fatal("banning a cuts a's whole branch including b")
	}
	if _, graced := s.Evicted["a"]; graced {
		t.Fatal("the banned pubkey itself must NOT be grace-eligible -- it purges immediately")
	}
	if ts, graced := s.Evicted["b"]; !graced || ts != 100 {
		t.Fatalf("b (an innocent descendant) must be grace-eligible from the ban timestamp, got ts=%d ok=%v", ts, graced)
	}
}

func TestPlainRemovalRecordsEvictionForGrace(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Invite(owner, "a", "", "src", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(owner, "a", "req", 50); err != nil {
		t.Fatal(err)
	}
	if ts, ok := s.Evicted["a"]; !ok || ts != 50 {
		t.Fatalf("Evicted[a] = %d,%v want 50,true", ts, ok)
	}
}

func TestEnnobledFollowPersistsAfterUnfollow(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Ennoble("follow1", "req", 0); err != nil {
		t.Fatal(err)
	}
	// Simulate an unfollow: follows list no longer includes follow1.
	citizens := s.Citizens(nil)
	if !contains(citizens, "follow1") {
		t.Fatal("ennobled pubkey must remain a citizen after unfollow -- tree is authoritative once ennobled")
	}
	if !s.Tree.IsMember("follow1") {
		t.Fatal("ennoble must add the pubkey to the tree")
	}
}

func TestInviteRejectsBannedTarget(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.BanPubkey("a", "src", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invite(owner, "a", "", "src", 1); !errors.Is(err, ErrTargetBanned) {
		t.Fatalf("got %v, want ErrTargetBanned", err)
	}
}

func TestBuildStateRejectsUnknownVerb(t *testing.T) {
	_, err := BuildState(owner, []Entry{{V: ledgerVersion, Verb: "not-a-real-verb", Timestamp: 0}}, 5, 4)
	if !errors.Is(err, ErrUnknownVerb) {
		t.Fatalf("got %v, want ErrUnknownVerb", err)
	}
}

// TestReplayIsDeterministic is the property test PLAN.md's Phase 2 asks
// for: ledger replay always reconstructs identical tree + elevation + ban
// state. It drives a state machine through random sequences of mutations,
// then checks that (a) replaying the successfully-applied entries from
// scratch reproduces identical state, and (b) round-tripping those entries
// through the actual on-disk ledger format changes nothing.
func TestReplayIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	pool := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	const maxInvites, maxDepth = 3, 3

	for trial := 0; trial < 30; trial++ {
		s := NewState(owner, maxInvites, maxDepth)
		var entries []Entry
		at := int64(0)
		for i := 0; i < 40; i++ {
			at++
			pk := pool[rng.Intn(len(pool))]
			other := pool[rng.Intn(len(pool))]
			var e Entry
			var err error
			switch rng.Intn(8) {
			case 0:
				inviter := owner
				if rng.Intn(2) == 0 {
					inviter = other
				}
				e, err = s.Invite(inviter, pk, "", "src", at)
			case 1:
				requester := owner
				if rng.Intn(2) == 0 {
					requester = other
				}
				e, err = s.Remove(requester, pk, "src", at)
			case 2:
				e, err = s.Ennoble(pk, "src", at)
			case 3:
				e, err = s.BanPubkey(pk, "src", at)
			case 4:
				e, err = s.PardonPubkey(pk, "src", at)
			case 5:
				e, err = s.Elevate(pk, rng.Intn(2) == 0, "src", at)
			case 6:
				e, err = s.Lower(pk, "src", at)
			case 7:
				e, err = s.BanDomain("example.com", "src", at)
			}
			if err == nil {
				entries = append(entries, e)
			}
		}

		replayed, err := BuildState(owner, entries, maxInvites, maxDepth)
		if err != nil {
			t.Fatalf("trial %d: replay from memory failed: %v", trial, err)
		}
		assertStateEqual(t, trial, "in-memory replay", s, replayed)

		dir := t.TempDir()
		path := filepath.Join(dir, "ledger.jsonl")
		for _, e := range entries {
			if err := AppendLedger(path, e); err != nil {
				t.Fatalf("trial %d: append: %v", trial, err)
			}
		}
		fromDisk, err := ReadLedger(path)
		if err != nil {
			t.Fatalf("trial %d: read: %v", trial, err)
		}
		replayedFromDisk, err := BuildState(owner, fromDisk, maxInvites, maxDepth)
		if err != nil {
			t.Fatalf("trial %d: replay from disk failed: %v", trial, err)
		}
		assertStateEqual(t, trial, "on-disk round trip", s, replayedFromDisk)
	}
}

func assertStateEqual(t *testing.T, trial int, label string, want, got *State) {
	t.Helper()
	if !reflect.DeepEqual(want.Tree.Members, got.Tree.Members) {
		t.Fatalf("trial %d (%s): tree mismatch\nwant %+v\ngot  %+v", trial, label, want.Tree.Members, got.Tree.Members)
	}
	if !reflect.DeepEqual(want.Elevation.Records, got.Elevation.Records) {
		t.Fatalf("trial %d (%s): elevation mismatch\nwant %+v\ngot  %+v", trial, label, want.Elevation.Records, got.Elevation.Records)
	}
	if !reflect.DeepEqual(want.Bans.Pubkeys, got.Bans.Pubkeys) {
		t.Fatalf("trial %d (%s): banned pubkeys mismatch\nwant %+v\ngot  %+v", trial, label, want.Bans.Pubkeys, got.Bans.Pubkeys)
	}
	if !reflect.DeepEqual(want.Bans.Domains, got.Bans.Domains) {
		t.Fatalf("trial %d (%s): banned domains mismatch\nwant %+v\ngot  %+v", trial, label, want.Bans.Domains, got.Bans.Domains)
	}
	wantC, gotC := want.Citizens(nil), got.Citizens(nil)
	sort.Strings(wantC)
	sort.Strings(gotC)
	if !reflect.DeepEqual(wantC, gotC) {
		t.Fatalf("trial %d (%s): citizens mismatch\nwant %v\ngot  %v", trial, label, wantC, gotC)
	}
}
