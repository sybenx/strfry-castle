// Command steward is the Castle's sidecar daemon: follows sync, report
// intake, raids, the invite tree, elevation, manual archival, the signed
// HTTP API, NIP-05 serving, and stats. It also serves towncrier's static
// files, so there is no separate web container. See CLAUDE.md, Component 2.
//
// This is a Phase 0 stub. It parses env config and exits; the cycle loop,
// ledger, tree, elevation, HTTP API, raid, and scribe land in Phases 2-5.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// writeJSONAtomic marshals v and writes it to path via a temp file in the
// same directory followed by a rename, so a crash mid-write never leaves a
// truncated state file for gatekeeper (or another reader) to hot-reload.
func writeJSONAtomic(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "steward: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("steward: stub build, listen=%s owner=%s\n", cfg.Listen, cfg.OwnerPubkey)
}
