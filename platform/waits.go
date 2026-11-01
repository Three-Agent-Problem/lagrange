package platform

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/relay"
)

// A task waiting on someone (an approval, a question, an account to connect) blocks its
// run until they answer. The workspace's WaitPolicy reminds them, escalates to someone
// else, and finally cancels the task; the waiting board shows all of it. A wait follows
// the policy of a workspace the person (or agent) waited on belongs to, or, for an
// outside caller, the asker's.

// maxReminders caps the policy's reminders per wait.
const maxReminders = 3

// WaitPolicy is a workspace's rules for waiting tasks. Delays are in seconds; nil is off.
type WaitPolicy struct {
	RemindAfter   *int64 `json:"remindAfter"`
	EscalateAfter *int64 `json:"escalateAfter"`
	EscalateTo    string `json:"escalateTo"`
	CancelAfter   *int64 `json:"cancelAfter"`
}

func (w WaitPolicy) off() bool {
	return w.RemindAfter == nil && (w.EscalateAfter == nil || w.EscalateTo == "") && w.CancelAfter == nil
}

func secs(s *int64) time.Duration { return time.Duration(*s) * time.Second }

func (p *Platform) waitPolicy(ctx context.Context, workspaceID int64) (WaitPolicy, error) {
	var w WaitPolicy
	err := p.DB.Pool.QueryRow(ctx, `SELECT remind_after_s, escalate_after_s, escalate_to, cancel_after_s FROM wait_policy WHERE workspace_id = $1`, workspaceID).
		Scan(&w.RemindAfter, &w.EscalateAfter, &w.EscalateTo, &w.CancelAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return WaitPolicy{}, nil
	}
	return w, err
}

// wait is a row of waits.
type wait struct {
	TaskID, Who, Asker string
	Since              time.Time
	Reminders          int
	RemindedAt         *time.Time
	EscalatedTo        string
	EscalatedAt        *time.Time
}

// onWaiting records that a task started waiting, then notifies the person.
func (p *Platform) onWaiting(ctx context.Context, w relay.Waiting) {
	e := w.Entry
	who, _ := e.Data["awaiting"].(string)
	who = firstNonEmpty(who, e.Sender)
	asker := e.Recipient
	if who == e.Recipient {
		asker = e.Sender
	}
	if who != "" && e.TaskID != "" {
		// The same wait can be reported twice (task and status events): only a later
		// question starts the timers over.
		_, err := p.DB.Pool.Exec(context.WithoutCancel(ctx), `INSERT INTO waits (task_id, who, asker, since) VALUES ($1, $2, $3, $4)
			ON CONFLICT (task_id) DO UPDATE SET who = EXCLUDED.who, asker = EXCLUDED.asker, since = EXCLUDED.since,
				reminders = 0, reminded_at = NULL, escalated_to = '', escalated_at = NULL
			WHERE waits.who <> EXCLUDED.who OR waits.since < EXCLUDED.since - interval '1 second'`, e.TaskID, who, asker, e.At)
		if err != nil {
			p.log.Warn("platform: recording a wait failed", "task", e.TaskID, "error", err)
		}
	}
	p.Notify.Waiting(ctx, w)
}

