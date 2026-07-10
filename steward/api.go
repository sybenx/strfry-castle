// The HTTP API: NIP-98 auth, towncrier static file serving, all /api
// endpoints from CLAUDE.md.
// See CLAUDE.md, "HTTP API".
package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// nip98Kind is the event kind NIP-98 HTTP Auth uses.
const nip98Kind = 27235

// nip98Tolerance bounds how far a NIP-98 event's created_at may drift from
// steward's clock, per CLAUDE.md: "created_at within ±60s".
const nip98Tolerance = 60 * time.Second

// nip98ReplayWindow is how long a NIP-98 event id is remembered to reject a
// replayed request, per CLAUDE.md: "remember event ids for 5 minutes
// (in-memory)".
const nip98ReplayWindow = 5 * time.Minute

// rateLimitWindow/rateLimitPerWindow bound the per-IP request rate on
// /api/*, per CLAUDE.md: "Rate-limit the API per IP." Generous enough for a
// signed-in Lord clicking through the UI, tight enough to blunt a script
// hammering the signed-request endpoints.
const (
	rateLimitWindow    = time.Minute
	rateLimitPerWindow = 60
)

var errUnauthorized = errors.New("api: unauthorized")

// Server is the HTTP API: routing, NIP-98 authentication, and towncrier's
// static files. It holds no domain state of its own — every request reads
// the ledger fresh via Cycle so a restart mid-outage can never drift from
// disk (same discipline as cycle.go and raid.go).
type Server struct {
	Cycle        *Cycle
	TowncrierDir string

	// mu serializes every read-modify-write mutation (ledger read, ledger
	// append, state-file rewrite) and every raid, so concurrent API
	// requests can never race on ledger.jsonl or lose an update.
	mu sync.Mutex

	replay *replayGuard
	rate   *rateLimiter
}

// NewServer builds a Server around an already-configured Cycle.
func NewServer(cycle *Cycle, towncrierDir string) *Server {
	return &Server{
		Cycle:        cycle,
		TowncrierDir: towncrierDir,
		replay:       newReplayGuard(),
		rate:         newRateLimiter(rateLimitPerWindow, rateLimitWindow),
	}
}

// Handler builds the full HTTP handler: the JSON API under /api, and
// towncrier's static files everywhere else. CORS: same-origin only — this
// deliberately emits no Access-Control-Allow-* headers at all, so a
// cross-origin browser request (which the POST endpoints' JSON body and
// Authorization header force into a CORS preflight) is refused by the
// browser itself. That is the entire same-origin policy; adding headers
// here would only widen it.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/wards", s.handleWards)
	mux.HandleFunc("POST /api/invite", s.handleInvite)
	mux.HandleFunc("POST /api/remove", s.handleRemove)
	mux.HandleFunc("POST /api/ennoble", s.handleEnnoble)
	mux.HandleFunc("POST /api/elevate", s.handleElevate)
	mux.HandleFunc("POST /api/lower", s.handleLower)
	mux.HandleFunc("POST /api/raid", s.handleRaid)
	// Catches any other /api/* path (wrong method or unknown route) so it
	// 404s instead of falling through to the static file server below.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	mux.Handle("/", http.FileServer(http.Dir(s.TowncrierDir)))

	return s.withRateLimit(mux)
}

// withRateLimit enforces rateLimitPerWindow requests per IP per
// rateLimitWindow on /api/* only — static asset serving is a single small
// file with no abuse surface worth gating.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !s.rate.Allow(clientIP(r), s.Cycle.Now()) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP prefers X-Forwarded-For (CLAUDE.md's reverse-proxy setup forwards
// a real-IP header) and falls back to the raw connection address.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			fwd = fwd[:i]
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- JSON response helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSONBody decodes r's body into v. A missing/empty body is not an
// error — several endpoints (ennoble, raid with no override) have every
// field optional.
func decodeJSONBody(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// --- NIP-98 auth ---

// replayGuard remembers NIP-98 event ids for nip98ReplayWindow so the same
// signed request can't be replayed. In-memory only, per CLAUDE.md — a
// steward restart resets it, which is fine since NIP-98's ±60s window means
// a stale replay would be rejected on timestamp grounds anyway.
type replayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time // event id -> expiry
}

func newReplayGuard() *replayGuard {
	return &replayGuard{seen: make(map[string]time.Time)}
}

// checkAndRemember returns false if id was already seen and hasn't expired
// yet (a replay); otherwise it remembers id and returns true.
func (g *replayGuard) checkAndRemember(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, exp := range g.seen {
		if now.After(exp) {
			delete(g.seen, k)
		}
	}
	if exp, ok := g.seen[id]; ok && now.Before(exp) {
		return false
	}
	g.seen[id] = now.Add(nip98ReplayWindow)
	return true
}

