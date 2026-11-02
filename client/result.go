package client

import (
	"context"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// AnsweredByKey is the message metadata key that says who answered from the inbox.
const AnsweredByKey = "https://threeagents.dev/ext/answered-by/v1"

// Result is a task's state and what it answered, flattened for people, scripts and
// models.
type Result struct {
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId,omitempty"`
	State     string `json:"state" jsonschema:"submitted, working, input-required, auth-required, completed, failed, canceled or rejected"`
	// Done is true once the task reached a final state.
	Done bool `json:"done"`
	// Text is the latest status message and any text artifacts.
	Text string `json:"text,omitempty"`
	// Data holds structured parts of the answer.
	Data       []any  `json:"data,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	// WaitingOn is the address a waiting task waits for.
	WaitingOn string `json:"waitingOn,omitempty"`
	Note      string `json:"note,omitempty"`
}

// ShortState turns "TASK_STATE_INPUT_REQUIRED" into "input-required".
func ShortState(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "TASK_STATE_")), "_", "-")
}

// Waiting reports whether a task state waits for someone.
func Waiting(s a2a.TaskState) bool {
	return s == a2a.TaskStateInputRequired || s == a2a.TaskStateAuthRequired
}

// WaitsOn returns who a waiting task waits for: the person it was addressed to
// (awaiting), or else its sender, whom the agent asked.
func (t *Task) WaitsOn() string {
	if t.Task == nil || !Waiting(t.Task.Status.State) {
		return ""
	}
	if t.Awaiting != "" {
		return t.Awaiting
	}
	return t.Sender
}

// Result flattens the task.
func (t *Task) Result() *Result {
	if t.Task == nil {
		return &Result{TaskID: t.ID, ContextID: t.ContextID, State: ShortState(t.State)}
	}
	r := ResultOf(t.Task)
	r.WaitingOn = t.WaitsOn()
	if t.Awaiting != "" && Waiting(t.Task.Status.State) {
		r.Text = "" // the relay's "Waiting for … to answer." placeholder, not an answer
	}
	return r
}

// ResultOf flattens an A2A task.
func ResultOf(t *a2a.Task) *Result {
	r := &Result{TaskID: string(t.ID), ContextID: t.ContextID, State: ShortState(string(t.Status.State)), Done: t.Status.State.Terminal()}
	var texts []string
	// add takes one message's or artifact's parts. Its text parts are joined as they
	// are: a streamed answer (an ACP agent's, say) arrives as one part per chunk.
	add := func(parts []*a2a.Part) {
		var b strings.Builder
		for _, p := range parts {
			if p == nil {
				continue
			}
			if tx := p.Text(); tx != "" {
				b.WriteString(tx)
			} else if d := p.Data(); d != nil {
				r.Data = append(r.Data, d)
			}
		}
		if b.Len() > 0 {
			texts = append(texts, b.String())
		}
	}
	if m := t.Status.Message; m != nil {
		add(m.Parts)
		if by, ok := m.Metadata[AnsweredByKey].(string); ok {
			r.AnsweredBy = by
		}
	}
	for _, a := range t.Artifacts {
		add(a.Parts)
	}
	r.Text = strings.Join(texts, "\n")
	return r
}

// MessageResult flattens a direct answer that started no task.
func MessageResult(m *a2a.Message) *Result {
	r := &Result{State: "completed", Done: true, Note: "The address answered directly, without a task."}
	var texts []string
	for _, p := range m.Parts {
		if tx := p.Text(); tx != "" {
			texts = append(texts, tx)
		} else if d := p.Data(); d != nil {
			r.Data = append(r.Data, d)
		}
	}
	r.Text = strings.Join(texts, "\n")
	return r
}

// Follow polls a task every poll (zero: one second) until stop says so or wait runs
// out, and returns the task as it last saw it, and whether stop said so.
func (c *Client) Follow(ctx context.Context, id string, wait, poll time.Duration, stop func(*Task) bool) (*Task, bool, error) {
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		t, err := c.Task(ctx, id)
		if err != nil {
			return nil, false, err
		}
		if t.Task != nil && stop(t) {
			return t, true, nil
		}
		if !time.Now().Before(deadline) {
			return t, false, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(min(poll, time.Until(deadline)+time.Millisecond)):
		}
	}
}
