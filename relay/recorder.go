package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/acp"
	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
)

// recorder writes a task's events to the activity log and its link on creation.
type recorder struct {
	r       *Relay
	ctx     context.Context
	ec      *a2asrv.ExecutorContext
	addr    *directory.Address
	sender  string
	parent  a2a.TaskID
	started bool
}

func (r *Relay) recorder(ctx context.Context, ec *a2asrv.ExecutorContext, addr *directory.Address) *recorder {
	rec := &recorder{r: r, ctx: ctx, ec: ec, addr: addr}
	if link, _ := r.Links.Get(ctx, ec.TaskID); link != nil {
		// A resumed task keeps its original sender and parent.
		rec.sender, rec.parent = link.Sender, link.ParentTaskID
		return rec
	}
	if ec.User != nil {
		rec.sender = ec.User.Name
	}
	if values, ok := ec.ServiceParams.Get(HeaderTask); ok && len(values) > 0 {
		if parent := a2a.TaskID(values[0]); r.Links.ValidParent(ctx, parent, rec.sender) {
			rec.parent = parent
		}
	}
	return rec
}

func (rec *recorder) entry(kind string) *activity.Entry {
	return &activity.Entry{
		Kind:         kind,
		TaskID:       string(rec.ec.TaskID),
		ParentTaskID: string(rec.parent),
		ContextID:    rec.ec.ContextID,
		Sender:       rec.sender,
		Recipient:    rec.addr.Handle,
	}
}

func (rec *recorder) add(e *activity.Entry) {
	if err := rec.r.Activity.Add(rec.ctx, e); err != nil {
		rec.r.log().Warn("relay: activity log write failed", "error", err)
	}
}

// input logs a message that continues the task, from who (the sender, or the person
// answering on their behalf).
func (rec *recorder) input(m *a2a.Message, who string) {
	e := rec.entry(activity.KindInput)
	e.Summary = Describe(m)
	e.Data = map[string]any{"from": who}
	if c := Preview(m.Parts, 4096); c != "" {
		e.Data["content"] = c
	}
	if t := Tags(m); t != nil {
		e.Data["tags"] = t
	}
	rec.add(e)
}

// waiting reports a task that stopped on someone to OnWaiting.
func (rec *recorder) waiting(e *activity.Entry, question *a2a.Message) {
	if rec.r.OnWaiting != nil && e.Seq != 0 && interrupted(a2a.TaskState(e.State)) {
		rec.r.OnWaiting(rec.ctx, Waiting{Entry: e, Question: question})
	}
}

// note logs something that isn't an A2A event, such as waiting in the mailbox.
func (rec *recorder) note(kind, summary string) {
	e := rec.entry(kind)
	e.Summary = summary
	rec.add(e)
}

func (rec *recorder) record(ev a2a.Event) {
	switch ev := ev.(type) {
	case *a2a.Task:
		rec.started = true
		if err := rec.r.Links.Create(rec.ctx, &Link{TaskID: ev.ID, Sender: rec.sender, Recipient: rec.addr.Handle, ParentTaskID: rec.parent}); err != nil {
			rec.r.log().Warn("relay: storing the task link failed", "task", ev.ID, "error", err)
		}
		e := rec.entry(activity.KindTask)
		e.State = string(ev.Status.State)
		e.Summary = Describe(rec.ec.Message)
		if t := Tags(rec.ec.Message); t != nil {
			e.Data = map[string]any{"tags": t}
		}
		rec.add(e)
		rec.waiting(e, ev.Status.Message)

	case *a2a.TaskStatusUpdateEvent:
		if u := usageOf(ev.Status.Message); u != nil {
			// A usage report (lagrange/acp): its own kind, so it isn't a step.
			e := rec.entry(activity.KindUsage)
			e.Summary = usageSummary(u)
			e.Data = map[string]any{"usage": u}
			if d, ok := rec.costDelta(u); ok {
				e.Data["costDelta"] = d
			}
			rec.add(e)
			return
		}
		e := rec.entry(activity.KindTask)
		e.State = string(ev.Status.State)
		e.Summary = Describe(ev.Status.Message)
		data := map[string]any{}
		if h, ok := ev.Metadata[AwaitingKey].(string); ok {
			data["awaiting"] = h
		}
		if m := ev.Status.Message; m != nil {
			if by, ok := m.Metadata[AnsweredByKey].(string); ok {
				data["answeredBy"] = by
			}
			if tc, ok := acp.Find(m.Parts, acp.ToolCallKey).(map[string]any); ok {
				data["toolCall"] = capToolCall(tc)
			}
		}
		if len(data) > 0 {
			e.Data = data
		}
		rec.add(e)
		question := ev.Status.Message
		if rec.addr.Kind == directory.KindHuman {
			question = rec.ec.Message
		}
		rec.waiting(e, question)

	case *a2a.TaskArtifactUpdateEvent:
		e := rec.entry(activity.KindArtifact)
		if ev.Artifact != nil {
			e.Summary = ev.Artifact.Name
			e.Data = map[string]any{"artifactId": string(ev.Artifact.ID), "append": ev.Append, "lastChunk": ev.LastChunk}
			if c := Preview(ev.Artifact.Parts, 4096); c != "" {
				e.Data["content"] = c
			}
		}
		rec.add(e)

	case *a2a.Message:
		e := rec.entry(activity.KindMessage)
		e.TaskID = ""
		e.Summary = Describe(rec.ec.Message)
		// callId is the task ID the call would have had: its exchanges are recorded under it.
		e.Data = map[string]any{"reply": Describe(ev), "callId": string(rec.ec.TaskID)}
		if c := Preview(rec.ec.Message.Parts, 4096); c != "" {
			e.Data["request"] = c
		}
		if c := Preview(ev.Parts, 4096); c != "" {
			e.Data["replyContent"] = c
		}
		if t := Tags(rec.ec.Message); t != nil {
			e.Data["tags"] = t
		}
		rec.add(e)
	}
}