// rateLimiter is a fixed-window per-key counter.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*rateWindow
}

type rateWindow struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, counts: make(map[string]*rateWindow)}
}

func (rl *rateLimiter) Allow(key string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	w, ok := rl.counts[key]
	if !ok || !now.Before(w.resetAt) {
		w = &rateWindow{resetAt: now.Add(rl.window)}
		rl.counts[key] = w
	}
	w.count++
	return w.count <= rl.limit
}

// authIdentity is what a verified NIP-98 request proves: who signed it
// (Pubkey) and the event's id, used as ledger provenance (Source) for
// whatever mutation the request triggers — never as a retention target,
// per CLAUDE.md's durable-state invariant.
type authIdentity struct {
	Pubkey string
	Source string
}

// authenticate verifies r's `Authorization: Nostr <base64 kind-27235
// event>` header per NIP-98: signature, `u` tag matches the full request
// URL, `method` tag matches the HTTP method, created_at within ±60s, and
// the event id hasn't been seen in the last 5 minutes.
func (s *Server) authenticate(r *http.Request) (authIdentity, error) {
	header := r.Header.Get("Authorization")
	const prefix = "Nostr "
	if !strings.HasPrefix(header, prefix) {
		return authIdentity{}, errUnauthorized
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return authIdentity{}, errUnauthorized
	}
	var evt nostr.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		return authIdentity{}, errUnauthorized
	}
	if evt.Kind != nip98Kind {
		return authIdentity{}, errUnauthorized
	}

	now := s.Cycle.Now()
	delta := now.Sub(evt.CreatedAt.Time())
	if delta < 0 {
		delta = -delta
	}
	if delta > nip98Tolerance {
		return authIdentity{}, errUnauthorized
	}

	uTag := evt.Tags.Find("u")
	methodTag := evt.Tags.Find("method")
	if uTag == nil || methodTag == nil {
		return authIdentity{}, errUnauthorized
	}
	if methodTag[1] != r.Method {
		return authIdentity{}, errUnauthorized
	}
	if uTag[1] != requestURL(r) {
		return authIdentity{}, errUnauthorized
	}

	ok, err := evt.CheckSignature()
	if err != nil || !ok {
		return authIdentity{}, errUnauthorized
	}
	// CheckSignature verifies the signature against a hash recomputed from
	// the event body — it never looks at evt.ID. Without also checking
	// CheckID, a client-supplied ID unrelated to the signed content would
	// still pass, and the replay guard (keyed on evt.ID below) could be
	// trivially bypassed by resubmitting the same signed content under a
	// fresh, made-up ID.
	if !evt.CheckID() {
		return authIdentity{}, errUnauthorized
	}

	if !s.replay.checkAndRemember(evt.ID, now) {
		return authIdentity{}, errUnauthorized
	}

	return authIdentity{Pubkey: evt.PubKey, Source: evt.ID}, nil
}

// requestURL reconstructs the full URL a NIP-98 event's `u` tag must match.
// steward sits behind a reverse proxy that terminates TLS (CLAUDE.md's
// "Reverse proxy" section), so the externally-visible scheme is trusted
// from X-Forwarded-Proto when present.
func requestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host + r.URL.Path
}

// requireLord authenticates r and additionally requires the signer to be
// the Lord. Used by every Lord-only endpoint.
func (s *Server) requireLord(w http.ResponseWriter, r *http.Request) (authIdentity, bool) {
	auth, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return authIdentity{}, false
	}
	if auth.Pubkey != s.Cycle.Owner {
		writeError(w, http.StatusForbidden, "the Lord's signature is required")
		return authIdentity{}, false
	}
	return auth, true
}

// --- pubkey normalization ---

// normalizePubkey accepts npub or lowercase/uppercase hex, per CLAUDE.md:
// "Accept npub or hex everywhere a pubkey is accepted."
func normalizePubkey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "npub1") {
		prefix, value, err := nip19.Decode(s)
		if err != nil || prefix != "npub" {
			return "", fmt.Errorf("invalid npub")
		}
		hexKey, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("invalid npub")
		}
		return hexKey, nil
	}
	if len(s) != 64 {
		return "", fmt.Errorf("invalid pubkey: want 64-char hex or npub1...")
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("invalid pubkey: not valid hex")
	}
	return strings.ToLower(s), nil
}

// --- domain state access ---

