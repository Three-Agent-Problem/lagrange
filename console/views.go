package console

import (
	"net/http"
	"net/url"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// node is a task with its delegated tasks, for the tree view.
type node struct {
	Task     *Task
	Children []*node
}

func (c *Console) tree() []*node {
	tasks := c.Tasks()
	byID := make(map[a2a.TaskID]*Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var build func(t *Task, depth int) *node
	build = func(t *Task, depth int) *node {
		n := &node{Task: t}
		if depth > 16 { // guard against a cycle in agent-supplied links
			return n
		}
		for _, id := range t.Children {
			if child, ok := byID[id]; ok {
				n.Children = append(n.Children, build(child, depth+1))
			}
		}
		return n
	}
	var roots []*node
	for _, t := range tasks {
		if _, hasParent := byID[t.ParentID]; t.ParentID == "" || !hasParent {
			roots = append(roots, build(t, 0))
		}
	}
	return roots
}

// visiblePart is a message or artifact part rendered for people.
type visiblePart struct {
	Text string
	JSON string
	URL  string
}

func visibleParts(parts []*a2a.Part) []visiblePart {
	var out []visiblePart
	for _, p := range parts {
		if p == nil || isProtocolPart(p) {
			continue
		}
		switch {
		case p.Text() != "":
			out = append(out, visiblePart{Text: p.Text()})
		case p.Data() != nil:
			out = append(out, visiblePart{JSON: prettyJSON(p.Data())})
		case p.URL() != "":
			out = append(out, visiblePart{URL: string(p.URL())})
		}
	}
	return out
}

func (c *Console) handleIndex(w http.ResponseWriter, r *http.Request) {
	c.render(w, "index.html", map[string]any{"Roots": c.tree(), "Messages": c.Messages(), "Waiting": len(c.inboxItems())})
}

func (c *Console) handleTask(w http.ResponseWriter, r *http.Request) {
	t, ok := c.Task(a2a.TaskID(r.PathValue("id")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var children []*Task
	for _, id := range t.Children {
		if child, ok := c.Task(id); ok {
			children = append(children, child)
		}
	}
	var parent *Task
	if t.ParentID != "" {
		parent, _ = c.Task(t.ParentID)
	}
	c.render(w, "task.html", map[string]any{"Task": t, "Children": children, "Parent": parent})
}

func (c *Console) render(w http.ResponseWriter, name string, data map[string]any) {
	c.mu.Lock()
	data["Nav"] = append([]navLink(nil), c.nav...)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.pages.ExecuteTemplate(w, name, data); err != nil {
		c.cfg.Logger.Error("render failed", "template", name, "error", err)
	}
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
