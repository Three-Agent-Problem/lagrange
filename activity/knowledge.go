package activity

import (
	"slices"
	"strings"
	"time"
)

// Knowledge is what one task of a run knows, as its log shows it: the files its agent
// read or changed, when it last did something, and how full its context was. An
// orchestrator uses it to send a question to the agent that already read the code
// (while its prompt cache is still warm) instead of having another agent read it again.
type Knowledge struct {
	TaskID     string      `json:"taskId"`
	Agent      string      `json:"agent"`
	LastActive time.Time   `json:"lastActive"`
	Context    int64       `json:"context,omitempty"` // tokens in context at the last usage report
	Files      []FileTouch `json:"files,omitempty"`   // by path
}

// FileTouch is one file a task read or changed.
type FileTouch struct {
	Path    string    `json:"path"`
	Read    bool      `json:"read,omitempty"`
	Changed bool      `json:"changed,omitempty"`
	At      time.Time `json:"at"` // the last time
}

// pathKeys are the tool call input keys that hold a file path (Claude Code's read and
// edit tools, and the ACP file tools).
var pathKeys = []string{"file_path", "path", "notebook_path"}

// changeKinds are the tool call kinds that change a file.
var changeKinds = map[string]bool{"edit": true, "delete": true, "move": true}

// KnowledgeOf builds the knowledge of every task in a run's entries (as RunDetail
// returns them), most recently active first.
func KnowledgeOf(entries []*Entry) []Knowledge {
	type call struct {
		task, kind, path string
		at               time.Time
	}
	byTask := map[string]*Knowledge{}
	get := func(e *Entry) *Knowledge {
		k := byTask[e.TaskID]
		if k == nil {
			k = &Knowledge{TaskID: e.TaskID, Agent: e.Recipient}
			byTask[e.TaskID] = k
		}
		if e.At.After(k.LastActive) {
			k.LastActive = e.At
		}
		return k
	}
	calls := map[string]*call{} // task + " " + call id
	var order []string
	for _, e := range entries {
		if e == nil || e.TaskID == "" {
			continue
		}
		switch e.Kind {
		case KindUsage:
			k := get(e)
			if u, ok := e.Data["usage"].(map[string]any); ok {
				if n, ok := u["used"].(float64); ok {
					k.Context = int64(n)
				}
			}
		case KindTask:
			tc, ok := e.Data["toolCall"].(map[string]any)
			if !ok {
				continue
			}
			get(e)
			id, _ := tc["id"].(string)
			if id == "" {
				continue
			}
			key := e.TaskID + " " + id
			c := calls[key]
			if c == nil {
				c = &call{task: e.TaskID}
				calls[key] = c
				order = append(order, key)
			}
			if s, _ := tc["kind"].(string); s != "" {
				c.kind = s
			}
			if in, ok := tc["input"].(map[string]any); ok {
				for _, k := range pathKeys {
					if p, _ := in[k].(string); p != "" {
						c.path = p
						break
					}
				}
			}
			if e.At.After(c.at) {
				c.at = e.At
			}
		}
	}
	files := map[string]map[string]*FileTouch{} // task -> path -> touch
	for _, key := range order {
		c := calls[key]
		if c.path == "" || (c.kind != "read" && !changeKinds[c.kind]) {
			continue
		}
		if files[c.task] == nil {
			files[c.task] = map[string]*FileTouch{}
		}
		f := files[c.task][c.path]
		if f == nil {
			f = &FileTouch{Path: c.path}
			files[c.task][c.path] = f
		}
		if c.kind == "read" {
			f.Read = true
		} else {
			f.Changed = true
		}
		if c.at.After(f.At) {
			f.At = c.at
		}
	}
	out := make([]Knowledge, 0, len(byTask))
	for id, k := range byTask {
		for _, f := range files[id] {
			k.Files = append(k.Files, *f)
		}
		slices.SortFunc(k.Files, func(a, b FileTouch) int { return strings.Compare(a.Path, b.Path) })
		out = append(out, *k)
	}
	slices.SortFunc(out, func(a, b Knowledge) int {
		if c := b.LastActive.Compare(a.LastActive); c != 0 {
			return c
		}
		return strings.Compare(a.TaskID, b.TaskID)
	})
	return out
}

// Touched reports whether the task read or changed a file whose path ends with path
// (compared by whole path elements, so "store.go" matches "/repo/store/store.go" but
// "re.go" doesn't), and returns that file.
func (k Knowledge) Touched(path string) (FileTouch, bool) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return FileTouch{}, false
	}
	for _, f := range k.Files {
		if f.Path == path || strings.HasSuffix(f.Path, "/"+path) {
			return f, true
		}
	}
	return FileTouch{}, false
}
