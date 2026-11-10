// Package ask is Lagrange's convention for questions an agent puts to a person:
// multiple-choice questions, like Claude Code's plan-mode questions (each has a few
// options to pick one or several of, and the person can write their own answer in
// clients that allow it), and approvals (a yes/no on what the message says).
//
// Both travel in the message metadata, next to a plain text rendering, so a client
// that knows neither still shows something to answer:
//   - MetaKey holds the questions; the answer is a text message, one line per
//     question (see Format).
//   - ApprovalKey (true) marks an approval; the answer is the decision "approve" or
//     "reject" (POST /api/tasks/{id}/answer), which completes or rejects the task.
//
// Clients with buttons (the web inbox, Telegram) render both as buttons.
package ask

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	// MetaKey is the message metadata key that carries the questions.
	MetaKey = "https://threeagents.dev/ext/questions"
	// LegacyMetaKey is the key Halo used before the questions moved to Lagrange; Parse
	// still reads it.
	LegacyMetaKey = "https://threeagents.dev/ext/halo/questions"
	// ApprovalKey marks a message that asks to approve or reject (metadata true).
	ApprovalKey = "https://threeagents.dev/ext/approval"
)

// ApprovalField returns the boolean field of a structured input form that a plain
// approve or reject can answer: the form has exactly one boolean field, and every
// other field is optional (an approval with an optional note).
func ApprovalField(schema *jsonschema.Schema) (string, bool) {
	if schema == nil {
		return "", false
	}
	field := ""
	for name, prop := range schema.Properties {
		if prop != nil && prop.Type == "boolean" {
			if field != "" {
				return "", false
			}
			field = name
		}
	}
	if field == "" {
		return "", false
	}
	for _, r := range schema.Required {
		if r != field {
			return "", false
		}
	}
	return field, true
}

// IsApproval reports whether metadata marks an approval.
func IsApproval(meta map[string]any) bool {
	v, _ := meta[ApprovalKey].(bool)
	return v
}

// Limits keep a form small enough to answer at a glance.
const (
	MaxQuestions = 4
	MinOptions   = 2
	MaxOptions   = 5
)

// Option is one answer to pick.
type Option struct {
	Label       string `json:"label" jsonschema:"1-5 words; put the recommended option first and end its label with (Recommended)"`
	Description string `json:"description,omitempty" jsonschema:"one sentence: what picking it means, its trade-off"`
}

// Question is one question of a form.
type Question struct {
	Header      string   `json:"header" jsonschema:"a 1-2 word tag for the question, like Auth or Scope"`
	Question    string   `json:"question" jsonschema:"the whole question, ending with a question mark"`
	Options     []Option `json:"options" jsonschema:"2-5 distinct choices; the human can always write their own instead"`
	MultiSelect bool     `json:"multi_select,omitempty" jsonschema:"true when several options can apply together"`
}

// Validate checks a form against the limits.
func Validate(qs []Question) error {
	if len(qs) == 0 {
		return errors.New("no questions")
	}
	if len(qs) > MaxQuestions {
		return fmt.Errorf("at most %d questions, got %d", MaxQuestions, len(qs))
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("question %d has no text", i+1)
		}
		if len(q.Options) < MinOptions || len(q.Options) > MaxOptions {
			return fmt.Errorf("question %d needs %d-%d options, got %d", i+1, MinOptions, MaxOptions, len(q.Options))
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			l := strings.ToLower(strings.TrimSpace(o.Label))
			if l == "" || seen[l] {
				return fmt.Errorf("question %d has an empty or repeated option", i+1)
			}
			seen[l] = true
		}
	}
	return nil
}

// Text renders the questions as plain text, for clients without the form.
func Text(qs []Question) string {
	var b strings.Builder
	for i, q := range qs {
		if i > 0 {
			b.WriteString("\n")
		}
		head := q.Header
		if head == "" {
			head = fmt.Sprintf("Q%d", i+1)
		}
		fmt.Fprintf(&b, "%s: %s", head, q.Question)
		if q.MultiSelect {
			b.WriteString(" (pick any)")
		}
		b.WriteString("\n")
		for j, o := range q.Options {
			fmt.Fprintf(&b, "  %d. %s", j+1, o.Label)
			if o.Description != "" {
				b.WriteString(" — " + o.Description)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Parse reads the questions from message metadata, or nil.
func Parse(meta map[string]any) []Question {
	v, ok := meta[MetaKey]
	if !ok {
		if v, ok = meta[LegacyMetaKey]; !ok {
			return nil
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var qs []Question
	if json.Unmarshal(raw, &qs) != nil || Validate(qs) != nil {
		return nil
	}
	return qs
}

// Answer is the human's answer to one question: the options picked (by
// index), and their own words, which may stand alone or add to the picks.
type Answer struct {
	Picked []int
	Other  string
}

// Format renders the answers for the lead, one line per question.
func Format(qs []Question, as []Answer) string {
	var lines []string
	for i, q := range qs {
		head := q.Header
		if head == "" {
			head = q.Question
		}
		var a Answer
		if i < len(as) {
			a = as[i]
		}
		var picks []string
		for _, p := range a.Picked {
			if p >= 0 && p < len(q.Options) {
				picks = append(picks, q.Options[p].Label)
			}
		}
		other := strings.TrimSpace(a.Other)
		switch {
		case len(picks) == 0 && other == "":
			lines = append(lines, head+": (no answer, your call)")
		case len(picks) == 0:
			lines = append(lines, head+": "+other)
		case other == "":
			lines = append(lines, head+": "+strings.Join(picks, ", "))
		default:
			lines = append(lines, head+": "+strings.Join(picks, ", ")+" (note: "+other+")")
		}
	}
	return strings.Join(lines, "\n")
}
