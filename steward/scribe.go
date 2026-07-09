// The scribe: manual per-member archival via POST /api/archive. One-shot
// background job, own goroutine and failure domain. Lands in Phase 5b.
// See CLAUDE.md, "Manual archival (the scribe)".
package main
