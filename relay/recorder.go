package relay

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

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
		rec.add(e)
		rec.waiting(e, ev.Status.Message)

	case *a2a.TaskStatusUpdateEvent:
		e := rec.entry(activity.KindTask)
		e.State = string(ev.Status.State)
		e.Summary = Describe(ev.Status.Message)
		if h, ok := ev.Metadata[AwaitingKey].(string); ok {
			e.Data = map[string]any{"awaiting": h}
		}
		if ev.Status.Message != nil {
			if by, ok := ev.Status.Message.Metadata[AnsweredByKey].(string); ok {
				e.Data = map[string]any{"answeredBy": by}
			}
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
