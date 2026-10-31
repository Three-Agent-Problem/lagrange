package platform

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
)

// A conversation is everything two addresses said to each other, either way round:
// tasks one sent the other and plain messages. The inbox shows a person's own; the
// "between agents" view shows anyone's, since the whole team sees the live graph.

// outside is how the web app names calls from outside clients (no sender).
const outside = "~outside"

// Conversation sums up what two addresses said to each other. A and B are sorted.
type Conversation struct {
	A        string    `json:"a"`
	B        string    `json:"b"`
	Tasks    int       `json:"tasks"`
	Messages int       `json:"messages"`
	Waiting  int       `json:"waiting"`
	LastAt   time.Time `json:"lastAt"`
	// Last is the latest thing said, and LastFrom who said it.
	Last     string `json:"last"`
	LastFrom string `json:"lastFrom"`
}

func pair(a, b string) (string, string) {
	if a == "" {
		a = outside
	}
	if b == "" {
		b = outside
	}
	if b < a {
		return b, a
	}
	return a, b
}

// conversations lists every pair of addresses that talked, latest first.
func (p *Platform) conversations(ctx context.Context) ([]*Conversation, error) {
	by := map[[2]string]*Conversation{}
	get := func(s, r string) *Conversation {
		a, b := pair(s, r)
		c := by[[2]string{a, b}]
		if c == nil {
			c = &Conversation{A: a, B: b}
			by[[2]string{a, b}] = c
		}
		return c
	}
	rows, err := p.DB.Pool.Query(ctx, `SELECT sender, recipient, count(DISTINCT task_id) FILTER (WHERE kind = 'task'),
			count(*) FILTER (WHERE kind = 'message'), max(at)
		FROM activity WHERE kind IN ('task', 'message') AND recipient <> '' GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s, r string
		var tasks, msgs int
		var at time.Time
		if err := rows.Scan(&s, &r, &tasks, &msgs, &at); err != nil {
			rows.Close()
			return nil, err
		}
		c := get(s, r)
		c.Tasks += tasks
		c.Messages += msgs
		if at.After(c.LastAt) {
			c.LastAt = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The latest thing each side said: a task's question or answer, or a message.
	rows, err = p.DB.Pool.Query(ctx, `SELECT DISTINCT ON (sender, recipient) sender, recipient, at, summary, coalesce(data->>'reply', '')
		FROM activity WHERE kind IN ('task', 'message', 'input') AND recipient <> '' AND summary <> ''
		ORDER BY sender, recipient, seq DESC`)
	if err != nil {
		return nil, err
	}
	latest := map[[2]string]time.Time{}
	for rows.Next() {
		var s, r, summary, reply string
		var at time.Time
		if err := rows.Scan(&s, &r, &at, &summary, &reply); err != nil {
			rows.Close()
			return nil, err
		}
		c := get(s, r)
		key := [2]string{c.A, c.B}
		if at.Before(latest[key]) {
			continue
		}
		latest[key] = at
		c.Last, c.LastFrom = summary, s
		if reply != "" {
			c.Last, c.LastFrom = reply, r
		}
		if c.LastFrom == "" {
			c.LastFrom = outside
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = p.DB.Pool.Query(ctx, `SELECT coalesce(l.sender, t.owner), t.tenant, count(*) FROM tasks t
		LEFT JOIN task_links l ON l.task_id = t.id WHERE t.state IN ($1, $2) GROUP BY 1, 2`,
		string(a2a.TaskStateInputRequired), string(a2a.TaskStateAuthRequired))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s, r string
		var n int
		if err := rows.Scan(&s, &r, &n); err != nil {
			rows.Close()
			return nil, err
		}
		get(s, r).Waiting += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*Conversation, 0, len(by))
	for _, c := range by {
		if c.A != c.B {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(x, y *Conversation) int { return y.LastAt.Compare(x.LastAt) })
	return out, nil
}

func (p *Platform) handleConversations(w http.ResponseWriter, r *http.Request, me *directory.User) {
	cs, err := p.conversations(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": cs})
}

// maxThread caps how much of a conversation one request returns.
const maxThread = 100

// handleConversation is what two addresses said to each other: their latest tasks (in
// full) and plain messages, oldest first.
func (p *Platform) handleConversation(w http.ResponseWriter, r *http.Request, me *directory.User) {
	ctx := r.Context()
	a, b := r.PathValue("a"), r.PathValue("b")
	if a == outside {
		a = ""
	}
	if b == outside {
		b = ""
	}
	if strings.TrimSpace(a) == "" && strings.TrimSpace(b) == "" {
		apiError(w, http.StatusBadRequest, "name two addresses")
		return
	}
	tasks, err := p.queryTasks(ctx, true, `WHERE (coalesce(l.sender, t.owner) = $1 AND t.tenant = $2) OR (coalesce(l.sender, t.owner) = $2 AND t.tenant = $1)
		ORDER BY t.created_at DESC LIMIT $3`, a, b, maxThread)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slices.Reverse(tasks)
	if tasks == nil {
		tasks = []*TaskView{}
	}
	msgs, err := p.Activity.Between(ctx, a, b, maxThread)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if msgs == nil {
		msgs = []*activity.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "messages": msgs})
}
