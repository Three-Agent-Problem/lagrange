// Package activity is the platform's event log: every task created, every state
// change, artifact and plain message, every message waiting in a mailbox, and every
// tunnel that connects or drops. The web app builds its live graph, inbox and replay
// from it, and Stream serves it as Server-Sent Events.
package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/pg"
)

// Kinds of entry.
const (
	// KindTask is a task state change; the first one for a task is its creation.
	KindTask = "task"
	// KindArtifact is an artifact (or a chunk of one) produced by a task.
	KindArtifact = "artifact"
	// KindMessage is a plain message answer: an exchange that created no task.
	KindMessage = "message"
	// KindQueued means a task is waiting in the recipient's mailbox (agent offline or busy).
	KindQueued = "queued"
	// KindDelivered means a queued task reached the agent.
	KindDelivered = "delivered"
	// KindPresence reports an agent's tunnel sessions and capacity.
	KindPresence = "presence"
	// KindAddress means an address was created, changed or removed.
	KindAddress = "address"
)

// Entry is one line of the log.
type Entry struct {
	Seq          int64          `json:"seq"`
	At           time.Time      `json:"at"`
	Kind         string         `json:"kind"`
	TaskID       string         `json:"taskId,omitempty"`
	ParentTaskID string         `json:"parentTaskId,omitempty"`
	ContextID    string         `json:"contextId,omitempty"`
	Sender       string         `json:"sender,omitempty"`
	Recipient    string         `json:"recipient,omitempty"`
	State        string         `json:"state,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
}

// Log writes and reads entries.
type Log struct {
	db *pg.DB
}

// New returns a log on db.
func New(db *pg.DB) *Log { return &Log{db: db} }

// Add appends e and notifies listeners. Seq and At are filled in.
func (l *Log) Add(ctx context.Context, e *Entry) error {
	var data []byte
	if e.Data != nil {
		var err error
		if data, err = json.Marshal(e.Data); err != nil {
			return err
		}
	}
	err := l.db.Pool.QueryRow(ctx, `INSERT INTO activity (kind, task_id, parent_task_id, context_id, sender, recipient, state, summary, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING seq, at`,
		e.Kind, e.TaskID, e.ParentTaskID, e.ContextID, e.Sender, e.Recipient, e.State, truncate(e.Summary, 280), data).Scan(&e.Seq, &e.At)
	if err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	return l.db.Notify(ctx, pg.ChannelActivity, strconv.FormatInt(e.Seq, 10))
}

// Since returns up to limit entries after seq, oldest first.
func (l *Log) Since(ctx context.Context, seq int64, limit int) ([]*Entry, error) {
	return l.query(ctx, `WHERE seq > $1 ORDER BY seq LIMIT $2`, seq, limit)
}

// Task returns every entry of a task, oldest first.
func (l *Log) Task(ctx context.Context, taskID string) ([]*Entry, error) {
	return l.query(ctx, `WHERE task_id = $1 ORDER BY seq`, taskID)
}

// Last returns the latest seq, or 0.
func (l *Log) Last(ctx context.Context) (int64, error) {
	var seq int64
	err := l.db.Pool.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM activity`).Scan(&seq)
	return seq, err
}

func (l *Log) query(ctx context.Context, where string, args ...any) ([]*Entry, error) {
	rows, err := l.db.Pool.Query(ctx, `SELECT seq, at, kind, task_id, parent_task_id, context_id, sender, recipient, state, summary, data
		FROM activity `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Entry, error) {
		var e Entry
		var data []byte
		if err := row.Scan(&e.Seq, &e.At, &e.Kind, &e.TaskID, &e.ParentTaskID, &e.ContextID, &e.Sender, &e.Recipient, &e.State, &e.Summary, &data); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &e.Data); err != nil {
				return nil, err
			}
		}
		return &e, nil
	})
}

// Stream serves the log as Server-Sent Events, starting after ?since=<seq> or the
// Last-Event-ID header (so browsers resume where they left off), or at the end of the
// log when neither is given. Each event's id is its seq.
func (l *Log) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	wake, unsubscribe := l.db.Subscribe(pg.ChannelActivity)
	defer unsubscribe()

	cursor, err := l.Last(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, v := range []string{r.URL.Query().Get("since"), r.Header.Get("Last-Event-ID")} {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cursor = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // let reverse proxies pass events through
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: 2000\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		for {
			entries, err := l.Since(ctx, cursor, 500)
			if err != nil {
				return
			}
			for _, e := range entries {
				raw, _ := json.Marshal(e)
				fmt.Fprintf(w, "id: %d\nevent: activity\ndata: %s\n\n", e.Seq, raw)
				cursor = e.Seq
			}
			flusher.Flush()
			if len(entries) < 500 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
