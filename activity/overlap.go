package activity

import (
	"encoding/json"
	"slices"
	"strings"
)

// OverlapReport says how much work the agents of one run repeated: the same tool
// call (same kind and input) made by several agents, or by one agent several times.
// It reads only what the log keeps, so output sizes are capped (see the relay's
// tool-call cap) and the token estimate is a lower bound.
type OverlapReport struct {
	Agents  []AgentWork `json:"agents"`
	Repeats []Repeat    `json:"repeats"`
	// Calls is the number of distinct tool calls in the run.
	Calls int `json:"calls"`
	// RepeatedCalls is how many calls repeat an earlier call with the same key.
	RepeatedCalls int `json:"repeatedCalls"`
	// RepeatedTokens estimates the output tokens those repeated calls read again
	// (logged output bytes / 4).
	RepeatedTokens int `json:"repeatedTokens"`
	// Cost is the run's total cost from usage reports.
	Cost float64 `json:"cost"`
}

// AgentWork is one agent's share of a run.
type AgentWork struct {
	Agent string  `json:"agent"`
	Calls int     `json:"calls"`
	Cost  float64 `json:"cost"`
}

// Repeat is one tool call made more than once.
type Repeat struct {
	Key    string         `json:"key"`
	Kind   string         `json:"kind"`
	Title  string         `json:"title"`
	Times  int            `json:"times"`
	Agents map[string]int `json:"agents"`
	// Tokens estimates the output tokens of one call (output bytes / 4).
	Tokens int `json:"tokens"`
}

// CrossAgent reports whether more than one agent made the call.
func (r Repeat) CrossAgent() bool { return len(r.Agents) > 1 }

// Overlap builds the report from a run's entries (as RunDetail returns them).
func Overlap(entries []*Entry) OverlapReport {
	type call struct {
		agent, kind, title, key string
		output                  int
	}
	calls := map[string]*call{} // agent + " " + call id
	var order []string
	costs := map[string]float64{}
	for _, e := range entries {
		if e == nil {
			continue
		}
		switch e.Kind {
		case KindUsage:
			if d, ok := e.Data["costDelta"].(float64); ok {
				costs[e.Recipient] += d
			}
		case KindTask:
			tc, ok := e.Data["toolCall"].(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			if id == "" {
				continue
			}
			k := e.Recipient + " " + id
			c := calls[k]
			if c == nil {
				c = &call{agent: e.Recipient}
				calls[k] = c
				order = append(order, k)
			}
			if s, _ := tc["kind"].(string); s != "" {
				c.kind = s
			}
			if s, _ := tc["title"].(string); s != "" {
				c.title = s
			}
			if in, ok := tc["input"]; ok && in != nil {
				c.key = inputKey(in)
			}
			if s, _ := tc["output"].(string); len(s) > c.output {
				c.output = len(s)
			}
		}
	}

	var rep OverlapReport
	work := map[string]*AgentWork{}
	agentOf := func(a string) *AgentWork {
		if work[a] == nil {
			work[a] = &AgentWork{Agent: a}
		}
		return work[a]
	}
	for a, c := range costs {
		agentOf(a).Cost = c
		rep.Cost += c
	}
	repeats := map[string]*Repeat{}
	var keys []string
	for _, k := range order {
		c := calls[k]
		agentOf(c.agent).Calls++
		rep.Calls++
		if c.key == "" {
			continue // no input logged: nothing to compare
		}
		key := c.kind + " " + c.key
		r := repeats[key]
		if r == nil {
			r = &Repeat{Key: c.key, Kind: c.kind, Title: c.title, Agents: map[string]int{}}
			repeats[key] = r
			keys = append(keys, key)
		}
		r.Times++
		r.Agents[c.agent]++
		if t := c.output / 4; t > r.Tokens {
			r.Tokens = t
		}
	}
	for _, k := range keys {
		r := repeats[k]
		if r.Times < 2 {
			continue
		}
		rep.Repeats = append(rep.Repeats, *r)
		rep.RepeatedCalls += r.Times - 1
		rep.RepeatedTokens += (r.Times - 1) * r.Tokens
	}
	slices.SortStableFunc(rep.Repeats, func(a, b Repeat) int {
		if a.CrossAgent() != b.CrossAgent() {
			if a.CrossAgent() {
				return -1
			}
			return 1
		}
		return (b.Times-1)*b.Tokens - (a.Times-1)*a.Tokens
	})
	for _, w := range work {
		rep.Agents = append(rep.Agents, *w)
	}
	slices.SortFunc(rep.Agents, func(a, b AgentWork) int { return strings.Compare(a.Agent, b.Agent) })
	return rep
}

// inputKey is a tool call's input as a comparable string: JSON with sorted keys.
func inputKey(in any) string {
	if s, ok := in.(string); ok {
		return s
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return string(raw)
}
