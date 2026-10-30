package console

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/three-agent-problem/lagrange/credentials"
)

// Task is the console's view of one task, rebuilt from the push events it received.
type Task struct {
	ID        a2a.TaskID      `json:"id"`
	ContextID string          `json:"contextId,omitempty"`
	AgentURL  string          `json:"agentUrl"`
	AgentName string          `json:"agentName"`
	State     a2a.TaskState   `json:"state"`
	Status    *a2a.Message    `json:"status,omitempty"`
	Artifacts []*a2a.Artifact `json:"artifacts,omitempty"`
	Events    []Event         `json:"events"`
	ParentID  a2a.TaskID      `json:"parentId,omitempty"`
	Children  []a2a.TaskID    `json:"children,omitempty"`
	FirstSeen time.Time       `json:"firstSeen"`
	LastSeen  time.Time       `json:"lastSeen"`
	// TimeIn is how long the task spent in each state, as observed by the console.
	TimeIn map[a2a.TaskState]time.Duration `json:"timeIn"`

	stateSince time.Time
}

// Event is one entry of a task's timeline.
type Event struct {
	At      time.Time     `json:"at"`
	Kind    string        `json:"kind"`
	State   a2a.TaskState `json:"state,omitempty"`
	Summary string        `json:"summary"`
}

// AgentMessage is a reply that was a plain Message rather than a task.
type AgentMessage struct {
	At        time.Time `json:"at"`
	AgentURL  string    `json:"agentUrl"`
	AgentName string    `json:"agentName"`
	Summary   string    `json:"summary"`
}

// TaskInfo implements a2a.TaskInfoProvider.
func (t *Task) TaskInfo() a2a.TaskInfo { return a2a.TaskInfo{TaskID: t.ID, ContextID: t.ContextID} }

func (t *Task) addEvent(at time.Time, kind string, state a2a.TaskState, summary string) {
	t.Events = append(t.Events, Event{At: at, Kind: kind, State: state, Summary: summary})
	t.LastSeen = at
}

// setState moves the task to state and charges the time spent in the previous state.
func (t *Task) setState(state a2a.TaskState, at time.Time) {
	if state == t.State || state == a2a.TaskStateUnspecified {
		return
	}
	if t.State != a2a.TaskStateUnspecified && !t.stateSince.IsZero() {
		t.TimeIn[t.State] += at.Sub(t.stateSince)
	}
	t.State = state
	t.stateSince = at
}

func (t *Task) mergeArtifact(ev *a2a.TaskArtifactUpdateEvent) {
	if ev.Artifact == nil {
		return
	}
	for _, a := range t.Artifacts {
		if a.ID != ev.Artifact.ID {
			continue
		}
		if ev.Append {
			a.Parts = append(a.Parts, ev.Artifact.Parts...)
		} else {
			*a = *ev.Artifact
		}
		return
	}
	cp := *ev.Artifact
	t.Artifacts = append(t.Artifacts, &cp)
}

// clone returns a copy that is safe to read without the console's lock.
func (t *Task) clone() *Task {
	cp := *t
	cp.Events = append([]Event(nil), t.Events...)
	cp.Children = append([]a2a.TaskID(nil), t.Children...)
	cp.Artifacts = append([]*a2a.Artifact(nil), t.Artifacts...)
	cp.TimeIn = make(map[a2a.TaskState]time.Duration, len(t.TimeIn)+1)
	for k, v := range t.TimeIn {
		cp.TimeIn[k] = v
	}
	// Charge the open interval so durations of live tasks grow on every refresh.
	if !t.State.Terminal() && t.State != a2a.TaskStateUnspecified && !t.stateSince.IsZero() {
		cp.TimeIn[t.State] += time.Since(t.stateSince)
	}
	return &cp
}

// Waiting reports whether the task is interrupted (spec 4.1.3): it needs an answer or a credential.
func (t *Task) Waiting() bool {
	return t.State == a2a.TaskStateInputRequired || t.State == a2a.TaskStateAuthRequired
}

// Duration is the time between the first and the last event seen for the task.
func (t *Task) Duration() time.Duration { return t.LastSeen.Sub(t.FirstSeen) }

// stateLabels names states the way a person waiting on the task thinks about them.
var stateLabels = map[a2a.TaskState]string{
	a2a.TaskStateSubmitted:     "queued",
	a2a.TaskStateWorking:       "working",
	a2a.TaskStateInputRequired: "waiting on a human",
	a2a.TaskStateAuthRequired:  "waiting on a credential",
}

// TimeBreakdown lists non-zero time-in-state entries in a stable order.
func (t *Task) TimeBreakdown() []string {
	var out []string
	for _, s := range []a2a.TaskState{a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired} {
		if d := t.TimeIn[s]; d > 0 {
			out = append(out, fmt.Sprintf("%s %s", stateLabels[s], formatDuration(d)))
		}
	}
	return out
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

// summarize turns a message into one line for the timeline.
func summarize(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var texts []string
	for _, p := range msg.Parts {
		if p == nil || isProtocolPart(p) {
			continue
		}
		if s := p.Text(); s != "" {
			texts = append(texts, s)
		} else if d := p.Data(); d != nil {
			texts = append(texts, compactJSON(d))
		}
	}
	return strings.Join(texts, " ")
}

func summarizeArtifact(a *a2a.Artifact) string {
	if a == nil {
		return ""
	}
	name := a.Name
	if name == "" {
		name = string(a.ID)
	}
	return name + ": " + summarize(&a2a.Message{Parts: a.Parts})
}

// isProtocolPart reports parts meant for software (schemas, challenges), not for people.
func isProtocolPart(p *a2a.Part) bool {
	if _, ok := structuredinput.GetPartMetadata(p); ok {
		return true
	}
	marked, _ := p.Meta()[credentials.MetadataKey].(bool)
	return marked
}

func compactJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func prettyJSON(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
