// The invite tree: invite/remove/ennoble, MAX_INVITES/MAX_DEPTH,
// ban-cuts-branch, tree.json as a materialized ledger replay view. Lands in
// Phase 2. See CLAUDE.md, "The invite tree (Pyramid mechanics)".
package main

import (
	"encoding/json"
	"errors"
)

// Member is one node in the invite tree (see tree.json's schema in
// CLAUDE.md). The Lord (Tree.Owner) is the implicit root and is never
// itself a Member.
type Member struct {
	InvitedBy string `json:"invited_by"`
	InvitedAt int64  `json:"invited_at"`
	Label     string `json:"label,omitempty"`
}

// Tree is a materialized view: it can always be rebuilt from ledger replay
// (see ledger.go's State/Apply/BuildState).
type Tree struct {
	Owner   string
	Members map[string]Member
}

func NewTree(owner string) *Tree {
	return &Tree{Owner: owner, Members: make(map[string]Member)}
}

var (
	ErrNotInviter    = errors.New("tree: inviter is neither the owner nor a tree member")
	ErrAlreadyMember = errors.New("tree: pubkey is already the owner or a tree member")
	ErrMaxInvites    = errors.New("tree: inviter has reached MAX_INVITES")
	ErrMaxDepth      = errors.New("tree: invite would exceed MAX_DEPTH")
	ErrNotFound      = errors.New("tree: pubkey not found")
	ErrNotOwnInvitee = errors.New("tree: requester may only remove their own invitees")
)

func (t *Tree) IsMember(pubkey string) bool {
	_, ok := t.Members[pubkey]
	return ok
}

// depth returns the owner's depth as 0, direct invitees as 1, and so on.
// Returns -1 for a pubkey that is neither the owner nor a tree member.
func (t *Tree) depth(pubkey string) int {
	if pubkey == t.Owner {
		return 0
	}
	m, ok := t.Members[pubkey]
	if !ok {
		return -1
	}
	d := t.depth(m.InvitedBy)
	if d == -1 {
		return -1
	}
	return d + 1
}

func (t *Tree) directInviteCount(pubkey string) int {
	n := 0
	for _, m := range t.Members {
		if m.InvitedBy == pubkey {
			n++
		}
	}
	return n
}

// Invite adds invitee as inviter's direct invitee, enforcing MAX_INVITES
// (inviter's direct invite count) and MAX_DEPTH (levels below the owner).
func (t *Tree) Invite(inviter, invitee, label string, at int64, maxInvites, maxDepth int) error {
	if inviter != t.Owner && !t.IsMember(inviter) {
		return ErrNotInviter
	}
	if invitee == t.Owner || t.IsMember(invitee) {
		return ErrAlreadyMember
	}
	if t.directInviteCount(inviter) >= maxInvites {
		return ErrMaxInvites
	}
	inviterDepth := t.depth(inviter)
	if inviterDepth+1 > maxDepth {
		return ErrMaxDepth
	}
	t.Members[invitee] = Member{InvitedBy: inviter, InvitedAt: at, Label: label}
	return nil
}

// subtree returns root plus every descendant of root (root need not be a
// member itself — used by removeSubtree where root is validated first).
func (t *Tree) subtree(root string) []string {
	out := []string{root}
	for _, pk := range t.directInvitees(root) {
		out = append(out, t.subtree(pk)...)
	}
	return out
}

func (t *Tree) directInvitees(pubkey string) []string {
	var out []string
	for pk, m := range t.Members {
		if m.InvitedBy == pubkey {
			out = append(out, pk)
		}
	}
	return out
}

// removeSubtree deletes target and its entire subtree structurally, with no
// permission checks — those belong to the caller (State.Remove decides who
// may remove whom; ban-cuts-branch in State.Apply always may). It returns
// every pubkey removed, target included, so the caller can record eviction
// timestamps for the grace window.
func (t *Tree) removeSubtree(target string) ([]string, error) {
	if !t.IsMember(target) {
		return nil, ErrNotFound
	}
	removed := t.subtree(target)
	for _, pk := range removed {
		delete(t.Members, pk)
	}
	return removed, nil
}

// Ennoble adds pubkey as the owner's direct invitee, uncapped by
// MAX_INVITES/MAX_DEPTH — an explicit Lord override, not a regular invite.
func (t *Tree) Ennoble(pubkey string, at int64) error {
	if pubkey == t.Owner || t.IsMember(pubkey) {
		return ErrAlreadyMember
	}
	t.Members[pubkey] = Member{InvitedBy: t.Owner, InvitedAt: at}
	return nil
}

// MarshalJSON produces tree.json's schema from CLAUDE.md:
// {"members": {"<pubkey>": {...}}}.
func (t *Tree) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Members map[string]Member `json:"members"`
	}{t.Members})
}
