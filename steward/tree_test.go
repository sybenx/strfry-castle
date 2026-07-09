package main

import (
	"errors"
	"testing"
)

const owner = "lord"

func TestTreeInviteRespectsMaxInvites(t *testing.T) {
	tr := NewTree(owner)
	for i, pk := range []string{"a", "b", "c"} {
		if err := tr.Invite(owner, pk, "", int64(i), 3, 4); err != nil {
			t.Fatalf("invite %s: %v", pk, err)
		}
	}
	if err := tr.Invite(owner, "d", "", 4, 3, 4); !errors.Is(err, ErrMaxInvites) {
		t.Fatalf("4th invite: got %v, want ErrMaxInvites", err)
	}
}

func TestTreeInviteRespectsMaxDepth(t *testing.T) {
	tr := NewTree(owner)
	chain := []string{"a", "b", "c", "d"} // depths 1..4 with maxDepth=4
	prev := owner
	for i, pk := range chain {
		if err := tr.Invite(prev, pk, "", int64(i), 5, 4); err != nil {
			t.Fatalf("invite %s at depth %d: %v", pk, i+1, err)
		}
		prev = pk
	}
	// prev is now "d" at depth 4; one more level would be depth 5 > maxDepth 4.
	if err := tr.Invite(prev, "e", "", 5, 5, 4); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("depth-5 invite: got %v, want ErrMaxDepth", err)
	}
}

func TestTreeInviteRejectsNonInviter(t *testing.T) {
	tr := NewTree(owner)
	if err := tr.Invite("stranger", "a", "", 0, 5, 4); !errors.Is(err, ErrNotInviter) {
		t.Fatalf("got %v, want ErrNotInviter", err)
	}
}

func TestTreeInviteRejectsAlreadyMember(t *testing.T) {
	tr := NewTree(owner)
	mustInvite(t, tr, owner, "a", 0)
	if err := tr.Invite(owner, "a", "", 1, 5, 4); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("got %v, want ErrAlreadyMember", err)
	}
	if err := tr.Invite(owner, owner, "", 1, 5, 4); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("inviting owner: got %v, want ErrAlreadyMember", err)
	}
}

func TestTreeRemoveMemberCannotRemoveNonInvitee(t *testing.T) {
	tr := NewTree(owner)
	mustInvite(t, tr, owner, "a", 0)
	mustInvite(t, tr, owner, "b", 1)
	// "a" did not invite "b", so "a" removing "b" must fail.
	if _, err := removeAsRequester(tr, "a", "b", false); !errors.Is(err, ErrNotOwnInvitee) {
		t.Fatalf("got %v, want ErrNotOwnInvitee", err)
	}
}

func TestTreeLordRemovesAnyone(t *testing.T) {
	tr := NewTree(owner)
	mustInvite(t, tr, owner, "a", 0)
	mustInvite(t, tr, "a", "b", 1)
	removed, err := removeAsRequester(tr, owner, "b", true)
	if err != nil {
		t.Fatalf("lord removing grandchild invitee: %v", err)
	}
	if len(removed) != 1 || removed[0] != "b" {
		t.Fatalf("removed = %v, want [b]", removed)
	}
}

func TestTreeRemoveCutsWholeSubtree(t *testing.T) {
	tr := NewTree(owner)
	mustInvite(t, tr, owner, "a", 0)
	mustInvite(t, tr, "a", "b", 1)
	mustInvite(t, tr, "a", "c", 2)
	mustInvite(t, tr, "b", "d", 3)
	mustInvite(t, tr, owner, "z", 4) // unrelated branch, must survive

	removed, err := removeAsRequester(tr, owner, "a", true)
	if err != nil {
		t.Fatal(err)
	}
	wantRemoved := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if len(removed) != len(wantRemoved) {
		t.Fatalf("removed = %v, want exactly %v", removed, wantRemoved)
	}
	for _, pk := range removed {
		if !wantRemoved[pk] {
			t.Fatalf("unexpected pubkey %q removed", pk)
		}
	}
	for pk := range wantRemoved {
		if tr.IsMember(pk) {
			t.Fatalf("%q should have been cut from the tree", pk)
		}
	}
	if !tr.IsMember("z") {
		t.Fatal("unrelated branch \"z\" must survive a sibling's removal")
	}
}

func TestTreeEnnobleAddsAsDirectInvitee(t *testing.T) {
	tr := NewTree(owner)
	if err := tr.Ennoble("follow1", 10); err != nil {
		t.Fatal(err)
	}
	m, ok := tr.Members["follow1"]
	if !ok {
		t.Fatal("ennobled pubkey missing from tree")
	}
	if m.InvitedBy != owner {
		t.Fatalf("InvitedBy = %q, want owner", m.InvitedBy)
	}
}

func TestTreeRemoveNotFound(t *testing.T) {
	tr := NewTree(owner)
	if _, err := tr.removeSubtree("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// --- test helpers ---

func mustInvite(t *testing.T, tr *Tree, inviter, invitee string, at int64) {
	t.Helper()
	if err := tr.Invite(inviter, invitee, "", at, 100, 100); err != nil {
		t.Fatalf("invite %s -> %s: %v", inviter, invitee, err)
	}
}

// removeAsRequester mirrors State.Remove's permission check without going
// through State, so tree.go's rules can be unit-tested in isolation.
func removeAsRequester(tr *Tree, requester, target string, isOwner bool) ([]string, error) {
	m, ok := tr.Members[target]
	if !ok {
		return nil, ErrNotFound
	}
	if !isOwner && m.InvitedBy != requester {
		return nil, ErrNotOwnInvitee
	}
	return tr.removeSubtree(target)
}
