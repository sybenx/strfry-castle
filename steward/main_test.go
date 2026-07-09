package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Listen != ":8787" {
		t.Fatalf("default LISTEN = %q, want :8787", cfg.Listen)
	}
	if !cfg.RaidDryRun {
		t.Fatal("RaidDryRun must default to true (first-deploy safety)")
	}
}

func TestWriteJSONAtomicCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "citizens.json")

	if err := writeJSONAtomic(path, stateformatTestShape{Pubkeys: []string{"a", "b"}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	assertJSONPubkeys(t, path, []string{"a", "b"})

	if err := writeJSONAtomic(path, stateformatTestShape{Pubkeys: []string{"c"}}); err != nil {
		t.Fatalf("replacing write: %v", err)
	}
	assertJSONPubkeys(t, path, []string{"c"})

	// No leftover temp files in the directory after a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir entries = %v, want exactly the one target file", entries)
	}
}

type stateformatTestShape struct {
	Pubkeys []string `json:"pubkeys"`
}

func assertJSONPubkeys(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got stateformatTestShape
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Pubkeys) != len(want) {
		t.Fatalf("pubkeys = %v, want %v", got.Pubkeys, want)
	}
	for i := range want {
		if got.Pubkeys[i] != want[i] {
			t.Fatalf("pubkeys = %v, want %v", got.Pubkeys, want)
		}
	}
}
