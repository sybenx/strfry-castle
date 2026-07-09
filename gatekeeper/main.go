// Command gatekeeper is strfry's write-policy plugin for the Castle. It
// reads newline-delimited JSON on stdin (strfry's plugin protocol) and
// writes an accept/reject decision per line on stdout. Stdlib only, plus
// the shared internal/stateformat package — see CLAUDE.md, Component 1.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sybenx/castle-for-strfry-experiment/internal/stateformat"
)

// Tier order, first match wins (CLAUDE.md's tier table): banned authors are
// rejected outright; kind 1059/9735 events addressed to a citizen (Castle
// Mail, judged by recipient) are accepted but always ride the mail bucket;
// citizen-authored events are accepted and exempt from both buckets;
// anything else is a stranger in the Outer Lands, throttled by the lands
// bucket (disabled by default — a firehose). Ephemeral kinds get no special
// treatment here — they fall through to the same citizen/stranger split as
// any other kind (see DECISIONS.md).
const (
	// defaultMailRatePerMinute/defaultLandsRatePerMinute back MAIL_RATE_PER_MIN
	// and LANDS_RATE_PER_MIN (env-configurable, CLAUDE.md's gatekeeper
	// section). Mail is the one lane where a stranger earns permanent
	// storage, so it is always throttled; the outer lands default to
	// unlimited (0) — the raid is the moderation, not a prefilter.
	defaultMailRatePerMinute  = 10
	defaultLandsRatePerMinute = 0
	bucketIdleTTL             = 10 * time.Minute
	bucketSweepInterval       = time.Minute
	defaultPollInterval       = time.Second

	msgBanned         = "blocked: you have been exiled from these lands"
	msgLandsRateLimit = "rate-limited: the outer lands are crowded"
	msgMailRateLimit  = "rate-limited: the lord's courier is overburdened"
)

type pluginRequest struct {
	Type   string          `json:"type"`
	Event  json.RawMessage `json:"event"`
	Source string          `json:"sourceInfo"`
}

type pluginEvent struct {
	ID     string     `json:"id"`
	PubKey string     `json:"pubkey"`
	Kind   int        `json:"kind"`
	Tags   [][]string `json:"tags"`
}

type pluginResponse struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Msg    string `json:"msg,omitempty"`
}

// stateDir is where the shared castle-state volume is mounted inside the
// strfry container (deploy/docker-compose.yml). Fixed, not configurable:
// CLAUDE.md gives gatekeeper no env config of its own, and install.sh
// places the gatekeeper binary itself at /plugin/gatekeeper, so the deploy
// layout is already load-bearing on this exact path.
const stateDir = "/plugin"

func main() {
	st := newStore(stateDir, defaultPollInterval, time.Now)
	mailRate := envRate("MAIL_RATE_PER_MIN", defaultMailRatePerMinute)
	landsRate := envRate("LANDS_RATE_PER_MIN", defaultLandsRatePerMinute)
	lims := newLimiters(mailRate, landsRate, bucketIdleTTL, bucketSweepInterval, time.Now)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		processLine(in.Bytes(), st, lims, out)
	}
}

// envRate reads a rate-per-minute env var, falling back to def if unset or
// unparseable (a malformed knob must not crash the plugin — stderr and the
// default it is).
func envRate(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: invalid %s=%q, using default %v: %v\n", name, v, def, err)
		return def
	}
	return f
}

// processLine handles one line of the strfry plugin protocol: parse,
// decide, respond. A malformed line is logged to stderr and skipped —
// never allowed to kill the loop or write anything but a protocol response
// to stdout.
func processLine(line []byte, st *store, lims *limiters, out *bufio.Writer) {
	var req pluginRequest
	if err := json.Unmarshal(line, &req); err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: malformed input line: %v\n", err)
		return
	}
	var ev pluginEvent
	if err := json.Unmarshal(req.Event, &ev); err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: malformed event: %v\n", err)
		return
	}

	resp := decide(ev, st, lims, req.Source)

	b, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: marshal response: %v\n", err)
		return
	}
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
}

