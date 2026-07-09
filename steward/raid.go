// The raid: streaming scan-then-delete of the Outer Lands past
// OUTER_TTL_DAYS. The only permitted strfry-delete call site.
// See CLAUDE.md, "The Raid".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
)

// strfryCLI is the interface to strfry's CLI, reached via `docker exec`
// into STRFRY_CONTAINER. strfry delete is the only irreversible operation
// in the system; ALL delete calls go through this one wrapper, with this
// file as the only call site (CLAUDE.md's "Delete confinement"). Interfaced
// so tests can fake it without a live strfry.
type strfryCLI interface {
	// DeleteByAuthors deletes every event authored by any of pubkeys with
	// created_at <= until, batching at most deleteBatchSize per call. The
	// until bound matters: a targeted author may have posted AFTER the
	// cutoff too, and "younger than cutoff" is a keep condition per-event,
	// not per-author — omitting it here would let a stranger's fresh notes
	// get swept up just because their older notes were being purged. If
	// dryRun, it logs the batches it would run and deletes nothing. Returns
	// the number of pubkeys targeted.
	DeleteByAuthors(ctx context.Context, pubkeys []string, until int64, dryRun bool) (int, error)
}

const deleteBatchSize = 50

// strfryBinPath is where the reference dockurr/strfry image (the one
// deploy/docker-compose.yml and deploy/smoke.sh actually boot) puts the
// strfry binary: NOT on $PATH, so `docker exec <container> strfry ...`
// fails with "executable file not found" against a real container. Every
// `docker exec` invocation of strfry (here and in stats.go's scanner) uses
// this absolute path instead of bare "strfry".
const strfryBinPath = "/app/strfry"

// dockerStrfryCLI is the real strfryCLI, shelling out to
// `docker exec <container> strfry delete --filter ...`.
type dockerStrfryCLI struct {
	Container string
}

func (d *dockerStrfryCLI) DeleteByAuthors(ctx context.Context, pubkeys []string, until int64, dryRun bool) (int, error) {
	for _, batch := range chunkStrings(pubkeys, deleteBatchSize) {
		filter, err := json.Marshal(map[string]any{"authors": batch, "until": until})
		if err != nil {
			return 0, err
		}
		if dryRun {
			fmt.Fprintf(os.Stderr, "steward: [dry-run] would delete %d authors: %s\n", len(batch), filter)
			continue
		}
		cmd := exec.CommandContext(ctx, "docker", "exec", d.Container, strfryBinPath, "delete", "--filter", string(filter))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("strfry delete: %w: %s", err, out)
		}
		fmt.Fprintf(os.Stderr, "steward: deleted %d authors' events\n", len(batch))
	}
	return len(pubkeys), nil
}

// --- The raid itself ---

// ErrInvalidTTLDays is returned when a per-raid ttl_days override is
// present but not a positive integer. CLAUDE.md: "Clamp: reject 0,
// negative, or non-integer with 400, nothing runs."
var ErrInvalidTTLDays = errors.New("raid: ttl_days must be >= 1")

// RaidResult is what one raid execution found: the count of stranger
// events purged (or, for a dry run, that would be purged) — exactly the
// `{events}` shape CLAUDE.md's dry-run preview returns.
type RaidResult struct {
	Events int `json:"events"`
}

