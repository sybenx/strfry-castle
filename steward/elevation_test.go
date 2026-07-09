package main

import "testing"

func TestElevatedNonMemberIsCitizen(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Elevate("stranger", true, "src", 0); err != nil {
		t.Fatal(err)
	}
	citizens := s.Citizens(nil)
	if !contains(citizens, "stranger") {
		t.Fatalf("citizens = %v, want stranger included", citizens)
	}
}

func TestCutBranchFavoriteKeepsRetentionLosesInviteRights(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Invite(owner, "a", "", "src", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Elevate("a", true, "src", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(owner, "a", "src", 2); err != nil {
		t.Fatal(err)
	}
	if !s.Elevation.IsFavorite("a") {
		t.Fatal("favorite status must survive branch cut (elevation is tree-independent)")
	}
	if !contains(s.Citizens(nil), "a") {
		t.Fatal("elevated pubkey must remain a citizen after losing tree membership")
	}
	// Lost invite rights: "a" is no longer a tree member, so it can't invite.
	if err := s.Tree.Invite("a", "b", "", 3, 5, 4); err == nil {
		t.Fatal("cut-branch favorite must lose invite rights")
	}
}

func TestVisibilityFlipIsOneLedgerLine(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Elevate("a", false, "src1", 0); err != nil {
		t.Fatal(err)
	}
	e, err := s.Elevate("a", true, "src2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.Verb != VerbFlipVisibility {
		t.Fatalf("verb = %q, want flip-visibility", e.Verb)
	}
	if !s.Elevation.IsFavorite("a") {
		t.Fatal("visibility should now be public (favorite)")
	}
}

func TestElevateSameVisibilityIsNoChange(t *testing.T) {
	s := NewState(owner, 5, 4)
	if _, err := s.Elevate("a", true, "src", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Elevate("a", true, "src", 1); err != ErrNoChange {
		t.Fatalf("got %v, want ErrNoChange", err)
	}
}

func TestWardsAbsentFromCitizensVisibilityInfo(t *testing.T) {
	// citizens.json (via CitizensJSON) must carry no visibility info at
	// all -- this test pins that Citizens has no such field by
	// construction (a single []string), and that wards are still present
	// (they must retain citizenship, invisibly).
	s := NewState(owner, 5, 4)
	if _, err := s.Elevate("ward1", false, "src", 0); err != nil {
		t.Fatal(err)
	}
	cj := s.CitizensJSON(nil)
	if !contains(cj.Pubkeys, "ward1") {
		t.Fatal("ward must retain citizenship in citizens.json")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