func decide(ev pluginEvent, st *store, lims *limiters, source string) pluginResponse {
	st.refresh()

	if st.isBanned(ev.PubKey) {
		return pluginResponse{ID: ev.ID, Action: "reject", Msg: msgBanned}
	}

	// Castle Mail: judged by recipient, not author (NIP-59 gift wraps use
	// random one-time signing keys, so author-based rules are blind to
	// them). Exempt from raid pruning, never from the write-path bucket —
	// mail is the one lane where a stranger earns PERMANENT storage, so it
	// is the one lane that is ALWAYS throttled, unconditionally.
	if isCastleMail(ev.Kind) && anyPTagIsCitizen(ev.Tags, st) {
		if !lims.mail.Allow(source) {
			return pluginResponse{ID: ev.ID, Action: "reject", Msg: msgMailRateLimit}
		}
		return pluginResponse{ID: ev.ID, Action: "accept"}
	}

	if st.isCitizen(ev.PubKey) {
		return pluginResponse{ID: ev.ID, Action: "accept"}
	}

	// Outer Lands: everyone else, including ephemeral-kind traffic (no
	// exemption — see DECISIONS.md). The lands bucket is off by default
	// (firehose); the raid, not a prefilter, is the moderation.
	if !lims.allowLands(source) {
		return pluginResponse{ID: ev.ID, Action: "reject", Msg: msgLandsRateLimit}
	}
	return pluginResponse{ID: ev.ID, Action: "accept"}
}

func isCastleMail(kind int) bool {
	return kind == 1059 || kind == 9735
}

func anyPTagIsCitizen(tags [][]string, st *store) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "p" && st.isCitizen(tag[1]) {
			return true
		}
	}
	return false
}

// store holds the banned/citizens hashsets, hot-reloaded from
// banned.json/citizens.json on the shared volume. Missing files mean empty
// sets (fail open) rather than an error. mtime is checked at most once per
// pollInterval so a busy relay isn't stat()-ing on every event; now is
// injectable so tests don't depend on wall-clock.
type store struct {
	bannedPath   string
	citizensPath string
	pollInterval time.Duration
	now          func() time.Time

	mu            sync.RWMutex
	lastCheck     time.Time
	bannedMTime   time.Time
	citizensMTime time.Time
	banned        map[string]struct{}
	citizens      map[string]struct{}
}

func newStore(dir string, pollInterval time.Duration, now func() time.Time) *store {
	return &store{
		bannedPath:   filepath.Join(dir, "banned.json"),
		citizensPath: filepath.Join(dir, "citizens.json"),
		pollInterval: pollInterval,
		now:          now,
		banned:       map[string]struct{}{},
		citizens:     map[string]struct{}{},
	}
}

func (s *store) refresh() {
	s.mu.Lock()
	now := s.now()
	if !s.lastCheck.IsZero() && now.Sub(s.lastCheck) < s.pollInterval {
		s.mu.Unlock()
		return
	}
	s.lastCheck = now
	s.mu.Unlock()

	s.reloadBanned()
	s.reloadCitizens()
}

// reloadBanned re-stats banned.json and reloads it if its mtime changed
// since the last reload. A missing file has a zero mtime; since a fresh
// store already starts with an empty set and a zero bannedMTime, "missing
// and always has been" is a no-op — the empty-set assignment only fires the
// moment a file that existed disappears.
func (s *store) reloadBanned() {
	mtime := statMTime(s.bannedPath)
	s.mu.RLock()
	same := mtime.Equal(s.bannedMTime)
	s.mu.RUnlock()
	if same {
		return
	}
	if mtime.IsZero() {
		s.mu.Lock()
		s.banned = map[string]struct{}{}
		s.bannedMTime = time.Time{}
		s.mu.Unlock()
		return
	}
	data, err := os.ReadFile(s.bannedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: read banned.json: %v\n", err)
		return
	}
	var b stateformat.Banned
	if err := json.Unmarshal(data, &b); err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: parse banned.json: %v\n", err)
		return
	}
	set := make(map[string]struct{}, len(b.Pubkeys))
	for _, pk := range b.Pubkeys {
		set[pk] = struct{}{}
	}
	s.mu.Lock()
	s.banned = set
	s.bannedMTime = mtime
	s.mu.Unlock()
}