// RunRaid scans every event with created_at <= (now - ttlDays days) and
// deletes the ones authored by a stranger — i.e. not a current citizen and
// not inside an eviction's grace window. The grace window ALWAYS uses
// graceTTLDays (== OUTER_TTL_DAYS), independent of ttlDays: a smaller
// per-raid override is aimed at aging out old stranger notes faster, never
// at shortening someone's eviction grace (CLAUDE.md's grace-decoupling
// rule). Streams the scan and batches deletes; never slurps.
func RunRaid(ctx context.Context, scanner strfryScanner, cli strfryCLI, state *State, follows []string, ttlDays, graceTTLDays int, now int64, dryRun bool) (RaidResult, error) {
	citizens := make(map[string]bool)
	for _, pk := range state.Citizens(follows) {
		citizens[pk] = true
	}
	graceSeconds := int64(graceTTLDays) * 86400
	inGrace := func(pk string) bool {
		evictedAt, ok := state.Evicted[pk]
		return ok && now-evictedAt < graceSeconds
	}

	cutoff := now - int64(ttlDays)*86400

	toDelete := make(map[string]bool)
	events := 0
	err := scanner.ScanUntil(ctx, cutoff, func(pubkey string, createdAt int64) {
		if citizens[pubkey] || inGrace(pubkey) {
			return
		}
		toDelete[pubkey] = true
		events++
	})
	if err != nil {
		return RaidResult{}, fmt.Errorf("raid: scan: %w", err)
	}

	if len(toDelete) > 0 {
		pubkeys := make([]string, 0, len(toDelete))
		for pk := range toDelete {
			pubkeys = append(pubkeys, pk)
		}
		sort.Strings(pubkeys)
		if _, err := cli.DeleteByAuthors(ctx, pubkeys, cutoff, dryRun); err != nil {
			return RaidResult{}, fmt.Errorf("raid: delete: %w", err)
		}
	}

	return RaidResult{Events: events}, nil
}

// Raid is the manual-trigger hook: the single entry point Phase 5's
// POST /api/raid calls, and what a RAID_CRON firing calls too. It resolves
// the effective ttl_days (override or OuterTTLDays) and the effective
// dry-run flag (RAID_DRY_RUN=true forces every raid to dry-run regardless
// of the request, per CLAUDE.md: "the armed raid itself also only
// dry-runs"), runs RunRaid against the CURRENT ledger-replayed state and
// follows snapshot, and appends the raid-run ledger line immediately.
func (c *Cycle) Raid(ctx context.Context, ttlDaysOverride *int, dryRun bool, source string) (RaidResult, error) {
	ttlDays := c.OuterTTLDays
	if ttlDaysOverride != nil {
		if *ttlDaysOverride < 1 {
			return RaidResult{}, ErrInvalidTTLDays
		}
		ttlDays = *ttlDaysOverride
	}
	effectiveDryRun := dryRun || c.RaidDryRun

	entries, err := ReadLedger(c.ledgerPath())
	if err != nil {
		return RaidResult{}, fmt.Errorf("raid: read ledger: %w", err)
	}
	state, err := BuildState(c.Owner, entries, c.MaxInvites, c.MaxDepth)
	if err != nil {
		return RaidResult{}, fmt.Errorf("raid: build state: %w", err)
	}
	follows, err := readFollows(c.followsPath())
	if err != nil {
		return RaidResult{}, fmt.Errorf("raid: read follows: %w", err)
	}

	now := c.Now().Unix()
	result, err := RunRaid(ctx, c.Scanner, c.CLI, state, follows.Pubkeys, ttlDays, c.OuterTTLDays, now, effectiveDryRun)
	if err != nil {
		return RaidResult{}, err
	}

	entry, err := state.RecordRaidRun(result.Events, ttlDays, effectiveDryRun, source, now)
	if err != nil {
		return RaidResult{}, fmt.Errorf("raid: record ledger entry: %w", err)
	}
	if err := AppendLedger(c.ledgerPath(), entry); err != nil {
		return RaidResult{}, fmt.Errorf("raid: append ledger: %w", err)
	}

	return result, nil
}

// --- RAID_CRON scheduling ---

// parseRaidCron parses RAID_CRON as a standard 5-field cron expression
// (minute hour day month weekday). An empty spec means "manual raids
// only" — CLAUDE.md's default — and is not an error.
func parseRaidCron(spec string) (cron.Schedule, error) {
	if spec == "" {
		return nil, nil
	}
	return cron.ParseStandard(spec)
}

// nextRaidTime is stats.json's raids.next: null when RAID_CRON is empty or
// unparseable, otherwise the next scheduled firing after now.
func (c *Cycle) nextRaidTime(now time.Time) *int64 {
	sched, err := parseRaidCron(c.RaidCron)
	if err != nil || sched == nil {
		return nil
	}
	next := sched.Next(now).Unix()
	return &next
}

func chunkStrings(items []string, size int) [][]string {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}
