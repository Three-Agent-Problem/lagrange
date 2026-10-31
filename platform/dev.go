package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

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
		`TRUNCATE tasks, task_events, push_configs, work_items, task_links, activity, exchanges, decision_links, archived_runs`,
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

// handleDevReset empties the server ({"seed": false}) or resets it to the demo seed
// ({"seed": true}): empty, then the seed command runs again with LAGRANGE_SEED=1.
func (p *Platform) handleDevReset(w http.ResponseWriter, r *http.Request, me *directory.User) {
	if !p.cfg.Dev {
		apiError(w, http.StatusNotFound, "dev endpoints are off (set LAGRANGE_DEV=1)")
		return
	}
	if !me.Admin {
		apiError(w, http.StatusForbidden, "only admins can reset the server")
		return
	}
	var body struct {
		Seed bool `json:"seed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "send {\"seed\": true|false}")
		return
	}
	if body.Seed && p.seed == nil {
		apiError(w, http.StatusBadRequest, "no seed command (set LAGRANGE_DEV_SEED)")
		return
	}
	p.seed.stop()
	if err := p.Reset(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Seed {
		if err := p.seed.start(true); err != nil {
			apiError(w, http.StatusInternalServerError, "seed command: "+err.Error())
			return
		}
	}
	p.log.Warn("dev reset", "by", me.Handle, "seed", body.Seed)
	w.WriteHeader(http.StatusNoContent)
}

// seeder runs the dev seed command (Config.DevSeed): agents that stay connected while it
// runs, plus, with LAGRANGE_SEED=1 in its environment, a scripted set of requests.
type seeder struct {
	argv []string
	env  []string
	log  *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newSeeder(cfg Config, log *slog.Logger) *seeder {
	argv := strings.Fields(cfg.DevSeed)
	if !cfg.Dev || len(argv) == 0 {
		return nil
	}
	return &seeder{
		argv: argv,
		env:  []string{"LAGRANGE_SERVER=" + cfg.PublicURL, "LAGRANGE_ADMIN_EMAIL=" + cfg.AdminEmail, "LAGRANGE_ADMIN_PASSWORD=" + cfg.AdminPassword},
		log:  log,
	}
}

// start stops a running seed command and starts it again; script adds LAGRANGE_SEED=1.
func (s *seeder) start(script bool) error {
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
	cmd.Env = append(os.Environ(), s.env...)
	if script {
		cmd.Env = append(cmd.Env, "LAGRANGE_SEED=1")
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			s.log.Warn("dev seed command exited", "err", err)
		}
	}()
	s.cancel, s.done = cancel, done
	s.log.Info("dev seed command started", "argv", s.argv, "script", script)
	return nil
}

// stop ends the seed command, if it runs, and waits for it to exit. A nil seeder is a no-op.
func (s *seeder) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel, s.done = nil, nil
}
