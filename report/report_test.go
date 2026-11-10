package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

var sample = Report{
	Headline: "Add rate limiting to the API",
	Status:   "landed",
	Progress: &Progress{Done: 3, Total: 3},
	Built:    []Item{{Title: "S1 token bucket", Detail: "sonnet · 2 rounds · +120 −4"}, {Title: "S2 docs"}},
	Stats:    []Stat{{Label: "Wall time", Value: "14m"}, {Label: "Cost", Value: "$1.82"}},
	Outcome:  "Merged into main.",
}

func TestRoundTripThroughJSON(t *testing.T) {
	// As the activity log keeps it: the map, through JSON.
	raw, _ := json.Marshal(sample.Map())
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	got, ok := FromMap(v)
	if !ok || got.Headline != sample.Headline || got.Progress.Done != 3 || len(got.Built) != 2 || got.Stats[1].Value != "$1.82" || !got.Final() {
		t.Fatalf("round trip: %+v", got)
	}
	if _, ok := FromMap(map[string]any{"status": "x"}); ok {
		t.Fatal("a report needs a headline")
	}
	if _, ok := FromMap(nil); ok {
		t.Fatal("nil is no report")
	}
}

func TestFindAndEvent(t *testing.T) {
	task := &a2a.Task{ID: "t1", ContextID: "c1"}
	ev := sample.Event(task)
	if ev.Append || !ev.LastChunk || ev.Artifact.ID != ArtifactName || ev.Artifact.Name != ArtifactName || ev.TaskID != "t1" {
		t.Fatalf("event: %+v", ev)
	}
	got, ok := Find(ev.Artifact.Parts)
	if !ok || got.Headline != sample.Headline {
		t.Fatalf("find: %+v %v", got, ok)
	}
	if _, ok := Find([]*a2a.Part{a2a.NewTextPart("hi")}); ok {
		t.Fatal("no report in text")
	}
}

func TestMarkdown(t *testing.T) {
	md := sample.Markdown()
	for _, want := range []string{"**Add rate limiting to the API**", "landed · 3/3 done", "Merged into main.", "- S1 token bucket · sonnet · 2 rounds · +120 −4", "- S2 docs", "Wall time: 14m · Cost: $1.82"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if md := (Report{Headline: "x"}).Markdown(); md != "**x**" {
		t.Fatalf("bare: %q", md)
	}
}