// loadState replays the current ledger and reads the current follows
// snapshot. Every handler reads fresh state this way rather than caching —
// the same discipline cycle.go and raid.go use, so a mutation from another
// request is always visible immediately.
func (s *Server) loadState() (*State, FollowsSnapshot, error) {
	entries, err := ReadLedger(s.Cycle.ledgerPath())
	if err != nil {
		return nil, FollowsSnapshot{}, err
	}
	state, err := BuildState(s.Cycle.Owner, entries, s.Cycle.MaxInvites, s.Cycle.MaxDepth)
	if err != nil {
		return nil, FollowsSnapshot{}, err
	}
	follows, err := readFollows(s.Cycle.followsPath())
	if err != nil {
		return nil, FollowsSnapshot{}, err
	}
	return state, follows, nil
}

// mutate runs fn against freshly-replayed state under s.mu, appends the
// resulting ledger entry, and immediately rewrites citizens.json and
// tree.json — CLAUDE.md: "Every mutation: append to ledger, rewrite state
// files immediately (no waiting for the next cycle)." If fn returns
// ErrNoChange (an idempotent no-op — e.g. re-elevating the same visibility,
// lowering someone not elevated), nothing is appended or rewritten and
// changed is false, but err is nil: CLAUDE.md requires a true no-op to
// "return success", not an error.
func (s *Server) mutate(fn func(*State) (Entry, error)) (state *State, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := ReadLedger(s.Cycle.ledgerPath())
	if err != nil {
		return nil, false, err
	}
	state, err = BuildState(s.Cycle.Owner, entries, s.Cycle.MaxInvites, s.Cycle.MaxDepth)
	if err != nil {
		return nil, false, err
	}

	entry, err := fn(state)
	if errors.Is(err, ErrNoChange) {
		return state, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	if err := AppendLedger(s.Cycle.ledgerPath(), entry); err != nil {
		return nil, false, err
	}

	follows, err := readFollows(s.Cycle.followsPath())
	if err != nil {
		return nil, false, err
	}
	if err := writeJSONAtomic(s.Cycle.citizensPath(), state.CitizensJSON(follows.Pubkeys)); err != nil {
		return nil, false, err
	}
	if err := writeJSONAtomic(s.Cycle.treePath(), state.Tree); err != nil {
		return nil, false, err
	}

	return state, true, nil
}

// mutationErrorStatus maps a domain error from tree.go/ledger.go to an HTTP
// status. Anything unrecognized is a 500 — it means a handler let through
// something it shouldn't have.
func mutationErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotInviter), errors.Is(err, ErrNotOwnInvitee):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrAlreadyMember), errors.Is(err, ErrMaxInvites), errors.Is(err, ErrMaxDepth):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// --- GET /api/stats ---

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.Cycle.statsPath())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "stats not yet generated")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// --- GET /api/tree ---

// TreeMemberView is one /api/tree member row: tree.json's Member plus the
// resolved kind-0 name/picture and whether the Lord has starred them.
type TreeMemberView struct {
	Pubkey    string `json:"pubkey"`
	InvitedBy string `json:"invited_by"`
	InvitedAt int64  `json:"invited_at"`
	Label     string `json:"label,omitempty"`
	Name      string `json:"name,omitempty"`
	Picture   string `json:"picture,omitempty"`
	Favorite  bool   `json:"favorite"`
}

// FavoredView is a public favorite who is not a tree member — CLAUDE.md's
// "Favored of the Lord" section.
type FavoredView struct {
	Pubkey  string `json:"pubkey"`
	Name    string `json:"name,omitempty"`
	Picture string `json:"picture,omitempty"`
}

// EvictedTreeView is one evicted-but-in-grace member, named for the public
// "struck through" listing. Never includes a ward — evictedInGrace (stats.go)
// only ever sees eviction timestamps, which carry no visibility info.
type EvictedTreeView struct {
	Pubkey  string `json:"pubkey"`
	Name    string `json:"name,omitempty"`
	Expires int64  `json:"expires"`
}

