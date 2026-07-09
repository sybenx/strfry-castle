package stateformat

import (
	"encoding/json"
	"testing"
)

func TestBannedRoundTrip(t *testing.T) {
	want := Banned{Pubkeys: []string{"deadbeef", "cafef00d"}}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Banned
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Pubkeys) != 2 || got.Pubkeys[0] != "deadbeef" {
		t.Fatalf("round trip mismatch: got %+v", got)
	}
}

func TestCitizensRoundTrip(t *testing.T) {
	want := Citizens{Pubkeys: []string{"abc123"}}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Citizens
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Pubkeys) != 1 || got.Pubkeys[0] != "abc123" {
		t.Fatalf("round trip mismatch: got %+v", got)
	}
}
