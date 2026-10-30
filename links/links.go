// Package links is a small metadata convention for traces that cross agents.
//
// When an orchestrator delegates work, it marks one of its own status updates with the
// downstream task it created. The console reads the mark and draws the parent → child
// tree. Nothing here is part of the A2A spec; it rides in ordinary event metadata, so
// agents that don't know about it simply ignore it.
package links

import (
	"encoding/json"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// MetadataKey is the metadata key that carries a Child.
const MetadataKey = "https://threeagents.dev/ext/links/v1"

// Child points at a task another agent runs on behalf of this one.
type Child struct {
	// AgentURL is the base URL of the downstream agent.
	AgentURL string     `json:"agentUrl"`
	TaskID   a2a.TaskID `json:"taskId"`
	Skill    string     `json:"skill,omitempty"`
}

// Set records child in the metadata of an event, task or message.
func Set(carrier interface{ SetMeta(string, any) }, child Child) {
	value := map[string]any{"agentUrl": child.AgentURL, "taskId": string(child.TaskID)}
	if child.Skill != "" {
		value["skill"] = child.Skill
	}
	carrier.SetMeta(MetadataKey, value)
}

// Get reads a Child from metadata, which usually arrived as JSON.
func Get(meta map[string]any) (Child, bool) {
	value, ok := meta[MetadataKey]
	if !ok {
		return Child{}, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Child{}, false
	}
	var child Child
	if err := json.Unmarshal(raw, &child); err != nil || child.TaskID == "" {
		return Child{}, false
	}
	return child, true
}
