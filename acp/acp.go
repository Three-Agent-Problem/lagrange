// Package acp holds Lagrange's conventions for coding agents served with a2aacp
// (tunnel --acp, or an executor of your own), so that every Lagrange client sees
// what such an agent does, not only the machine that runs it.
//
//   - Tool calls: a working status message when a call starts, not only when it
//     finishes, each with a {"toolCall": {...}} data part (id, title, kind,
//     status, input, and the output once it finished). The relay records that
//     part in the activity log.
//   - Usage: the agent's token and cost reports, as a status message with a
//     {"usage": {...}} data part (see [Usage]).
//   - Thoughts: with Options.Thoughts, the agent's thinking streams into the
//     [ThoughtsArtifact] artifact. Its reply streams into "response" as before.
//
// Wrap the converter of your a2aacp.Config with [Converter]:
//
//	cfg := &a2aacp.Config{Converter: acp.Converter(nil, acp.Options{Thoughts: true})}
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

// Keys and names this package writes.
const (
	// ToolCallKey is the data part key of a tool call: {"toolCall": {"id", "title",
	// "kind", "status", "input", "output"}}. a2aacp uses the same shape.
	ToolCallKey = "toolCall"
	// UsageKey is the data part key of a usage report: {"usage": {...}}.
	UsageKey = "usage"
	// ThoughtsArtifact is the name of the artifact the agent's thinking streams
	// into, with Options.Thoughts.
	ThoughtsArtifact = "thoughts"
	// StatusRunning is the toolCall status of a call that started.
	StatusRunning = "in_progress"
	// MaxOutput caps a finished tool call's output, in bytes; the end is kept.
	MaxOutput = 4096
)

// Options select what Converter adds to a2aacp's built-in conversion.
type Options struct {
	// Thoughts streams the agent's thinking into the thoughts artifact. Each
	// chunk is an event, so it costs activity log space.
	Thoughts bool
	// Session returns the key of the agent session a task runs in, for usage
	// reports (Usage.Session). Leave it nil when every A2A context has its own
	// session, which is a2aacp's default.
	Session func(ctx context.Context, ec *a2asrv.ExecutorContext) string
}

// Converter wraps next (a2aacp.ConvertUpdate if nil) with Lagrange's conventions.
// It is safe for concurrent tasks.
func Converter(next a2aacp.Converter, o Options) a2aacp.Converter {
	if next == nil {
		next = a2aacp.ConverterFunc(a2aacp.ConvertUpdate)
	}
	return &converter{next: next, o: o, started: map[string]bool{}, lastUsage: map[string]string{}}
}

type converter struct {
	next    a2aacp.Converter
	o       Options
	mu      sync.Mutex
	started map[string]bool // task + tool call ID: a start was already reported
	// lastUsage is the last usage report per task, as JSON, to skip repeats.
	lastUsage map[string]string
}

// rawUpdate is the part of a session update this package reads.
type rawUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Content    json.RawMessage `json:"content"`
	// usage_update (claude-agent-acp): context use and the session's cost so far.
	Used int64           `json:"used"`
	Size int64           `json:"size"`
	Cost json.RawMessage `json:"cost"`
}

func (c *converter) Convert(ctx context.Context, ec *a2asrv.ExecutorContext, u a2aacp.Update) (*a2aacp.ConverterResult, error) {
	var raw rawUpdate
	_ = json.Unmarshal(u.Raw, &raw)

	switch u.Type {
	case "tool_call", "tool_call_update":
		if raw.ToolCallID == "" {
			break
		}
		key := string(ec.TaskID) + "\x00" + raw.ToolCallID
		if raw.Status == "pending" || (u.Type == "tool_call" && raw.Status == "") {
			// The call started. a2aacp reports calls from in_progress on, and
			// some agents (claude-agent-acp) go from pending straight to completed.
			c.mu.Lock()
			seen := c.started[key]
			c.started[key] = true
			c.mu.Unlock()
			if seen {
				return nil, nil
			}
			return toolStart(raw), nil
		}
		res, err := c.next.Convert(ctx, ec, u)
		if err != nil || res == nil {
			return res, err
		}
		c.mu.Lock()
		seen := c.started[key]
		switch raw.Status {
		case StatusRunning:
			c.started[key] = true
		case "completed", "failed":
			delete(c.started, key)
		}
		c.mu.Unlock()
		if raw.Status == StatusRunning && seen {
			return nil, nil // already reported as started
		}
		if raw.Status == "completed" || raw.Status == "failed" {
			addOutput(res, raw)
		}
		return res, nil

	case "usage_update":
		session := ""
		if c.o.Session != nil {
			session = c.o.Session(ctx, ec)
		}
		res := usageResult(raw, session)
		if res == nil {
			return nil, nil
		}
		b, _ := json.Marshal(res.Parts[0].Data())
		c.mu.Lock()
		same := c.lastUsage[string(ec.TaskID)] == string(b)
		c.lastUsage[string(ec.TaskID)] = string(b)
		c.mu.Unlock()
		if same {
			return nil, nil
		}
		return res, nil

	case "agent_thought_chunk":
		if c.o.Thoughts {
			if t := chunkText(raw.Content); t != "" {
				return &a2aacp.ConverterResult{Artifact: ThoughtsArtifact, Parts: []*a2a.Part{a2a.NewTextPart(t)}}, nil
			}
		}
		return nil, nil
	}
	return c.next.Convert(ctx, ec, u)
}

func toolStart(raw rawUpdate) *a2aacp.ConverterResult {
	title := raw.Title
	if title == "" {
		title = raw.Kind
	}
	data := map[string]any{"id": raw.ToolCallID, "title": raw.Title, "kind": raw.Kind, "status": StatusRunning}
	if in := jsonValue(raw.RawInput); in != nil {
		data["input"] = in
	}
	return &a2aacp.ConverterResult{Parts: []*a2a.Part{
		a2a.NewTextPart(fmt.Sprintf("Running %s", title)),
		a2a.NewDataPart(map[string]any{ToolCallKey: data}),
	}}
}

// addOutput puts a finished call's output into its toolCall data part.
func addOutput(res *a2aacp.ConverterResult, raw rawUpdate) {
	out := contentText(raw.Content)
	if out == "" {
		if s, ok := jsonValue(raw.RawOutput).(string); ok {
			out = s
		} else if len(raw.RawOutput) > 0 && string(raw.RawOutput) != "null" {
			out = string(raw.RawOutput)
		}
	}
	if out == "" {
		return
	}
	if len(out) > MaxOutput {
		out = "…" + out[len(out)-MaxOutput:]
	}
	for _, p := range res.Parts {
		d, ok := p.Data().(map[string]any)
		if !ok {
			continue
		}
		if tc, ok := d[ToolCallKey].(map[string]any); ok {
			tc["output"] = out
			return
		}
	}
}

func jsonValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// contentText joins the text of a tool call's content: [{"type": "content",
// "content": {"type": "text", "text": ...}}, {"type": "diff", ...}].
func contentText(raw json.RawMessage) string {
	var items []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Path    string          `json:"path"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var out []string
	for _, it := range items {
		switch it.Type {
		case "content":
			if t := chunkText(it.Content); t != "" {
				out = append(out, t)
			}
		case "diff":
			out = append(out, "diff "+it.Path)
		}
	}
	return strings.Join(out, "\n")
}

func chunkText(content json.RawMessage) string {
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &block) != nil || block.Type != "text" {
		return ""
	}
	return block.Text
}
