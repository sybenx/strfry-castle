package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// newAPIServer builds a Server around a fresh Cycle with fake network/strfry
// dependencies (same fakes raid_test.go and stats_test.go use) and a fixed
// clock, so NIP-98's ±60s window is entirely test-controlled.
func newAPIServer(t *testing.T, ownerPub string, now time.Time) *Server {
	t.Helper()
	cycle := &Cycle{
		StateDir:       t.TempDir(),
		Owner:          ownerPub,
		OwnRelay:       "ws://own",
		PublicRelays:   nil,
		MaxInvites:     5,
		MaxDepth:       4,
		OuterTTLDays:   30,
		RunningVersion: "test",
		Fetcher:        &fakeFetcher{},
		Scanner:        &fakeScanner{},
		CLI:            &fakeStrfryCLI{},
		ReleaseChecker: &fakeReleaseChecker{},
		Now:            func() time.Time { return now },
	}
	return NewServer(cycle, t.TempDir(), "")
}

// buildNip98Event signs a NIP-98 event for method+url. When body is
// non-empty, it also attaches the `payload` tag (sha256 hex of body) that
// authenticate() now requires for any request carrying a body — mirroring
// what towncrier's real nip98Fetch sends.
func buildNip98Event(t *testing.T, sec, method, url string, createdAt time.Time, body []byte) nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"u", url}, {"method", method}}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		tags = append(tags, nostr.Tag{"payload", hex.EncodeToString(sum[:])})
	}
	evt := nostr.Event{
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Kind:      nip98Kind,
		Tags:      tags,
		Content:   "",
	}
	if err := evt.Sign(sec); err != nil {
		t.Fatalf("sign nip98 event: %v", err)
	}
	return evt
}

func authHeader(t *testing.T, evt nostr.Event) string {
	t.Helper()
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(data)
}

func apiRequest(t *testing.T, method, url string, body []byte, auth string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, url, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

// doRequest signs a fresh NIP-98 event for method+url with sec, attaches
// body, and runs it through the server's full handler (auth, routing, rate
// limiting all included).
func doRequest(t *testing.T, s *Server, sec, method, url string, body []byte, now time.Time) *httptest.ResponseRecorder {
	t.Helper()
	evt := buildNip98Event(t, sec, method, url, now, body)
	req := apiRequest(t, method, url, body, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func genKeypair(t *testing.T) (sec, pub string) {
	t.Helper()
	sec = nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(sec)
	if err != nil {
		t.Fatal(err)
	}
	return sec, pub
}

// --- NIP-98 auth checklist (CLAUDE.md: "bad NIP-98 sig rejected; stale
// created_at rejected; replayed event id rejected; u/method mismatch
// rejected") ---

func TestAPI_BadSignatureRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	url := "http://castle.example/api/wards"
	evt := buildNip98Event(t, ownerSec, "GET", url, now, nil)
	// Flip one hex digit of the signature: still well-formed hex, just
	// wrong, so this exercises CheckSignature specifically rather than a
	// decode failure.
	sig := []byte(evt.Sig)
	if sig[0] == '0' {
		sig[0] = '1'
	} else {
		sig[0] = '0'
	}
	evt.Sig = string(sig)

	req := apiRequest(t, "GET", url, nil, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a tampered signature", w.Code)
	}
}

func TestAPI_StaleCreatedAtRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	url := "http://castle.example/api/wards"
	evt := buildNip98Event(t, ownerSec, "GET", url, now.Add(-5*time.Minute), nil)
	req := apiRequest(t, "GET", url, nil, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a stale created_at", w.Code)
	}
}

func TestAPI_ReplayedEventRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	url := "http://castle.example/api/wards"
	header := authHeader(t, buildNip98Event(t, ownerSec, "GET", url, now, nil))

	w1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w1, apiRequest(t, "GET", url, nil, header))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", w1.Code)
	}

	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, apiRequest(t, "GET", url, nil, header))
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request status = %d, want 401", w2.Code)
	}
}

func TestAPI_URLMismatchRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	// Signed for /api/stats, presented against /api/wards.
	evt := buildNip98Event(t, ownerSec, "GET", "http://castle.example/api/stats", now, nil)
	req := apiRequest(t, "GET", "http://castle.example/api/wards", nil, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a u-tag/URL mismatch", w.Code)
	}
}