// TreeResponse is GET /api/tree's body. Ward-safe by construction: it is
// built only from state.Tree.Members, state.Elevation.Records where
// Public==true, and evictedInGrace — a ward record is never public, so it
// can never enter Members/Favored, and evictedInGrace carries no visibility
// info at all.
type TreeResponse struct {
	Owner   string            `json:"owner"`
	Members []TreeMemberView  `json:"members"`
	Favored []FavoredView     `json:"favored"`
	Evicted []EvictedTreeView `json:"evicted"`
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	state, follows, err := s.loadState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	names, err := readNameCache(s.Cycle.nameCachePath())
	if err != nil {
		names = NameCache{}
	}
	now := s.Cycle.Now().Unix()
	evicted := evictedInGrace(state, follows.Pubkeys, now, s.Cycle.OuterTTLDays)

	members := make([]TreeMemberView, 0, len(state.Tree.Members))
	for pk, m := range state.Tree.Members {
		n := names[pk]
		members = append(members, TreeMemberView{
			Pubkey:    pk,
			InvitedBy: m.InvitedBy,
			InvitedAt: m.InvitedAt,
			Label:     m.Label,
			Name:      n.Name,
			Picture:   n.Picture,
			Favorite:  state.Elevation.IsFavorite(pk),
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Pubkey < members[j].Pubkey })

	favored := []FavoredView{}
	for pk, rec := range state.Elevation.Records {
		if !rec.Public || state.Tree.IsMember(pk) {
			continue
		}
		n := names[pk]
		favored = append(favored, FavoredView{Pubkey: pk, Name: n.Name, Picture: n.Picture})
	}
	sort.Slice(favored, func(i, j int) bool { return favored[i].Pubkey < favored[j].Pubkey })

	evictedViews := make([]EvictedTreeView, 0, len(evicted))
	for _, e := range evicted {
		n := names[e.Pubkey]
		evictedViews = append(evictedViews, EvictedTreeView{Pubkey: e.Pubkey, Name: n.Name, Expires: e.Expires})
	}

	writeJSON(w, http.StatusOK, TreeResponse{
		Owner:   state.Owner,
		Members: members,
		Favored: favored,
		Evicted: evictedViews,
	})
}

// --- GET /api/wards (Lord only) ---

func (s *Server) handleWards(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireLord(w, r); !ok {
		return
	}
	state, _, err := s.loadState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	wards := []string{}
	for pk, rec := range state.Elevation.Records {
		if !rec.Public {
			wards = append(wards, pk)
		}
	}
	sort.Strings(wards)
	writeJSON(w, http.StatusOK, map[string][]string{"wards": wards})
}

// --- POST /api/invite ---

type inviteRequest struct {
	Pubkey string `json:"pubkey"`
	Label  string `json:"label,omitempty"`
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req inviteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	target, err := normalizePubkey(req.Pubkey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := s.Cycle.Now().Unix()
	_, changed, err := s.mutate(func(st *State) (Entry, error) {
		return st.Invite(auth.Pubkey, target, req.Label, auth.Source, now)
	})
	if err != nil {
		writeError(w, mutationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// --- POST /api/remove ---

type pubkeyRequest struct {
	Pubkey string `json:"pubkey"`
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req pubkeyRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	target, err := normalizePubkey(req.Pubkey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := s.Cycle.Now().Unix()
	_, changed, err := s.mutate(func(st *State) (Entry, error) {
		return st.Remove(auth.Pubkey, target, auth.Source, now)
	})
	if err != nil {
		writeError(w, mutationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// --- POST /api/ennoble (Lord only) ---

func (s *Server) handleEnnoble(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireLord(w, r)
	if !ok {
		return
	}
	var req pubkeyRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	target, err := normalizePubkey(req.Pubkey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := s.Cycle.Now().Unix()
	_, changed, err := s.mutate(func(st *State) (Entry, error) {
		return st.Ennoble(target, auth.Source, now)
	})
	if err != nil {
		writeError(w, mutationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// --- POST /api/elevate (Lord only) ---

type elevateRequest struct {
	Pubkey string `json:"pubkey"`
	Public bool   `json:"public"`
}

func (s *Server) handleElevate(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireLord(w, r)
	if !ok {
		return
	}
	var req elevateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	target, err := normalizePubkey(req.Pubkey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := s.Cycle.Now().Unix()
	_, changed, err := s.mutate(func(st *State) (Entry, error) {
		return st.Elevate(target, req.Public, auth.Source, now)
	})
	if err != nil {
		writeError(w, mutationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// --- POST /api/lower (Lord only) ---

func (s *Server) handleLower(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireLord(w, r)
	if !ok {
		return
	}
	var req pubkeyRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	target, err := normalizePubkey(req.Pubkey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := s.Cycle.Now().Unix()
	_, changed, err := s.mutate(func(st *State) (Entry, error) {
		return st.Lower(target, auth.Source, now)
	})
	if err != nil {
		writeError(w, mutationErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

// --- POST /api/raid (Lord only) ---

type raidRequest struct {
	TTLDays *int `json:"ttl_days,omitempty"`
	DryRun  bool `json:"dry_run,omitempty"`
}

func (s *Server) handleRaid(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireLord(w, r)
	if !ok {
		return
	}
	var req raidRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}

	s.mu.Lock()
	result, err := s.Cycle.Raid(r.Context(), req.TTLDays, req.DryRun, auth.Source)
	s.mu.Unlock()

	if errors.Is(err, ErrInvalidTTLDays) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "raid failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
