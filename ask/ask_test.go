package ask

import (
	"encoding/json"
	"strings"
	"testing"
)

var form = []Question{
	{Header: "Auth", Question: "How do users sign in?", Options: []Option{{Label: "OAuth (Recommended)", Description: "GitHub and Google"}, {Label: "Email link"}}},
	{Header: "Scope", Question: "Which surfaces?", MultiSelect: true, Options: []Option{{Label: "API"}, {Label: "CLI"}, {Label: "Web"}}},
}

func TestRoundTrip(t *testing.T) {
	// Through JSON, as the metadata of an A2A message.
	raw, _ := json.Marshal(map[string]any{MetaKey: form})
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	got := Parse(meta)
	if len(got) != 2 || got[1].Options[2].Label != "Web" || !got[1].MultiSelect {
		t.Fatalf("parse: %+v", got)
	}
	if len(Parse(map[string]any{LegacyMetaKey: form})) != 2 {
		t.Fatal("the legacy key still parses")
	}
	if !IsApproval(map[string]any{ApprovalKey: true}) || IsApproval(map[string]any{ApprovalKey: "yes"}) || IsApproval(nil) {
		t.Fatal("approval marker")
	}
	if Parse(map[string]any{}) != nil || Parse(map[string]any{MetaKey: "junk"}) != nil {
		t.Fatal("no form, no questions")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(form); err != nil {
		t.Fatal(err)
	}
	bad := [][]Question{
		nil,
		{{Question: "x?", Options: []Option{{Label: "a"}}}},
		{{Question: "x?", Options: []Option{{Label: "a"}, {Label: "A"}}}},
		{{Question: " ", Options: []Option{{Label: "a"}, {Label: "b"}}}},
		{form[0], form[0], form[0], form[0], form[0]},
	}
	for i, qs := range bad {
		if Validate(qs) == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}

func TestTextAndFormat(t *testing.T) {
	txt := Text(form)
	for _, want := range []string{"Auth: How do users sign in?", "1. OAuth (Recommended) — GitHub and Google", "Scope: Which surfaces? (pick any)", "3. Web"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	got := Format(form, []Answer{{Picked: []int{0}}, {Picked: []int{0, 2}, Other: "CLI later"}})
	want := "Auth: OAuth (Recommended)\nScope: API, Web (note: CLI later)"
	if got != want {
		t.Fatalf("format:\n%s\nwant:\n%s", got, want)
	}
	if got := Format(form, []Answer{{Other: "magic links only"}}); got != "Auth: magic links only\nScope: (no answer, your call)" {
		t.Fatalf("other only: %q", got)
	}
}
