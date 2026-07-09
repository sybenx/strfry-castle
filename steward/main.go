// Command steward is the Castle's sidecar daemon: follows sync, report
// intake, raids, the invite tree, elevation, manual archival, the signed
// HTTP API, NIP-05 serving, and stats. It also serves towncrier's static
// files, so there is no separate web container. See CLAUDE.md, Component 2.
//
// This is a Phase 0 stub. It parses env config and exits; the cycle loop,
// ledger, tree, elevation, HTTP API, raid, and scribe land in Phases 2-5.
package main

import (
	"fmt"
	"os"
)

// config holds the env-driven settings from CLAUDE.md, Component 2.
type config struct {
	OwnerPubkey     string
	StrfryContainer string
	PublicRelays    []string
	OuterTTLDays    int
	CycleMinutes    int
	RaidCron        string
	RaidDryRun      bool
	MaxInvites      int
	MaxDepth        int
	Nip05Domain     string
	Listen          string
}

func loadConfig() (config, error) {
	return config{
		OwnerPubkey:     os.Getenv("OWNER_PUBKEY"),
		StrfryContainer: os.Getenv("STRFRY_CONTAINER"),
		OuterTTLDays:    30,
		CycleMinutes:    10,
		RaidCron:        os.Getenv("RAID_CRON"),
		RaidDryRun:      true,
		MaxInvites:      5,
		MaxDepth:        4,
		Nip05Domain:     os.Getenv("NIP05_DOMAIN"),
		Listen:          envOr("LISTEN", ":8787"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "steward: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("steward: stub build, listen=%s owner=%s\n", cfg.Listen, cfg.OwnerPubkey)
}
