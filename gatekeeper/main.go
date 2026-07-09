// Command gatekeeper is strfry's write-policy plugin for the Castle. It
// reads newline-delimited JSON on stdin (strfry's plugin protocol) and
// writes an accept/reject decision per line on stdout. Stdlib only — see
// CLAUDE.md, Component 1.
//
// This is a Phase 0 stub: it accepts everything so `make build` has a
// binary to compile. The real hashset checks, token bucket, and hot-reload
// land in Phase 1.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type pluginRequest struct {
	Type   string          `json:"type"`
	Event  json.RawMessage `json:"event"`
	Source string          `json:"sourceInfo"`
}

type pluginEvent struct {
	ID string `json:"id"`
}

type pluginResponse struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Msg    string `json:"msg,omitempty"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		line := in.Bytes()
		var req pluginRequest
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "gatekeeper: malformed input line: %v\n", err)
			continue
		}
		var ev pluginEvent
		if err := json.Unmarshal(req.Event, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "gatekeeper: malformed event: %v\n", err)
			continue
		}
		resp := pluginResponse{ID: ev.ID, Action: "accept"}
		b, err := json.Marshal(resp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gatekeeper: marshal response: %v\n", err)
			continue
		}
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
}
