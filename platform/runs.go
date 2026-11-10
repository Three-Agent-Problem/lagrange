package platform

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
)

// A run is one major action, the unit people follow: a task nobody delegated (say "Order
// 200 hoodies") with every task delegated from it and every message sent from those, or
// a plain message exchange that wasn't made from a task.
//
// A workspace sees runs from its side: a run starts at a task it sees whose parent it
// doesn't (a call from another workspace starts a run here), and the tasks another
// workspace delegated internally are left out and counted as Hidden.

// Run summarizes a run for the runs list.
type Run struct {
	// ID is the root task's ID, or msg-<seq> for a plain message exchange.
	ID string `json:"id"`
	// ContextID is the root task's A2A context: send into it (POST /api/send with
	// contextId) to continue the conversation the run started.
	ContextID string `json:"contextId,omitempty"`
	Title     string `json:"title"`
	// Outcome is how the run ended, in words: the root task's last answer, or a plain
	// message's reply. Empty while it runs.
	Outcome   string `json:"outcome,omitempty"`
	Starter   string `json:"starter"`
	Recipient string `json:"recipient"`
	// State is the root task's state ("TASK_STATE_COMPLETED"), or "message".
	State        string    `json:"state"`
	Waiting      []string  `json:"waiting"`
	Participants []string  `json:"participants"`
	Tasks        int       `json:"tasks"`
	Entries      int       `json:"entries"`
	StartedAt    time.Time `json:"startedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// WaitingSince is when the longest-waiting task in the run started waiting.
	WaitingSince *time.Time `json:"waitingSince,omitempty"`
	// Archived runs are hidden from the list unless asked for (?archived=1).
	Archived bool `json:"archived,omitempty"`
	// Hidden counts the run's tasks inside other workspaces, which this one doesn't see.
	Hidden int `json:"hidden,omitempty"`
	// Cost is what the run's agents reported spending on it (usage entries, see
	// lagrange/acp), in Currency. Zero when no agent reports usage.
	Cost     float64 `json:"cost,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

// RunDetail is everything recorded for a run.
type RunDetail struct {
	Run       *Run                 `json:"run"`
	Tasks     []*TaskView          `json:"tasks"`
	Entries   []*activity.Entry    `json:"entries"`
	Exchanges []*activity.Exchange `json:"exchanges"`
}

const maxRunDepth = 10

// runTrees returns, for each root, the IDs of its tasks (root first).
func (p *Platform) runTrees(ctx context.Context, roots []string) (map[string][]string, error) {
	rows, err := p.DB.Pool.Query(ctx, `WITH RECURSIVE tree(root, id, depth) AS (
			SELECT r, r, 0 FROM unnest($1::text[]) AS r
			UNION ALL
			SELECT tree.root, l.task_id, tree.depth + 1 FROM task_links l JOIN tree ON l.parent_task_id = tree.id
			WHERE tree.depth < $2
		) SELECT root, id FROM tree ORDER BY depth`, roots, maxRunDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trees := map[string][]string{}
	for rows.Next() {
		var root, id string
		if err := rows.Scan(&root, &id); err != nil {
			return nil, err
		}
		if !slices.Contains(trees[root], id) {
			trees[root] = append(trees[root], id)
		}
	}
	return trees, rows.Err()
}

// rootOf walks up from a task to the task nobody delegated it from, or with s, to the
// first task whose parent s doesn't see.
func (p *Platform) rootOf(ctx context.Context, s *scope, id string) (string, error) {
	for range maxRunDepth {
		link, err := p.Links.Get(ctx, a2a.TaskID(id))
		if err != nil {
			return "", err
		}
		if link == nil || link.ParentTaskID == "" {
			return id, nil
		}
		if s != nil {
			parent, err := p.queryTasks(ctx, false, `WHERE t.id = $1 AND `+visibleSQL(2), string(link.ParentTaskID), s.handles)
			if err != nil {
				return "", err
			}
			if len(parent) == 0 {
				return id, nil
			}
		}
		id = string(link.ParentTaskID)
	}
	return id, nil
}

// rootsSQL selects the tasks that start runs in a scope: visible tasks with no visible
// parent. $1 is the scope's handles, $2 the limit.
const rootsSQL = `SELECT t.id FROM tasks t LEFT JOIN task_links l ON l.task_id = t.id
	LEFT JOIN tasks pt ON pt.id = l.parent_task_id LEFT JOIN task_links pl ON pl.task_id = pt.id
	WHERE (t.tenant = ANY($1) OR coalesce(l.sender, t.owner) = ANY($1))
		AND (pt.id IS NULL OR NOT (pt.tenant = ANY($1) OR coalesce(pl.sender, pt.owner) = ANY($1)))
	ORDER BY t.updated_at DESC LIMIT $2`

// summarize builds a run's summary from its tasks (root first) and entries.
func summarize(root *TaskView, tasks []*TaskView, entries []*activity.Entry) *Run {
	r := &Run{
		ID: string(root.ID), ContextID: root.ContextID, Title: root.Summary, Starter: root.Sender, Recipient: root.Recipient,
		State: root.State, Tasks: len(tasks), StartedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt,
		Waiting: []string{}, Participants: []string{},
	}
	if s := a2a.TaskState(root.State); s.Terminal() {
		r.Outcome = root.Status
	}
	add := func(list *[]string, h string) {
		if h != "" && !slices.Contains(*list, h) {
			*list = append(*list, h)
		}
	}
	add(&r.Participants, root.Sender)
	for _, t := range tasks {
		add(&r.Participants, t.Sender)
		add(&r.Participants, t.Recipient)
		if interruptedState(t.State) {
			// An agent's question goes back to whoever sent it, unless it names someone.
			add(&r.Waiting, firstNonEmpty(t.Awaiting, t.Sender))
			if r.WaitingSince == nil || t.UpdatedAt.Before(*r.WaitingSince) {
				since := t.UpdatedAt
				r.WaitingSince = &since
			}
		}
		if t.UpdatedAt.After(r.UpdatedAt) {
			r.UpdatedAt = t.UpdatedAt
		}
	}
	for _, e := range entries {
		if e.Kind == activity.KindUsage {
			if d, ok := e.Data["costDelta"].(float64); ok {
				r.Cost += d
				if u, ok := e.Data["usage"].(map[string]any); ok {
					if c, ok := u["cost"].(map[string]any); ok {
						r.Currency, _ = c["currency"].(string)
					}
				}
			}
		}
		r.Entries++
		add(&r.Participants, e.Sender)
		add(&r.Participants, e.Recipient)
		if e.At.After(r.UpdatedAt) {
			r.UpdatedAt = e.At
		}
	}
	return r
}

func interruptedState(s string) bool {
	return s == string(a2a.TaskStateInputRequired) || s == string(a2a.TaskStateAuthRequired)
}

// messageRun is the run of a plain message exchange.
func messageRun(e *activity.Entry) *Run {
	r := &Run{
		ID: "msg-" + strconv.FormatInt(e.Seq, 10), ContextID: e.ContextID, Title: e.Summary, Starter: e.Sender, Recipient: e.Recipient,
		State: "message", Entries: 1, StartedAt: e.At, UpdatedAt: e.At, Waiting: []string{}, Participants: []string{},
	}
	r.Outcome, _ = e.Data["reply"].(string)
	for _, h := range []string{e.Sender, e.Recipient} {
		if h != "" {
			r.Participants = append(r.Participants, h)
		}
	}
	return r
}

// runStatus is the filter group a run falls in.
func runStatus(r *Run) string {
	switch {
	case len(r.Waiting) > 0:
		return "waiting"
	case r.State == "message" || r.State == string(a2a.TaskStateCompleted):
		return "done"
	case r.State == string(a2a.TaskStateFailed) || r.State == string(a2a.TaskStateRejected) || r.State == string(a2a.TaskStateCanceled):
		return "failed"
	default:
		return "running"
	}
}

// handleRuns lists runs, newest activity first: ?status=running|waiting|done|failed,
// ?q (title and participants), ?participant=<handle>, ?waitingOn=<handle> (runs blocked on
// that address), ?limit, ?archived=1 (only the archived ones; otherwise they are left out).
func (p *Platform) handleRuns(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.DB.Pool.Query(ctx, rootsSQL, s.handles, limit*2)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var roots []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			roots = append(roots, id)
		}
	}
	rows.Close()

	runs := []*Run{}
	if len(roots) > 0 {
		trees, err := p.runTrees(ctx, roots)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		var all []string
		owner := map[string]string{}
		for root, ids := range trees {
			all = append(all, ids...)
			for _, id := range ids {
				owner[id] = root
			}
		}
		views, err := p.queryTasks(ctx, false, `WHERE t.id = ANY($1)`, all)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		entries, err := p.Activity.Run(ctx, all)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		tasksOf, entriesOf, hidden := map[string][]*TaskView{}, map[string][]*activity.Entry{}, map[string]int{}
		for _, v := range views {
			root := owner[string(v.ID)]
			if !s.task(v) {
				hidden[root]++
				continue
			}
			tasksOf[root] = append(tasksOf[root], v)
		}
		for _, e := range entries {
			if !s.entry(e) {
				continue
			}
			root := owner[e.TaskID]
			if root == "" {
				root = owner[e.ParentTaskID]
			}
			entriesOf[root] = append(entriesOf[root], e)
		}
		for _, root := range roots {
			ts := tasksOf[root]
			i := slices.IndexFunc(ts, func(v *TaskView) bool { return string(v.ID) == root })
			if i < 0 {
				continue
			}
			run := summarize(ts[i], ts, entriesOf[root])
			run.Hidden = hidden[root]
			runs = append(runs, run)
		}
	}
	messages, err := p.Activity.Messages(ctx, s.handles, limit)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range messages {
		runs = append(runs, messageRun(e))
	}

	archived, err := p.archivedRuns(ctx, s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status, text, who, blocker := q.Get("status"), strings.ToLower(q.Get("q")), q.Get("participant"), q.Get("waitingOn")
	wantArchived := q.Get("archived") == "1"
	runs = slices.DeleteFunc(runs, func(run *Run) bool {
		run.Archived = archived[run.ID]
		if run.Archived != wantArchived {
			return true
		}
		if status != "" && runStatus(run) != status {
			return true
		}
		if who != "" && !slices.Contains(run.Participants, who) {
			return true
		}
		if blocker != "" && !slices.Contains(run.Waiting, blocker) {
			return true
		}
		if text != "" && !strings.Contains(strings.ToLower(run.Title+" "+strings.Join(run.Participants, " ")), text) {
			return true
		}
		return false
	})
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleRun returns everything recorded for the run a task belongs to: {id} is any task
// in it (the root is found), or msg-<seq>.
func (p *Platform) handleRun(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	d, err := p.runDetail(ctx, s, r.PathValue("id"))
	switch {
	case errors.Is(err, errNoRun):
		apiError(w, http.StatusNotFound, "no such run")
	case err != nil:
		apiError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

var errNoRun = errors.New("no such run")

func (p *Platform) markArchived(ctx context.Context, s *scope, run *Run) {
	_ = p.DB.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM archived_runs WHERE workspace_id = $1 AND run_id = $2)`, s.ID, run.ID).Scan(&run.Archived)
}

func (p *Platform) archivedRuns(ctx context.Context, s *scope) (map[string]bool, error) {
	rows, err := p.DB.Pool.Query(ctx, `SELECT run_id FROM archived_runs WHERE workspace_id = $1`, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// handleArchiveRun hides a run from the list (POST) or shows it again (DELETE). {id} is
// any task in the run, or msg-<seq>.
func (p *Platform) handleArchiveRun(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	d, err := p.runDetail(ctx, s, r.PathValue("id"))
	switch {
	case errors.Is(err, errNoRun):
		apiError(w, http.StatusNotFound, "no such run")
		return
	case err != nil:
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.Method == http.MethodDelete {
		_, err = p.DB.Pool.Exec(ctx, `DELETE FROM archived_runs WHERE workspace_id = $1 AND run_id = $2`, s.ID, d.Run.ID)
	} else {
		_, err = p.DB.Pool.Exec(ctx, `INSERT INTO archived_runs (workspace_id, run_id, archived_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, s.ID, d.Run.ID, me.Handle)
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runDetail is the run as s sees it.
func (p *Platform) runDetail(ctx context.Context, s *scope, id string) (*RunDetail, error) {
	if n, ok := strings.CutPrefix(id, "msg-"); ok {
		seq, _ := strconv.ParseInt(n, 10, 64)
		e, err := p.Activity.Get(ctx, seq)
		if err != nil {
			return nil, err
		}
		if e == nil || e.Kind != activity.KindMessage || !s.entry(e) {
			return nil, errNoRun
		}
		exchanges, err := p.exchangesOf(ctx, nil, []*activity.Entry{e})
		if err != nil {
			return nil, err
		}
		run := messageRun(e)
		p.markArchived(ctx, s, run)
		return &RunDetail{Run: run, Tasks: []*TaskView{}, Entries: []*activity.Entry{e}, Exchanges: exchanges}, nil
	}
	if seen, err := p.queryTasks(ctx, false, `WHERE t.id = $1 AND `+visibleSQL(2), id, s.handles); err != nil {
		return nil, err
	} else if len(seen) == 0 {
		return nil, errNoRun
	}
	root, err := p.rootOf(ctx, s, id)
	if err != nil {
		return nil, err
	}
	trees, err := p.runTrees(ctx, []string{root})
	if err != nil {
		return nil, err
	}
	all, err := p.queryTasks(ctx, true, `WHERE t.id = ANY($1) ORDER BY t.created_at`, trees[root])
	if err != nil {
		return nil, err
	}
	var ids []string
	tasks := []*TaskView{}
	for _, t := range all {
		if s.task(t) {
			tasks = append(tasks, t)
			ids = append(ids, string(t.ID))
		}
	}
	i := slices.IndexFunc(tasks, func(v *TaskView) bool { return string(v.ID) == root })
	if i < 0 {
		return nil, errNoRun
	}
	entries, err := p.Activity.Run(ctx, ids)
	if err != nil {
		return nil, err
	}
	entries = slices.DeleteFunc(entries, func(e *activity.Entry) bool { return !s.entry(e) })
	exchanges, err := p.exchangesOf(ctx, ids, entries)
	if err != nil {
		return nil, err
	}
	// The platform's push sender delivers a task's updates on behalf of its recipient.
	recipients := map[string]string{}
	for _, t := range tasks {
		recipients[string(t.ID)] = t.Recipient
	}
	for _, x := range exchanges {
		if x.Direction == activity.DirPush && x.Sender == "" {
			x.Sender = recipients[x.TaskID]
		}
	}
	if entries == nil {
		entries = []*activity.Entry{}
	}
	run := summarize(tasks[i], tasks, entries)
	run.Hidden = len(all) - len(tasks)
	p.markArchived(ctx, s, run)
	return &RunDetail{Run: run, Tasks: tasks, Entries: entries, Exchanges: exchanges}, nil
}

// exchangesOf returns the exchanges of the run's tasks and of its plain messages (those
// are recorded under the call's ID, or matched by context).
func (p *Platform) exchangesOf(ctx context.Context, taskIDs []string, entries []*activity.Entry) ([]*activity.Exchange, error) {
	ids := slices.Clone(taskIDs)
	var contexts []string
	for _, e := range entries {
		if e.Kind != activity.KindMessage {
			continue
		}
		if id, ok := e.Data["callId"].(string); ok && id != "" {
			ids = append(ids, id)
		}
		if e.ContextID != "" {
			contexts = append(contexts, e.ContextID)
		}
	}
	x, err := p.Activity.Exchanges(ctx, ids, contexts)
	if x == nil {
		x = []*activity.Exchange{}
	}
	return x, err
}