func TestAPI_MethodMismatchRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	url := "http://castle.example/api/wards"
	evt := buildNip98Event(t, ownerSec, "POST", url, now, nil) // signed for POST
	req := apiRequest(t, "GET", url, nil, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a method mismatch", w.Code)
	}
}

// TestAPI_BodyPayloadMismatchRejected: the signed event's `payload` tag
// must match the ACTUAL request body, not just be present. Without this,
// a captured Authorization header could be replayed against the same
// URL/method with an attacker-chosen body (e.g. a different invite target,
// or dry_run flipped on a raid).
func TestAPI_BodyPayloadMismatchRejected(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	url := "http://castle.example/api/invite"
	signedBody := mustJSON(t, inviteRequest{Pubkey: strings.Repeat("a", 64)})
	sentBody := mustJSON(t, inviteRequest{Pubkey: strings.Repeat("b", 64)})
	evt := buildNip98Event(t, ownerSec, "POST", url, now, signedBody)
	req := apiRequest(t, "POST", url, sentBody, authHeader(t, evt))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the payload tag doesn't match the actual body", w.Code)
	}
}

// TestAPI_DistinctBodiesSameSecondBothSucceed is the regression this fix
// targets directly: towncrier's raid control signs a preview
// (dry_run:true) and, on confirm, an immediate follow-up
// (dry_run:false) — both POST /api/raid, often within the same
// wall-clock second. Before binding the signature to the body via the
// `payload` tag, these two legitimate, distinct requests produced
// byte-identical NIP-98 events, and the replay guard rejected the second
// one as a replay of the first.
func TestAPI_DistinctBodiesSameSecondBothSucceed(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)
	s.Cycle.Scanner = &fakeScanner{}
	s.Cycle.CLI = &fakeStrfryCLI{}

	url := "http://castle.example/api/raid"
	previewBody := mustJSON(t, raidRequest{DryRun: true, TTLDays: intPtr(30)})
	confirmBody := mustJSON(t, raidRequest{DryRun: false, TTLDays: intPtr(30)})

	w1 := doRequest(t, s, ownerSec, "POST", url, previewBody, now)
	if w1.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", w1.Code, w1.Body.String())
	}
	w2 := doRequest(t, s, ownerSec, "POST", url, confirmBody, now)
	if w2.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200 (a distinct body must not trip the replay guard), body = %s", w2.Code, w2.Body.String())
	}
}

func TestAPI_MissingAuthRejected(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	req := httptest.NewRequest("GET", "http://castle.example/api/wards", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no Authorization header", w.Code)
	}
}

// --- /api/config: RELAY_URL's split-domain hook (CLAUDE.md, "Split-domain
// deployment") ---

