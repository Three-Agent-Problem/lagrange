package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

func upd(t *testing.T, v map[string]any) a2aacp.Update {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return a2aacp.Update{Type: v["sessionUpdate"].(string), Raw: raw}
}

func toolCall(t *testing.T, res *a2aacp.ConverterResult) (string, map[string]any) {
	t.Helper()
	if res == nil || len(res.Parts) != 2 {
		t.Fatalf("result = %+v, want a text and a data part", res)
	}
	tc, _ := Find(res.Parts, ToolCallKey).(map[string]any)
	if tc == nil {
		t.Fatalf("no toolCall part in %+v", res.Parts)
	}
	return res.Parts[0].Text(), tc
}

func TestToolCallStartAndFinish(t *testing.T) {
	ctx := context.Background()
	c := Converter(nil, Options{})
	ec := &a2asrv.ExecutorContext{TaskID: "t1"}

	res, err := c.Convert(ctx, ec, upd(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "go test ./...",
		"kind": "execute", "status": "pending", "rawInput": map[string]any{"command": "go test ./..."}}))
	if err != nil {
		t.Fatal(err)
	}
	text, tc := toolCall(t, res)
	if text != "Running go test ./..." || tc["status"] != StatusRunning || tc["kind"] != "execute" || tc["id"] != "c1" {
		t.Fatalf("start: %q %v", text, tc)
	}
	if in, _ := tc["input"].(map[string]any); in["command"] != "go test ./..." {
		t.Fatalf("start input = %v", tc["input"])
	}
	// in_progress after pending isn't reported twice.
	if res, _ := c.Convert(ctx, ec, upd(t, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "in_progress"})); res != nil {
		t.Fatalf("in_progress after the start: %+v", res)
	}
	out := strings.Repeat("x", MaxOutput+10) + "ok"
	res, err = c.Convert(ctx, ec, upd(t, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "completed",
		"title": "go test ./...", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}}}))
	if err != nil {
		t.Fatal(err)
	}
	text, tc = toolCall(t, res)
	got, _ := tc["output"].(string)
	if !strings.HasPrefix(text, "Finished") || !strings.HasSuffix(got, "ok") || len(got) > MaxOutput+len("…") {
		t.Fatalf("finish: %q, output %d bytes", text, len(got))
	}
}

func TestToolCallRawOutput(t *testing.T) {
	c := Converter(nil, Options{})
	res, err := c.Convert(context.Background(), &a2asrv.ExecutorContext{TaskID: "t"}, upd(t, map[string]any{"sessionUpdate": "tool_call_update",
		"toolCallId": "c", "title": "Read a.go", "status": "failed", "rawOutput": "no such file"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, tc := toolCall(t, res); tc["output"] != "no such file" {
		t.Fatalf("output = %v", tc["output"])
	}
}

func TestUsage(t *testing.T) {
	ctx := context.Background()
	c := Converter(nil, Options{})
	ec := &a2asrv.ExecutorContext{TaskID: "t1"}
	report := map[string]any{"sessionUpdate": "usage_update", "used": 52000, "size": 200000, "cost": map[string]any{"amount": 0.42, "currency": "USD"}}
	res, err := c.Convert(ctx, ec, upd(t, report))
	if err != nil || res == nil || len(res.Parts) != 1 {
		t.Fatalf("usage: %+v, %v", res, err)
	}
	u, ok := ParseUsage(Find(res.Parts, UsageKey))
	if !ok || u.Used != 52000 || u.Size != 200000 || u.Cost == nil || u.Cost.Amount != 0.42 || u.Cost.Currency != "USD" {
		t.Fatalf("usage = %+v", u)
	}
	if again, _ := c.Convert(ctx, ec, upd(t, report)); again != nil {
		t.Fatalf("a repeated report was emitted: %+v", again)
	}
	report["used"] = 60000
	if changed, _ := c.Convert(ctx, ec, upd(t, report)); changed == nil {
		t.Fatal("a changed report wasn't emitted")
	}
	if res, _ := c.Convert(ctx, ec, upd(t, map[string]any{"sessionUpdate": "usage_update"})); res != nil {
		t.Fatalf("an empty report was emitted: %+v", res)
	}
	// A turn's tokens (ReportTokens), twice the same: two turns, both reported.
	turn := map[string]any{"sessionUpdate": "usage_update", "_meta": map[string]any{TokensMeta: map[string]any{"output": 40, "cacheRead": 9000}}}
	for i := range 2 {
		res, _ := c.Convert(ctx, ec, upd(t, turn))
		if res == nil {
			t.Fatalf("turn %d: tokens not reported", i)
		}
		u, ok := ParseUsage(Find(res.Parts, UsageKey))
		if !ok || u.Tokens == nil || *u.Tokens != (Tokens{Output: 40, CacheRead: 9000}) {
			t.Fatalf("turn %d: usage = %+v", i, u)
		}
	}
}

func TestThoughts(t *testing.T) {
	ctx := context.Background()
	thought := map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hmm"}}
	if res, _ := Converter(nil, Options{}).Convert(ctx, &a2asrv.ExecutorContext{}, upd(t, thought)); res != nil {
		t.Fatalf("thoughts without the option: %+v", res)
	}
	res, err := Converter(nil, Options{Thoughts: true}).Convert(ctx, &a2asrv.ExecutorContext{}, upd(t, thought))
	if err != nil || res == nil || res.Artifact != ThoughtsArtifact || res.Parts[0].Text() != "hmm" {
		t.Fatalf("thought: %+v, %v", res, err)
	}
	// The reply still streams into response.
	msg := map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "done"}}
	if res, _ := Converter(nil, Options{Thoughts: true}).Convert(ctx, &a2asrv.ExecutorContext{}, upd(t, msg)); res == nil || res.Artifact != a2aacp.ResponseArtifact {
		t.Fatalf("message: %+v", res)
	}
}

func TestParseUsage(t *testing.T) {
	if _, ok := ParseUsage(map[string]any{}); ok {
		t.Fatal("empty usage parsed")
	}
	if u, ok := ParseUsage(map[string]any{"cost": map[string]any{"amount": 1.5, "currency": "USD"}}); !ok || u.Cost.Amount != 1.5 {
		t.Fatalf("cost only: %+v %v", u, ok)
	}
}

func TestUsageSession(t *testing.T) {
	c := Converter(nil, Options{Session: func(context.Context, *a2asrv.ExecutorContext) string { return "lead/S1/1" }})
	ec := &a2asrv.ExecutorContext{TaskID: "t1"}
	res, err := c.Convert(context.Background(), ec, upd(t, map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 2000,
		"cost": map[string]any{"amount": 0.5, "currency": "USD"}}))
	if err != nil || res == nil {
		t.Fatalf("convert: %v %v", res, err)
	}
	u, ok := ParseUsage(Find(res.Parts, UsageKey))
	if !ok || u.Session != "lead/S1/1" || u.Cost.Amount != 0.5 {
		t.Fatalf("usage = %+v", u)
	}
}