func (s *store) reloadCitizens() {
	mtime := statMTime(s.citizensPath)
	s.mu.RLock()
	same := mtime.Equal(s.citizensMTime)
	s.mu.RUnlock()
	if same {
		return
	}
	if mtime.IsZero() {
		s.mu.Lock()
		s.citizens = map[string]struct{}{}
		s.citizensMTime = time.Time{}
		s.mu.Unlock()
		return
	}
	data, err := os.ReadFile(s.citizensPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: read citizens.json: %v\n", err)
		return
	}
	var c stateformat.Citizens
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintf(os.Stderr, "gatekeeper: parse citizens.json: %v\n", err)
		return
	}
	set := make(map[string]struct{}, len(c.Pubkeys))
	for _, pk := range c.Pubkeys {
		set[pk] = struct{}{}
	}
	s.mu.Lock()
	s.citizens = set
	s.citizensMTime = mtime
	s.mu.Unlock()
}

// statMTime returns path's mtime, or the zero Time if it's missing or
// unreadable (fail open — CLAUDE.md: "Missing files = empty sets").
func statMTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (s *store) isBanned(pk string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.banned[pk]
	return ok
}

func (s *store) isCitizen(pk string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.citizens[pk]
	return ok
}

// limiters holds the two per-IP token buckets CLAUDE.md calls for: mail is
// always on (Castle Mail is the one lane where a stranger earns permanent
// storage); lands is nil when LANDS_RATE_PER_MIN <= 0, meaning "disabled" —
// the Outer Lands are a firehose by default, and the raid is the
// moderation, not a prefilter.
type limiters struct {
	mail  *limiter
	lands *limiter
}

func newLimiters(mailRatePerMinute, landsRatePerMinute float64, idleTTL, sweepEvery time.Duration, now func() time.Time) *limiters {
	ls := &limiters{
		mail: newLimiter(mailRatePerMinute, mailRatePerMinute*2, idleTTL, sweepEvery, now),
	}
	if landsRatePerMinute > 0 {
		ls.lands = newLimiter(landsRatePerMinute, landsRatePerMinute*2, idleTTL, sweepEvery, now)
	}
	return ls
}

// allowLands reports whether a lands-bucket event may proceed. A nil lands
// bucket means the knob is at its default (0 = unlimited): always allow.
func (l *limiters) allowLands(source string) bool {
	if l.lands == nil {
		return true
	}
	return l.lands.Allow(source)
}

// limiter is a per-key (per-IP) token bucket. Idle buckets are swept
// periodically so unbounded IP churn doesn't grow memory forever.
type limiter struct {
	rate       float64 // tokens per second
	burst      float64
	idleTTL    time.Duration
	sweepEvery time.Duration
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	lastSweep time.Time
}

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
	lastSeen time.Time
}

func newLimiter(ratePerMinute, burst float64, idleTTL, sweepEvery time.Duration, now func() time.Time) *limiter {
	return &limiter{
		rate:       ratePerMinute / 60.0,
		burst:      burst,
		idleTTL:    idleTTL,
		sweepEvery: sweepEvery,
		now:        now,
		buckets:    map[string]*tokenBucket{},
	}
}

func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, lastFill: now}
		l.buckets[key] = b
	} else if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastFill = now
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) bucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *limiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < l.sweepEvery {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.idleTTL {
			delete(l.buckets, k)
		}
	}
}