func TestAPI_ConfigEmptyByDefault(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	req := httptest.NewRequest("GET", "http://castle.example/api/config", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["relay_url"] != "" {
		t.Fatalf("relay_url = %q, want empty when RELAY_URL is unset", body["relay_url"])
	}
}

func TestAPI_ConfigReturnsConfiguredRelayURL(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	cycle := &Cycle{
		StateDir:       t.TempDir(),
		Owner:          ownerPub,
		OwnRelay:       "ws://own",
		MaxInvites:     5,
		MaxDepth:       4,
		OuterTTLDays:   30,
		RunningVersion: "test",
		Fetcher:        &fakeFetcher{},
		Scanner:        &fakeScanner{},
		CLI:            &fakeStrfryCLI{},
		ReleaseChecker: &fakeReleaseChecker{},
		Now:            func() time.Time { return now },
	}
	s := NewServer(cycle, t.TempDir(), "wss://relay.example.com")

	req := httptest.NewRequest("GET", "http://castle.example/api/config", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["relay_url"] != "wss://relay.example.com" {
		t.Fatalf("relay_url = %q, want wss://relay.example.com", body["relay_url"])
	}
}

// --- /api/wards: Lord-only, and ward privacy across public endpoints ---

func TestAPI_WardsRefusesNonLord(t *testing.T) {
	_, ownerPub := genKeypair(t)
	strangerSec, _ := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	w := doRequest(t, s, strangerSec, "GET", "http://castle.example/api/wards", nil, now)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-Lord signer", w.Code)
	}
}

func TestAPI_WardsAbsentFromPublicPayloads(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	if err := AppendLedger(s.Cycle.ledgerPath(), Entry{
		Verb: VerbElevate, Pubkey: "wardpub1", Public: false, Source: "test", Timestamp: now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	mustRun(t, s.Cycle) // regenerates citizens.json/tree.json/stats.json/name-cache

	for _, path := range []string{"/api/tree", "/api/stats"} {
		req := httptest.NewRequest("GET", "http://castle.example"+path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, w.Code)
		}
		if bytes.Contains(w.Body.Bytes(), []byte("wardpub1")) {
			t.Fatalf("%s response leaked the ward pubkey: %s", path, w.Body.String())
		}
	}

	url := "http://castle.example/api/wards"
	w := doRequest(t, s, ownerSec, "GET", url, nil, now)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("wardpub1")) {
		t.Fatalf("Lord-signed /api/wards should list the ward, got %d %s", w.Code, w.Body.String())
	}
}

// --- Mutation endpoints: invite, remove, ennoble, elevate, lower, raid ---

func TestAPI_InviteAcceptsNpubAndAddsMember(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	_, memberPub := genKeypair(t)
	memberNpub, err := nip19.EncodePublicKey(memberPub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	body := mustJSON(t, inviteRequest{Pubkey: memberNpub, Label: "friend"})
	w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/invite", body, now)
	if w.Code != http.StatusOK {
		t.Fatalf("invite status = %d, body = %s", w.Code, w.Body.String())
	}

	state, _, err := s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tree.IsMember(memberPub) {
		t.Fatal("invite via API (npub) did not add the member to the tree")
	}
}

func TestAPI_InviteRejectsNonMemberNonLord(t *testing.T) {
	_, ownerPub := genKeypair(t)
	strangerSec, _ := genKeypair(t)
	_, targetPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	body := mustJSON(t, inviteRequest{Pubkey: targetPub})
	w := doRequest(t, s, strangerSec, "POST", "http://castle.example/api/invite", body, now)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-member, non-Lord inviter", w.Code)
	}
}

func TestAPI_EnnobleIsLordOnly(t *testing.T) {
	_, ownerPub := genKeypair(t)
	strangerSec, _ := genKeypair(t)
	_, targetPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	body := mustJSON(t, pubkeyRequest{Pubkey: targetPub})
	w := doRequest(t, s, strangerSec, "POST", "http://castle.example/api/ennoble", body, now)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-Lord ennoble", w.Code)
	}
}

func TestAPI_ElevateLowerFlow(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	_, targetPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	body := mustJSON(t, elevateRequest{Pubkey: targetPub, Public: true})
	w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/elevate", body, now)
	if w.Code != http.StatusOK {
		t.Fatalf("elevate status = %d, body = %s", w.Code, w.Body.String())
	}
	state, _, err := s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Elevation.IsFavorite(targetPub) {
		t.Fatal("elevate via API did not favorite the target")
	}

	// Re-elevating the same visibility is a true no-op: 200, changed=false,
	// nothing appended (CLAUDE.md: "a true no-op appends nothing and
	// returns success"). Advance the clock so the re-signed event is
	// distinct from the first (byte-identical NIP-98 events sign to the
	// same id, which the replay guard would otherwise reject).
	entriesBefore, err := ReadLedger(s.Cycle.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	s.Cycle.Now = func() time.Time { return now }
	w = doRequest(t, s, ownerSec, "POST", "http://castle.example/api/elevate", body, now)
	if w.Code != http.StatusOK {
		t.Fatalf("re-elevate status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["changed"] != false {
		t.Fatalf("re-elevating the same visibility must report changed=false, got %v", resp)
	}
	entriesAfter, err := ReadLedger(s.Cycle.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("a true elevation no-op must append nothing: before=%d after=%d", len(entriesBefore), len(entriesAfter))
	}

	// Lower removes it.
	lowerBody := mustJSON(t, pubkeyRequest{Pubkey: targetPub})
	w = doRequest(t, s, ownerSec, "POST", "http://castle.example/api/lower", lowerBody, now)
	if w.Code != http.StatusOK {
		t.Fatalf("lower status = %d, body = %s", w.Code, w.Body.String())
	}
	state, _, err = s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Elevation.IsElevated(targetPub) {
		t.Fatal("lower via API did not remove elevation")
	}
}

func TestAPI_RemoveByLord(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	_, memberPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	inviteBody := mustJSON(t, inviteRequest{Pubkey: memberPub})
	if w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/invite", inviteBody, now); w.Code != http.StatusOK {
		t.Fatalf("setup invite failed: %d %s", w.Code, w.Body.String())
	}

	removeBody := mustJSON(t, pubkeyRequest{Pubkey: memberPub})
	w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/remove", removeBody, now)
	if w.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body = %s", w.Code, w.Body.String())
	}
	state, _, err := s.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tree.IsMember(memberPub) {
		t.Fatal("remove via API did not cut the member from the tree")
	}
}