func (p *Platform) waits(ctx context.Context, ids []string) (map[string]*wait, error) {
	rows, err := p.DB.Pool.Query(ctx, `SELECT task_id, who, asker, since, reminders, reminded_at, escalated_to, escalated_at FROM waits WHERE task_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*wait{}
	for rows.Next() {
		w := &wait{}
		if err := rows.Scan(&w.TaskID, &w.Who, &w.Asker, &w.Since, &w.Reminders, &w.RemindedAt, &w.EscalatedTo, &w.EscalatedAt); err != nil {
			return nil, err
		}
		out[w.TaskID] = w
	}
	return out, rows.Err()
}

// waitingTasks returns every task waiting on someone that s sees, with its wait (made up
// from the task for waits that started before waits were recorded).
func (p *Platform) waitingTasks(ctx context.Context, s *scope) ([]*TaskView, map[string]*wait, error) {
	tasks, err := p.queryTasks(ctx, true, `WHERE t.state IN ($1, $2) AND `+visibleSQL(3)+` ORDER BY t.updated_at`,
		string(a2a.TaskStateInputRequired), string(a2a.TaskStateAuthRequired), s.handles)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = string(t.ID)
	}
	ws, err := p.waits(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tasks {
		if ws[string(t.ID)] == nil {
			who := firstNonEmpty(t.Awaiting, t.Sender)
			asker := t.Recipient
			if who == t.Recipient {
				asker = t.Sender
			}
			ws[string(t.ID)] = &wait{TaskID: string(t.ID), Who: who, Asker: asker, Since: t.UpdatedAt}
		}
	}
	return tasks, ws, nil
}

// WaitView is a waiting task on the waiting board.
type WaitView struct {
	Task  *TaskView `json:"task"`
	RunID string    `json:"runId"`
	Who   string    `json:"who"`
	Asker string    `json:"asker"`
	// Person is true when who is a person (reminders and escalations reach people only).
	Person      bool       `json:"person"`
	Since       time.Time  `json:"since"`
	Reminders   int        `json:"reminders"`
	RemindedAt  *time.Time `json:"remindedAt,omitempty"`
	EscalatedTo string     `json:"escalatedTo,omitempty"`
	EscalatedAt *time.Time `json:"escalatedAt,omitempty"`
	// The policy's next steps for this wait, if any.
	RemindAt   *time.Time `json:"remindAt,omitempty"`
	EscalateAt *time.Time `json:"escalateAt,omitempty"`
	CancelAt   *time.Time `json:"cancelAt,omitempty"`
}

// next fills in when the policy acts next on w.
func (v *WaitView) next(pol WaitPolicy) {
	at := func(t time.Time, d time.Duration) *time.Time { t = t.Add(d); return &t }
	if pol.RemindAfter != nil && v.Person && v.Reminders < maxReminders {
		from := v.Since
		if v.RemindedAt != nil {
			from = *v.RemindedAt
		}
		v.RemindAt = at(from, secs(pol.RemindAfter))
	}
	if pol.EscalateAfter != nil && pol.EscalateTo != "" && pol.EscalateTo != v.Who && v.EscalatedAt == nil {
		v.EscalateAt = at(v.Since, secs(pol.EscalateAfter))
	}
	if pol.CancelAfter != nil {
		v.CancelAt = at(v.Since, secs(pol.CancelAfter))
	}
}

func (p *Platform) people(ctx context.Context) (map[string]bool, error) {
	rows, err := p.DB.Pool.Query(ctx, `SELECT handle FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// handleWaiting is the waiting board: every task of the workspace waiting on someone,
// oldest first, with the workspace's policy.
func (p *Platform) handleWaiting(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	pol, err := p.waitPolicy(ctx, s.ID)
	if err == nil {
		var tasks []*TaskView
		var ws map[string]*wait
		var people map[string]bool
		if tasks, ws, err = p.waitingTasks(ctx, s); err == nil {
			people, err = p.people(ctx)
			if err == nil {
				out := []*WaitView{}
				for _, t := range tasks {
					wt := ws[string(t.ID)]
					root, rerr := p.rootOf(ctx, s, string(t.ID))
					if rerr != nil {
						root = string(t.ID)
					}
					v := &WaitView{
						Task: t, RunID: root, Who: wt.Who, Asker: wt.Asker, Person: people[wt.Who], Since: wt.Since,
						Reminders: wt.Reminders, RemindedAt: wt.RemindedAt, EscalatedTo: wt.EscalatedTo, EscalatedAt: wt.EscalatedAt,
					}
					v.next(pol)
					out = append(out, v)
				}
				slices.SortStableFunc(out, func(a, b *WaitView) int { return a.Since.Compare(b.Since) })
				writeJSON(w, http.StatusOK, map[string]any{"waiting": out, "policy": pol})
				return
			}
		}
	}
	apiError(w, http.StatusInternalServerError, err.Error())
}

func (p *Platform) handleWaitPolicy(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	pol, err := p.waitPolicy(r.Context(), s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": pol})
}

func (p *Platform) handleSetWaitPolicy(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var pol WaitPolicy
	if err := decode(r, &pol); err != nil {
		apiError(w, http.StatusBadRequest, "send {remindAfter, escalateAfter, escalateTo, cancelAfter} (seconds or null)")
		return
	}
	for _, s := range []*int64{pol.RemindAfter, pol.EscalateAfter, pol.CancelAfter} {
		if s != nil && *s < 1 {
			apiError(w, http.StatusBadRequest, "delays are seconds, at least 1 (or null for off)")
			return
		}
	}
	if pol.EscalateTo != "" {
		if _, err := p.Directory.UserByHandle(r.Context(), pol.EscalateTo); err != nil || !s.in[pol.EscalateTo] {
			apiError(w, http.StatusBadRequest, "escalateTo must be the handle of a person in this workspace")
			return
		}
	}
	_, err := p.DB.Pool.Exec(r.Context(), `INSERT INTO wait_policy (workspace_id, remind_after_s, escalate_after_s, escalate_to, cancel_after_s, updated_by, updated_at)
		VALUES ($6, $1, $2, $3, $4, $5, now())
		ON CONFLICT (workspace_id) DO UPDATE SET remind_after_s = $1, escalate_after_s = $2, escalate_to = $3, cancel_after_s = $4, updated_by = $5, updated_at = now()`,
		pol.RemindAfter, pol.EscalateAfter, pol.EscalateTo, pol.CancelAfter, me.Handle, s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": pol})
}

// handleRemind reminds the person a task waits on now, at most once a minute.
func (p *Platform) handleRemind(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	id := r.PathValue("id")
	tasks, ws, err := p.waitingTasks(ctx, s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	i := slices.IndexFunc(tasks, func(t *TaskView) bool { return string(t.ID) == id })
	if i < 0 {
		apiError(w, http.StatusNotFound, "this task isn't waiting on anyone")
		return
	}
	wt := ws[id]
	if _, err := p.Directory.UserByHandle(ctx, wt.Who); err != nil {
		apiError(w, http.StatusBadRequest, "@"+wt.Who+" is an agent: only people get reminders")
		return
	}
	// Make sure the wait is recorded, then claim the reminder.
	_, _ = p.DB.Pool.Exec(ctx, `INSERT INTO waits (task_id, who, asker, since) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, wt.Who, wt.Asker, wt.Since)
	tag, err := p.DB.Pool.Exec(ctx, `UPDATE waits SET reminders = reminders + 1, reminded_at = now()
		WHERE task_id = $1 AND (reminded_at IS NULL OR reminded_at < now() - interval '1 minute')`, id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		apiError(w, http.StatusTooManyRequests, "reminded less than a minute ago")
		return
	}
	t := tasks[i]
	if err := p.Notify.Notify(ctx, notify.Notice{
		To: wt.Who, Asker: wt.Asker, TaskID: id, State: t.State, Question: question(t, p.isPerson(ctx, t.Recipient)),
		Kind: notify.NoticeReminder, Waited: time.Since(wt.Since),
	}); err != nil {
		apiError(w, http.StatusBadGateway, "the reminder could not be sent: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// question is what a waiting task asks. A task addressed to a person asks with the
// message it was sent (its status only says who it waits for); an agent asks with its
// status message.
func question(t *TaskView, toPerson bool) *a2a.Message {
	if t == nil || t.Task == nil {
		return nil
	}
	if !toPerson && t.Task.Status.Message != nil {
		return t.Task.Status.Message
	}
	for i := len(t.Task.History) - 1; i >= 0; i-- {
		if m := t.Task.History[i]; m.Role == a2a.MessageRoleUser {
			return m
		}
	}
	return t.Task.Status.Message
}

func (p *Platform) isPerson(ctx context.Context, handle string) bool {
	_, err := p.Directory.UserByHandle(ctx, handle)
	return err == nil
}

// escalatedTo is who a task's wait was escalated to, if anyone.
func (p *Platform) escalatedTo(ctx context.Context, taskID string) string {
	var h string
	_ = p.DB.Pool.QueryRow(ctx, `SELECT escalated_to FROM waits WHERE task_id = $1`, taskID).Scan(&h)
	return h
}

// runWaitTimers applies the wait policy every tick until ctx ends. Every action is
// claimed with a conditional UPDATE or DELETE first, so with several replicas each
// reminder, escalation and cancel happens once.
func (p *Platform) runWaitTimers(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.applyWaitPolicy(ctx); err != nil && ctx.Err() == nil {
				p.log.Warn("platform: wait timers", "error", err)
			}
		}
	}
}

// applyWaitPolicy applies each workspace's policy to the waits on its addresses. A wait
// on someone in several workspaces follows the oldest workspace's policy that is on.
func (p *Platform) applyWaitPolicy(ctx context.Context) error {
	rows, err := p.DB.Pool.Query(ctx, `SELECT workspace_id FROM wait_policy ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	people, err := p.people(ctx)
	if err != nil {
		return err
	}
	belongs, err := p.Directory.HandleWorkspaces(ctx)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, id := range ids {
		pol, err := p.waitPolicy(ctx, id)
		if err != nil {
			return err
		}
		if pol.off() {
			continue
		}
		s, err := p.scopeFor(ctx, &directory.Workspace{ID: id})
		if err != nil {
			return err
		}
		if err := p.applyPolicy(ctx, s, pol, people, belongs, done); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) applyPolicy(ctx context.Context, s *scope, pol WaitPolicy, people map[string]bool, belongs map[string][]int64, done map[string]bool) error {
	tasks, ws, err := p.waitingTasks(ctx, s)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, t := range tasks {
		wt := ws[string(t.ID)]
		governs := s.in[wt.Who] || len(belongs[wt.Who]) == 0 && s.in[wt.Asker]
		if !governs || done[wt.TaskID] {
			continue
		}
		done[wt.TaskID] = true
		v := &WaitView{Who: wt.Who, Person: people[wt.Who], Since: wt.Since, Reminders: wt.Reminders, RemindedAt: wt.RemindedAt, EscalatedAt: wt.EscalatedAt}
		v.next(pol)
		// Make sure waits from before waits were recorded can be claimed.
		_, _ = p.DB.Pool.Exec(ctx, `INSERT INTO waits (task_id, who, asker, since) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, wt.TaskID, wt.Who, wt.Asker, wt.Since)
		switch {
		case v.CancelAt != nil && !now.Before(*v.CancelAt):
			tag, err := p.DB.Pool.Exec(ctx, `DELETE FROM waits WHERE task_id = $1`, wt.TaskID)
			if err != nil || tag.RowsAffected() == 0 {
				continue
			}
			p.cancelWaiting(ctx, t, wt)
		case v.EscalateAt != nil && !now.Before(*v.EscalateAt):
			tag, err := p.DB.Pool.Exec(ctx, `UPDATE waits SET escalated_to = $2, escalated_at = now() WHERE task_id = $1 AND escalated_at IS NULL`, wt.TaskID, pol.EscalateTo)
			if err != nil || tag.RowsAffected() == 0 {
				continue
			}
			p.send(ctx, notify.Notice{To: pol.EscalateTo, Asker: wt.Asker, TaskID: wt.TaskID, State: t.State, Question: question(t, people[t.Recipient]),
				Kind: notify.NoticeEscalation, Waited: now.Sub(wt.Since), For: wt.Who})
		case v.RemindAt != nil && !now.Before(*v.RemindAt):
			tag, err := p.DB.Pool.Exec(ctx, `UPDATE waits SET reminders = reminders + 1, reminded_at = now() WHERE task_id = $1 AND reminders = $2`, wt.TaskID, wt.Reminders)
			if err != nil || tag.RowsAffected() == 0 {
				continue
			}
			p.send(ctx, notify.Notice{To: wt.Who, Asker: wt.Asker, TaskID: wt.TaskID, State: t.State, Question: question(t, people[t.Recipient]),
				Kind: notify.NoticeReminder, Waited: now.Sub(wt.Since)})
		}
	}
	return nil
}

func (p *Platform) send(ctx context.Context, n notify.Notice) {
	if err := p.Notify.Notify(ctx, n); err != nil {
		p.log.Warn("platform: wait notice failed", "kind", n.Kind, "to", n.To, "task", n.TaskID, "error", err)
	}
}

// cancelWaiting cancels a task that waited longer than the policy allows.
func (p *Platform) cancelWaiting(ctx context.Context, t *TaskView, wt *wait) {
	stored, tenant, err := p.Tasks.Load(ctx, t.ID)
	if err == nil {
		_, err = p.A2A.CancelTask(callAs(ctx, stored.User, nil), &a2a.CancelTaskRequest{Tenant: tenant, ID: t.ID})
	}
	if err != nil {
		p.log.Warn("platform: cancelling a task that waited too long failed", "task", t.ID, "error", err)
		return
	}
	p.log.Info("platform: cancelled a task that waited too long", "task", t.ID, "waited on", wt.Who, "since", wt.Since)
}
