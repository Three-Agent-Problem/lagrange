package activity

import (
	"strings"
	"testing"
)

func toolEntry(agent, id, kind string, input any, output string) *Entry {
	tc := map[string]any{"id": id, "kind": kind, "title": kind, "status": "in_progress"}
	if input != nil {
		tc["input"] = input
	}
	if output != "" {
		tc["output"], tc["status"] = output, "completed"
	}
	return &Entry{Kind: KindTask, Recipient: agent, Data: map[string]any{"toolCall": tc}}
}

func TestOverlap(t *testing.T) {
	read := map[string]any{"file_path": "/repo/main.go"}
	out := strings.Repeat("x", 400) // 100 tokens
	entries := []*Entry{
		// w1 reads main.go: a start entry with the input, a finish entry with the output.
		toolEntry("w1", "c1", "read", read, ""),
		toolEntry("w1", "c1", "read", nil, out),
		// w2 reads the same file.
		toolEntry("w2", "c9", "read", map[string]any{"file_path": "/repo/main.go"}, out),
		// w2 runs the same command twice.
		toolEntry("w2", "c10", "execute", map[string]any{"command": "go test ./..."}, "ok"),
		toolEntry("w2", "c11", "execute", map[string]any{"command": "go test ./..."}, "ok"),
		// Coordination calls (kind other) and edits never count as repeats.
		toolEntry("lead", "m1", "other", map[string]any{"timeout_seconds": 120}, "x"),
		toolEntry("lead", "m2", "other", map[string]any{"timeout_seconds": 120}, "x"),
		toolEntry("w1", "e1", "edit", map[string]any{"file_path": "/repo/a.go"}, "ok"),
		toolEntry("w1", "e2", "edit", map[string]any{"file_path": "/repo/a.go"}, "ok"),
		// A one-off call.
		toolEntry("w1", "c2", "read", map[string]any{"file_path": "/repo/other.go"}, "y"),
		{Kind: KindUsage, Recipient: "w1", Data: map[string]any{"costDelta": 0.5}},
		{Kind: KindUsage, Recipient: "w1", Data: map[string]any{"costDelta": 0.25}},
		{Kind: KindUsage, Recipient: "w2", Data: map[string]any{"costDelta": 1.0}},
		{Kind: KindTask, Recipient: "w2", State: "working"}, // no tool call
		nil,
	}
	rep := Overlap(entries)
	if rep.Calls != 9 {
		t.Fatalf("Calls = %d, want 9", rep.Calls)
	}
	if rep.RepeatedCalls != 2 {
		t.Fatalf("RepeatedCalls = %d, want 2", rep.RepeatedCalls)
	}
	if rep.RepeatedTokens != 100 {
		t.Fatalf("RepeatedTokens = %d, want 100", rep.RepeatedTokens)
	}
	if rep.Cost != 1.75 {
		t.Fatalf("Cost = %v, want 1.75", rep.Cost)
	}
	if len(rep.Repeats) != 2 || !rep.Repeats[0].CrossAgent() || rep.Repeats[0].Kind != "read" || rep.Repeats[1].CrossAgent() {
		t.Fatalf("Repeats = %+v, want the cross-agent read first", rep.Repeats)
	}
	if len(rep.Agents) != 3 || rep.Agents[1] != (AgentWork{Agent: "w1", Calls: 4, Cost: 0.75}) || rep.Agents[2].Calls != 3 {
		t.Fatalf("Agents = %+v", rep.Agents)
	}
}

func TestOverlapEmpty(t *testing.T) {
	rep := Overlap(nil)
	if rep.Calls != 0 || len(rep.Repeats) != 0 || len(rep.Agents) != 0 {
		t.Fatalf("Overlap(nil) = %+v", rep)
	}
}