// --- /api/raid ---

func TestAPI_RaidDryRunPreviewDeletesNothing(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)
	s.Cycle.Scanner = &fakeScanner{events: []fakeStoredEvent{{Pubkey: "stranger1", CreatedAt: 1}}}
	cli := &fakeStrfryCLI{}
	s.Cycle.CLI = cli

	body := mustJSON(t, raidRequest{DryRun: true, TTLDays: intPtr(30)})
	w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/raid", body, now)
	if w.Code != http.StatusOK {
		t.Fatalf("raid status = %d, body = %s", w.Code, w.Body.String())
	}
	var result RaidResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Events == 0 {
		t.Fatal("dry-run preview should report a nonzero event count against the fixture stranger event")
	}
	for _, c := range cli.calls {
		if !c.DryRun {
			t.Fatal("a dry-run raid must never issue a real delete call")
		}
	}
}

func TestAPI_RaidRejectsInvalidTTLDays(t *testing.T) {
	ownerSec, ownerPub := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	body := mustJSON(t, raidRequest{TTLDays: intPtr(0)})
	w := doRequest(t, s, ownerSec, "POST", "http://castle.example/api/raid", body, now)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for ttl_days=0", w.Code)
	}
}

func TestAPI_RaidIsLordOnly(t *testing.T) {
	_, ownerPub := genKeypair(t)
	strangerSec, _ := genKeypair(t)
	now := time.Unix(3_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	w := doRequest(t, s, strangerSec, "POST", "http://castle.example/api/raid", mustJSON(t, raidRequest{}), now)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-Lord raid trigger", w.Code)
	}
}

func intPtr(n int) *int { return &n }

// --- Rate limiting ---

func TestRateLimiter_BlocksAfterLimit(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("1.2.3.4", now) {
			t.Fatalf("request %d should be allowed within the limit", i)
		}
	}
	if rl.Allow("1.2.3.4", now) {
		t.Fatal("request past the limit should be blocked")
	}
	// A different key has its own bucket.
	if !rl.Allow("5.6.7.8", now) {
		t.Fatal("a different IP must not share the exhausted bucket")
	}
	// The window resets after it elapses.
	if !rl.Allow("1.2.3.4", now.Add(time.Minute+time.Second)) {
		t.Fatal("the limit should reset once the window elapses")
	}
}

// --- GET /api/stats: first-cycle-pending degrade ---

func TestHandleStats_PendingBeforeFirstCycle(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	req := httptest.NewRequest("GET", "http://castle.example/api/stats", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before stats.json exists", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(body["error"], "first sync") {
		t.Fatalf("error = %q, want it to mention the first sync being in progress", body["error"])
	}
}

func TestHandleStats_GenericUnavailableAfterFirstCycleWithNoStats(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)
	s.MarkFirstCycleDone()

	req := httptest.NewRequest("GET", "http://castle.example/api/stats", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when stats.json still doesn't exist", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if strings.Contains(body["error"], "first sync") {
		t.Fatalf("error = %q, should no longer blame first-sync pendency once it's marked done", body["error"])
	}
}

func TestHandleStats_ServesStatsOnceWritten(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	if err := writeJSONAtomic(s.Cycle.statsPath(), Stats{TheLord: LordStats{Pubkey: ownerPub}}); err != nil {
		t.Fatalf("write stats.json: %v", err)
	}

	// A pending first cycle must not shadow stats.json once it actually exists.
	req := httptest.NewRequest("GET", "http://castle.example/api/stats", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 once stats.json exists, body: %s", w.Code, w.Body)
	}
	var stats Stats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.TheLord.Pubkey != ownerPub {
		t.Fatalf("the_lord.pubkey = %q, want %q", stats.TheLord.Pubkey, ownerPub)
	}
}

func TestAPI_UnknownAPIPathIs404NotStaticFallback(t *testing.T) {
	_, ownerPub := genKeypair(t)
	now := time.Unix(2_000_000, 0)
	s := newAPIServer(t, ownerPub, now)

	req := httptest.NewRequest("GET", "http://castle.example/api/nonexistent", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown /api/ path", w.Code)
	}
}
