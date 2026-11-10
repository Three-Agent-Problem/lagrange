package platform

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/telegram"
)

// Telegram: each person can connect their own bot (Settings → Notifications). What the
// bot may do is telegramDeps, and nothing more: answer a task waiting on its owner,
// and read their waiting tasks and runs. See package telegram.

func (p *Platform) telegramRoutes(mux *http.ServeMux) {
	// API keys work here too (Halo connects the bot with one); viewer keys only read.
	keyed := func(h apiHandler) http.Handler { return p.session(h, true, true) }
	mux.Handle("GET /api/settings/telegram", keyed(p.handleTelegram))
	mux.Handle("PUT /api/settings/telegram", keyed(p.handleConnectTelegram))
	mux.Handle("DELETE /api/settings/telegram", keyed(p.handleDisconnectTelegram))
	mux.Handle("POST /api/settings/telegram/pair", keyed(p.handlePairTelegram))
	mux.Handle("PUT /api/settings/telegram/level", keyed(p.handleTelegramLevel))
	mux.Handle("POST /api/settings/telegram/test", keyed(p.handleTestTelegram))
}

// personal is the scope of one person on their own: what they sent and what was sent
// to them, in any workspace. The bot never sees more.
func personal(handle string) *scope { return newScope(&directory.Workspace{}, []string{handle}) }

func (p *Platform) telegramDeps() telegram.Deps {
	return telegram.Deps{
		Answer: func(ctx context.Context, handle, taskID string, a telegram.Answer) error {
			u, err := p.Directory.UserByHandle(ctx, handle)
			if err != nil {
				return err
			}
			status, err := p.answer(ctx, u, nil, taskID, &answerBody{Text: a.Text, Decision: a.Decision})
			if err != nil {
				switch status {
				case http.StatusConflict, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
					return errors.Join(telegram.ErrGone, err)
				}
			}
			return err
		},
		Waiting: func(ctx context.Context, handle string) ([]telegram.Waiting, error) {
			tasks, waits, err := p.waitingTasks(ctx, personal(handle))
			if err != nil {
				return nil, err
			}
			var out []telegram.Waiting
			for _, t := range tasks {
				w := waits[string(t.ID)]
				if w == nil || (w.Who != handle && w.EscalatedTo != handle) {
					continue
				}
				stored, tenant, err := p.Tasks.Load(ctx, t.ID)
				if err != nil {
					continue
				}
				out = append(out, telegram.Waiting{
					TaskID: string(t.ID), Asker: w.Asker, State: t.State, Question: questionOf(stored.Task, tenant == handle || tenant == w.Who),
					For: forOf(w, handle), Since: w.Since,
				})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
			return out, nil
		},
		Runs: func(ctx context.Context, handle string, limit int) ([]telegram.Run, error) {
			s := personal(handle)
			runs, err := p.runsOf(ctx, s, limit)
			if err != nil {
				return nil, err
			}
			archived, err := p.archivedRuns(ctx, s)
			if err != nil {
				return nil, err
			}
			runs = slices.DeleteFunc(runs, func(r *Run) bool { return r.State == "message" || archived[r.ID] })
			sort.SliceStable(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
			if len(runs) > limit {
				runs = runs[:limit]
			}
			out := make([]telegram.Run, len(runs))
			for i, r := range runs {
				out[i] = telegramRun(r)
			}
			return out, nil
		},
		Run: func(ctx context.Context, handle, taskID string) (*telegram.Run, error) {
			d, err := p.runDetail(ctx, personal(handle), taskID)
			if err != nil {
				return nil, err
			}
			r := telegramRun(d.Run)
			return &r, nil
		},
	}
}

func forOf(w *wait, handle string) string {
	if w.EscalatedTo == handle && w.Who != handle {
		return w.Who
	}
	return ""
}

// questionOf is what a waiting task asks: for a task sent to a person, the message
// they were sent; for an agent's question back to its sender, its status message.
func questionOf(t *a2a.Task, toPerson bool) *a2a.Message {
	if toPerson {
		for i := len(t.History) - 1; i >= 0; i-- {
			if m := t.History[i]; m != nil && m.Role == a2a.MessageRoleUser {
				return m
			}
		}
	}
	return t.Status.Message
}

func telegramRun(r *Run) telegram.Run {
	return telegram.Run{
		ID: r.ID, Title: r.Title, State: r.State, Status: runStatus(r), Waiting: r.Waiting, Cost: r.Cost, Currency: r.Currency,
		StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt, Outcome: r.Outcome, Report: r.Report,
	}
}

func (p *Platform) handleTelegram(w http.ResponseWriter, r *http.Request, me *directory.User) {
	st, err := p.Telegram.Status(r.Context(), me)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (p *Platform) handleConnectTelegram(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {token}")
		return
	}
	st, err := p.Telegram.Connect(r.Context(), me, body.Token)
	if err != nil {
		status := http.StatusBadGateway
		if telegram.IsUserError(err) {
			status = http.StatusBadRequest
		}
		apiError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (p *Platform) handlePairTelegram(w http.ResponseWriter, r *http.Request, me *directory.User) {
	st, err := p.Telegram.Pair(r.Context(), me)
	if err != nil {
		apiError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (p *Platform) handleTelegramLevel(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body struct {
		Level string `json:"level"`
	}
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {level}")
		return
	}
	st, err := p.Telegram.SetLevel(r.Context(), me, body.Level)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (p *Platform) handleTestTelegram(w http.ResponseWriter, r *http.Request, me *directory.User) {
	sent, err := p.Telegram.Test(r.Context(), me)
	switch {
	case err != nil:
		apiError(w, http.StatusBadGateway, "the test message failed: "+err.Error())
	case !sent:
		apiError(w, http.StatusConflict, "connect and pair a bot first")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *Platform) handleDisconnectTelegram(w http.ResponseWriter, r *http.Request, me *directory.User) {
	if err := p.Telegram.Disconnect(r.Context(), me); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
