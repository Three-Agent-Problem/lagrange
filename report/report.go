// Package report is Lagrange's convention for a run's situation report: what a run is
// doing and, at the end, what it built, in a form every Lagrange client can show
// (the Runs page, Telegram, a terminal) without knowing the agent.
//
// An agent publishes the report as an artifact of the run's root task, named
// "report" (ID "report"), with one data part {Key: {...}} (see [Report]). Each new
// report replaces the last one. The relay records it in the activity log as
// data.report of the artifact entry, so a run's latest report is in its entries.
//
// Notice is for the person who started the run: Lagrange forwards each report that
// carries one to their notification channels (Telegram), so set it on the report
// that announces something (a failure, the run landing) and leave it empty on the
// next ones.
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const (
	// ArtifactName is the name of a report artifact, and its ID.
	ArtifactName = "report"
	// Key is the data part key of a report.
	Key = "https://threeagents.dev/ext/report"
)

// Report is a run's situation report.
type Report struct {
	// Headline is what the run is about, in a line: its goal.
	Headline string `json:"headline"`
	// Status is where the run stands, in a word or two ("working", "landed", "failed").
	Status string `json:"status,omitempty"`
	// Progress counts the run's steps, when it has countable ones.
	Progress *Progress `json:"progress,omitempty"`
	// Built is what the run made, one item per piece.
	Built []Item `json:"built,omitempty"`
	// Stats are the numbers worth seeing, in order ("Wall time": "14m").
	Stats []Stat `json:"stats,omitempty"`
	// Outcome is how the run ended, in words.
	Outcome string `json:"outcome,omitempty"`
	// Notice, when set, is forwarded to whoever started the run.
	Notice string    `json:"notice,omitempty"`
	At     time.Time `json:"at"`
}

// Progress is how many of a run's steps are done.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Item is one thing a run built.
type Item struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// Stat is one labeled number.
type Stat struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Final reports whether the report describes a run that ended.
func (r Report) Final() bool { return r.Outcome != "" }

// Map returns r as the JSON object of the report data part.
func (r Report) Map() map[string]any {
	raw, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// Part returns r as a data part.
func (r Report) Part() *a2a.Part {
	return a2a.NewDataPart(map[string]any{Key: r.Map()})
}

// Event returns the artifact event that publishes r on the task info names (the
// run's root task), replacing any earlier report.
func (r Report) Event(info a2a.TaskInfoProvider) *a2a.TaskArtifactUpdateEvent {
	ev := a2a.NewArtifactUpdateEvent(info, ArtifactName, r.Part())
	ev.Append = false
	ev.LastChunk = true
	ev.Artifact.Name = ArtifactName
	return ev
}

// Find returns the report in parts.
func Find(parts []*a2a.Part) (Report, bool) {
	for _, p := range parts {
		if d, ok := p.Data().(map[string]any); ok {
			if r, ok := FromMap(d[Key]); ok {
				return r, true
			}
		}
	}
	return Report{}, false
}

// FromMap reads a report from its JSON object (as the activity log keeps it).
func FromMap(v any) (Report, bool) {
	if v == nil {
		return Report{}, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return Report{}, false
	}
	var r Report
	if json.Unmarshal(raw, &r) != nil || strings.TrimSpace(r.Headline) == "" {
		return Report{}, false
	}
	return r, true
}

// Markdown renders the report to paste anywhere: the headline, status and progress,
// the outcome, a bullet per built item, then the stats.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", strings.TrimSpace(r.Headline))
	if line := r.statusLine(); line != "" {
		fmt.Fprintf(&b, "%s\n", line)
	}
	if r.Outcome != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(r.Outcome))
	}
	if len(r.Built) > 0 {
		b.WriteString("\n**Built**\n")
		for _, it := range r.Built {
			if it.Detail != "" {
				fmt.Fprintf(&b, "- %s · %s\n", it.Title, it.Detail)
			} else {
				fmt.Fprintf(&b, "- %s\n", it.Title)
			}
		}
	}
	if len(r.Stats) > 0 {
		b.WriteString("\n")
		parts := make([]string, len(r.Stats))
		for i, s := range r.Stats {
			parts[i] = s.Label + ": " + s.Value
		}
		b.WriteString(strings.Join(parts, " · ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r Report) statusLine() string {
	var parts []string
	if r.Status != "" {
		parts = append(parts, r.Status)
	}
	if p := r.Progress; p != nil && p.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d done", p.Done, p.Total))
	}
	return strings.Join(parts, " · ")
}
