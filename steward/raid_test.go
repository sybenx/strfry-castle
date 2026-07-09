package main

import "testing"

func TestChunkStrings(t *testing.T) {
	items := make([]string, 120)
	for i := range items {
		items[i] = "pk"
	}
	batches := chunkStrings(items, 50)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches of <=50, got %d", len(batches))
	}
	if len(batches[0]) != 50 || len(batches[1]) != 50 || len(batches[2]) != 20 {
		t.Fatalf("unexpected batch sizes: %d %d %d", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if chunkStrings(nil, 50) != nil {
		t.Fatal("chunking an empty slice should yield nil")
	}
}
