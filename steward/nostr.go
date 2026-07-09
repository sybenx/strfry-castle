// Nostr client plumbing built on github.com/nbd-wtf/go-nostr: the
// follows-sync fetch. Lands in Phase 3a. See CLAUDE.md, "Cycle loop (every
// CYCLE_MINUTES)".
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// relayQueryTimeout bounds each individual relay round-trip so one
// unreachable relay can't stall a whole cycle.
const relayQueryTimeout = 10 * time.Second

// NostrFetcher is every network read the cycle depends on. Interfaced so
// cycle tests can fake it without a live relay; relayFetcher below is the
// real go-nostr-backed implementation.
type NostrFetcher interface {
	// LatestKind3 returns the newest kind-3 event authored by pubkey across
	// relayURLs (CLAUDE.md's follows sync: "own relay + PUBLIC_RELAYS,
	// newest wins"), or nil if none of them have one. An unreachable relay
	// is logged and skipped, never allowed to sink the others; "on fetch
	// failure keep previous" is the caller's job, so this never returns an
	// error for anything short of a bad pubkey.
	LatestKind3(ctx context.Context, relayURLs []string, pubkey string) (*nostr.Event, error)
}

// relayFetcher is the real NostrFetcher, built on go-nostr's per-relay
// QuerySync. It holds no state of its own: every call connects, queries,
// and disconnects, so a dead relay never wedges a later cycle.
type relayFetcher struct{}

func (relayFetcher) LatestKind3(ctx context.Context, relayURLs []string, pubkey string) (*nostr.Event, error) {
	filter := nostr.Filter{Kinds: []int{3}, Authors: []string{pubkey}, Limit: 1}
	var newest *nostr.Event
	for _, url := range relayURLs {
		events, err := queryRelay(ctx, url, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "steward: kind-3 fetch from %s: %v\n", url, err)
			continue
		}
		for _, ev := range events {
			if newest == nil || ev.CreatedAt > newest.CreatedAt {
				newest = ev
			}
		}
	}
	return newest, nil
}

// queryRelay connects to url, runs filter via QuerySync, and disconnects.
// Bounded by relayQueryTimeout so one slow relay can't stall a cycle.
func queryRelay(ctx context.Context, url string, filter nostr.Filter) ([]*nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, relayQueryTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer relay.Close()

	return relay.QuerySync(ctx, filter)
}
