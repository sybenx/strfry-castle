package main

import "testing"

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
