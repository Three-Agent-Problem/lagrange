package platform

import (
	"context"
	"net/http"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
)

// Reset deletes every agent address (with its tokens) and all traffic: tasks, runs,
// activity, recorded HTTP, decision links. People, their logins and their settings stay.
// Connected tunnels are dropped. Only for development (Config.Dev).
func (p *Platform) Reset(ctx context.Context) error {
	tx, err := p.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`DELETE FROM addresses WHERE kind = 'agent'`,
		// Sequences keep counting, so live event streams carry on without replaying old seqs.
		`TRUNCATE tasks, task_events, push_configs, work_items, task_links, activity, exchanges, decision_links`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	p.sessions.closeAll()
	return p.Activity.Add(ctx, &activity.Entry{Kind: activity.KindAddress})
}

func (p *Platform) handleDevReset(w http.ResponseWriter, r *http.Request, me *directory.User) {
	if !p.cfg.Dev {
		apiError(w, http.StatusNotFound, "dev endpoints are off (set LAGRANGE_DEV=1)")
		return
	}
	if !me.Admin {
		apiError(w, http.StatusForbidden, "only admins can reset the server")
		return
	}
	if err := p.Reset(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.log.Warn("dev reset: deleted all agents and traffic", "by", me.Handle)
	w.WriteHeader(http.StatusNoContent)
}
