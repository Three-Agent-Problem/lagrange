package acp

import (
	"encoding/json"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

// Usage is an agent's usage report for its session (the A2A context), as the
// agent sends it: Cost is cumulative over the session, so the session's cost is
// its latest report.
type Usage struct {
	// Used is the number of tokens in the context window, Size the window's size.
	Used int64 `json:"used,omitempty"`
	Size int64 `json:"size,omitempty"`
	// Cost is the session's cost so far (for claude-agent-acp, Claude Code's
	// estimate at API prices).
	Cost *Cost `json:"cost,omitempty"`
}

// Cost is an amount of money.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// usageResult turns a usage_update into a status message with only a usage data
// part, or nil if it carries nothing.
func usageResult(raw rawUpdate) *a2aacp.ConverterResult {
	u := Usage{Used: raw.Used, Size: raw.Size}
	if len(raw.Cost) > 0 && string(raw.Cost) != "null" {
		var c Cost
		if json.Unmarshal(raw.Cost, &c) == nil {
			u.Cost = &c
		}
	}
	if u.Used == 0 && u.Size == 0 && u.Cost == nil {
		return nil
	}
	return &a2aacp.ConverterResult{Parts: []*a2a.Part{a2a.NewDataPart(map[string]any{UsageKey: u.Map()})}}
}

// Map returns u as the JSON object of a usage data part.
func (u Usage) Map() map[string]any {
	m := map[string]any{}
	if u.Used != 0 {
		m["used"] = u.Used
	}
	if u.Size != 0 {
		m["size"] = u.Size
	}
	if u.Cost != nil {
		m["cost"] = map[string]any{"amount": u.Cost.Amount, "currency": u.Cost.Currency}
	}
	return m
}

// ParseUsage reads a usage data part's value ({"used", "size", "cost"}), as
// found in a message or an activity entry's data.
func ParseUsage(v any) (Usage, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Usage{}, false
	}
	var u Usage
	if json.Unmarshal(raw, &u) != nil || (u.Used == 0 && u.Size == 0 && u.Cost == nil) {
		return Usage{}, false
	}
	return u, true
}

// Find returns the value of the first data part of parts with key, or nil.
func Find(parts []*a2a.Part, key string) any {
	for _, p := range parts {
		if d, ok := p.Data().(map[string]any); ok {
			if v, ok := d[key]; ok {
				return v
			}
		}
	}
	return nil
}