// Text joins the text parts of a message.
func Text(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, p := range m.Parts {
		if t := p.Text(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// Describe summarizes a message for the activity log: its text, or else its data and
// files, shortened.
func Describe(m *a2a.Message) string {
	if t := Text(m); t != "" || m == nil {
		return t
	}
	var parts []string
	for _, p := range m.Parts {
		switch {
		case p.Data() != nil:
			raw, _ := json.Marshal(p.Data())
			parts = append(parts, string(raw))
		case p.Filename != "":
			parts = append(parts, p.Filename)
		}
	}
	s := strings.Join(parts, " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}

// Preview renders parts for the activity log, up to n runes: text as is, data as JSON,
// files by name.
func Preview(parts []*a2a.Part, n int) string {
	var out []string
	for _, p := range parts {
		switch {
		case p.Text() != "":
			out = append(out, p.Text())
		case p.Data() != nil:
			raw, _ := json.Marshal(p.Data())
			out = append(out, string(raw))
		case p.Filename != "":
			out = append(out, "file: "+p.Filename)
		}
	}
	s := strings.Join(out, "\n")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// usageOf returns the usage report of a status message that carries only that, or nil.
func usageOf(m *a2a.Message) map[string]any {
	if m == nil || len(m.Parts) != 1 {
		return nil
	}
	u, _ := acp.Find(m.Parts, acp.UsageKey).(map[string]any)
	if _, ok := acp.ParseUsage(u); !ok {
		return nil
	}
	return u
}

// usageSummary says a usage report in a few words: "$0.42 · 26% of context".
func usageSummary(v map[string]any) string {
	u, _ := acp.ParseUsage(v)
	var parts []string
	if u.Cost != nil {
		if u.Cost.Currency == "" || u.Cost.Currency == "USD" {
			parts = append(parts, fmt.Sprintf("$%.2f", u.Cost.Amount))
		} else {
			parts = append(parts, fmt.Sprintf("%.2f %s", u.Cost.Amount, u.Cost.Currency))
		}
	}
	if u.Size > 0 {
		parts = append(parts, fmt.Sprintf("%d%% of context", u.Used*100/u.Size))
	} else if u.Used > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens", u.Used))
	}
	return "usage: " + strings.Join(parts, " · ")
}

// maxToolField caps a tool call's input and output in the activity log.
const maxToolField = 4096

// capToolCall returns tc with its input and output cut to what the log keeps.
func capToolCall(tc map[string]any) map[string]any {
	out := make(map[string]any, len(tc))
	for k, v := range tc {
		out[k] = v
	}
	if s, ok := out["output"].(string); ok && len(s) > maxToolField {
		out["output"] = "…" + s[len(s)-maxToolField:]
	}
	if in, ok := out["input"]; ok {
		if raw, err := json.Marshal(in); err == nil && len(raw) > maxToolField {
			out["input"] = string(raw[:maxToolField]) + "…"
		}
	}
	return out
}

// costDelta is how much the session cost since its previous usage report: costs are
// cumulative per session, and a session can span runs.
func (rec *recorder) costDelta(v map[string]any) (float64, bool) {
	u, _ := acp.ParseUsage(v)
	if u.Cost == nil {
		return 0, false
	}
	delta := u.Cost.Amount
	prev, err := rec.r.Activity.LastUsage(rec.ctx, rec.addr.Handle, rec.ec.ContextID)
	if err != nil {
		rec.r.log().Warn("relay: reading the last usage report failed", "error", err)
		return 0, false
	}
	if prev != nil {
		if p, ok := acp.ParseUsage(prev.Data["usage"]); ok && p.Cost != nil && p.Cost.Currency == u.Cost.Currency && p.Cost.Amount <= u.Cost.Amount {
			delta -= p.Cost.Amount
		}
	}
	return delta, true
}

// Tags returns the tags of a message (TagsKey in its metadata), cut to the limits the
// activity log keeps, or nil. Values that aren't strings are written as JSON.
func Tags(m *a2a.Message) map[string]string {
	if m == nil {
		return nil
	}
	raw, ok := m.Metadata[TagsKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := map[string]string{}
	for _, k := range keys {
		if len(out) == maxTags {
			break
		}
		if k == "" || len([]rune(k)) > 64 {
			continue
		}
		var v string
		switch x := raw[k].(type) {
		case string:
			v = x
		case nil:
			continue
		default:
			b, _ := json.Marshal(x)
			v = string(b)
		}
		if r := []rune(v); len(r) > 256 {
			v = string(r[:255]) + "…"
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const maxTags = 16
