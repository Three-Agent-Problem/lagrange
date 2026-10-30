package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PushStore implements push.ConfigStore.
type PushStore struct {
	db *DB
}

var _ push.ConfigStore = (*PushStore)(nil)

// NewPushStore returns a push config store backed by db.
func NewPushStore(db *DB) *PushStore { return &PushStore{db: db} }

// Save implements push.ConfigStore.
func (s *PushStore) Save(ctx context.Context, taskID a2a.TaskID, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("%w: push config is required", a2a.ErrInvalidParams)
	}
	if u, err := url.ParseRequestURI(config.URL); err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid push config endpoint URL", a2a.ErrInvalidParams)
	}
	saved := *config
	if saved.ID == "" {
		saved.ID = uuid.NewString()
	}
	saved.TaskID = taskID
	raw, err := json.Marshal(&saved)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Pool.Exec(ctx, `INSERT INTO push_configs (task_id, id, config) VALUES ($1, $2, $3)
		ON CONFLICT (task_id, id) DO UPDATE SET config = excluded.config`, taskID, saved.ID, raw)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// Get implements push.ConfigStore.
func (s *PushStore) Get(ctx context.Context, taskID a2a.TaskID, configID string) (*a2a.PushConfig, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT config FROM push_configs WHERE task_id = $1 AND id = $2`, taskID, configID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, push.ErrPushConfigNotFound
	}
	if err != nil {
		return nil, err
	}
	var config a2a.PushConfig
	return &config, json.Unmarshal(raw, &config)
}

// List implements push.ConfigStore.
func (s *PushStore) List(ctx context.Context, taskID a2a.TaskID) ([]*a2a.PushConfig, error) {
	rows, err := s.db.Pool.Query(ctx, `SELECT config FROM push_configs WHERE task_id = $1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	configs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*a2a.PushConfig, error) {
		var raw []byte
		if err := row.Scan(&raw); err != nil {
			return nil, err
		}
		var config a2a.PushConfig
		return &config, json.Unmarshal(raw, &config)
	})
	if err != nil {
		return nil, err
	}
	return configs, nil
}

// Delete implements push.ConfigStore.
func (s *PushStore) Delete(ctx context.Context, taskID a2a.TaskID, configID string) error {
	_, err := s.db.Pool.Exec(ctx, `DELETE FROM push_configs WHERE task_id = $1 AND id = $2`, taskID, configID)
	return err
}

// DeleteAll implements push.ConfigStore.
func (s *PushStore) DeleteAll(ctx context.Context, taskID a2a.TaskID) error {
	_, err := s.db.Pool.Exec(ctx, `DELETE FROM push_configs WHERE task_id = $1`, taskID)
	return err
}

// WorkQueueConfig tunes the work queue. Zero values mean the defaults.
type WorkQueueConfig struct {
	// Lease is how long a claimed item stays with a worker without a heartbeat (default 10s).
	Lease time.Duration
	// MaxAttempts is how many times an item that fails is retried (default 3).
	MaxAttempts int
	// Poll is the longest a worker waits between claim attempts when no notification
	// arrives (default 1s).
	Poll time.Duration
}

// NewWorkQueue returns an a2a-go work queue backed by db for cluster mode. workerID
// identifies this process in work_items.worker.
func NewWorkQueue(db *DB, workerID string, cfg WorkQueueConfig) workqueue.Queue {
	return NewWorkQueueBackend(db, workerID, cfg).Queue()
}

// WorkQueue is the workqueue.ReadWriter behind NewWorkQueue.
type WorkQueue struct {
	db     *DB
	worker string
	cfg    WorkQueueConfig

	closing  context.Context
	close    context.CancelFunc
	inFlight sync.WaitGroup
}

var _ workqueue.ReadWriter = (*WorkQueue)(nil)

// NewWorkQueueBackend returns the ReadWriter that NewWorkQueue wraps.
func NewWorkQueueBackend(db *DB, workerID string, cfg WorkQueueConfig) *WorkQueue {
	if cfg.Lease <= 0 {
		cfg.Lease = 10 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	closing, closeFn := context.WithCancel(context.Background())
	return &WorkQueue{db: db, worker: workerID, cfg: cfg, closing: closing, close: closeFn}
}

// Queue returns the a2a-go queue that pulls from q. Executions run with a context that
// Close cancels.
func (q *WorkQueue) Queue() workqueue.Queue {
	return workqueue.NewPullQueue(q, &workqueue.PullQueueConfig{
		BeforeExecutionCallback: func(ctx context.Context, _ workqueue.Message) (context.Context, error) {
			ctx, cancel := context.WithCancel(ctx)
			context.AfterFunc(q.closing, cancel)
			return ctx, nil
		},
	})
}

// Close stops claiming work and cancels running executions, then waits up to wait for
// them to hand their items back. Items returned this way don't count as an attempt and
// are free for another worker at once; items that aren't returned in time are picked up
// when their lease expires.
func (q *WorkQueue) Close(wait time.Duration) {
	q.close()
	done := make(chan struct{})
	go func() {
		q.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// Write implements workqueue.Writer. Only one execution per task can be pending or
// running at a time.
func (q *WorkQueue) Write(ctx context.Context, payload *workqueue.Payload) (a2a.TaskID, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	_, err = q.db.Pool.Exec(ctx, `INSERT INTO work_items (task_id, kind, payload) VALUES ($1, $2, $3)`,
		payload.TaskID, string(payload.Type), raw)
	if IsUniqueViolation(err) {
		return "", fmt.Errorf("an execution is already in progress for task %s: %w", payload.TaskID, a2a.ErrInvalidRequest)
	}
	if err != nil {
		return "", err
	}
	_ = q.db.Notify(ctx, ChannelWork, "")
	return payload.TaskID, nil
}

// Read implements workqueue.ReadWriter. It blocks until an item can be claimed.
func (q *WorkQueue) Read(ctx context.Context) (workqueue.Message, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(q.closing, cancel)
	defer stop()
	wake, unsubscribe := q.db.Subscribe(ChannelWork)
	defer unsubscribe()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		msg, err := q.claim(ctx)
		if err != nil || msg != nil {
			return msg, err
		}
		timer.Reset(q.cfg.Poll)
		select {
		case <-ctx.Done():
			return nil, workqueue.ErrQueueClosed
		case <-wake:
		case <-timer.C:
		}
	}
}

func (q *WorkQueue) claim(ctx context.Context) (workqueue.Message, error) {
	var id int64
	var raw []byte
	err := q.db.Pool.QueryRow(ctx, `UPDATE work_items SET state = 'working', worker = $1, attempts = attempts + 1,
			lease_until = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = (
			SELECT id FROM work_items
			WHERE state IN ('pending', 'working') AND lease_until <= now()
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, payload`, q.worker, q.cfg.Lease.Seconds()).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, workqueue.ErrQueueClosed
		}
		return nil, err
	}
	var payload workqueue.Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		_, _ = q.db.Pool.Exec(ctx, `UPDATE work_items SET state = 'failed', cause = $2 WHERE id = $1`, id, err.Error())
		return nil, nil
	}
	q.inFlight.Add(1)
	return &workMessage{q: q, id: id, payload: &payload}, nil
}

type workMessage struct {
	q       *WorkQueue
	id      int64
	payload *workqueue.Payload
	settled sync.Once
}

func (m *workMessage) settle() { m.settled.Do(m.q.inFlight.Done) }

var (
	_ workqueue.Message     = (*workMessage)(nil)
	_ workqueue.Heartbeater = (*workMessage)(nil)
)

func (m *workMessage) Payload() *workqueue.Payload { return m.payload }

func (m *workMessage) HeartbeatInterval() time.Duration { return m.q.cfg.Lease / 3 }

func (m *workMessage) Heartbeat(ctx context.Context) error {
	_, err := m.q.db.Pool.Exec(ctx, `UPDATE work_items SET lease_until = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = $1 AND state = 'working'`, m.id, m.q.cfg.Lease.Seconds())
	return err
}

func (m *workMessage) Complete(ctx context.Context) error {
	defer m.settle()
	_, err := m.q.db.Pool.Exec(context.WithoutCancel(ctx), `UPDATE work_items SET state = 'done', updated_at = now() WHERE id = $1`, m.id)
	return err
}

// Return puts the item back for another attempt after a short backoff, or marks it
// failed once it has been tried MaxAttempts times.
func (m *workMessage) Return(ctx context.Context, cause error) error {
	defer m.settle()
	ctx = context.WithoutCancel(ctx)
	if m.q.closing.Err() != nil {
		// The worker is shutting down: hand the item back untouched.
		_, err := m.q.db.Pool.Exec(ctx, `UPDATE work_items SET state = 'pending', attempts = greatest(attempts - 1, 0),
			lease_until = now(), worker = '', updated_at = now() WHERE id = $1`, m.id)
		return err
	}
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	_, err := m.q.db.Pool.Exec(ctx, `UPDATE work_items SET
			state = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'pending' END,
			lease_until = now() + (attempts * interval '1 second'),
			cause = $2, updated_at = now()
		WHERE id = $1`, m.id, reason, m.q.cfg.MaxAttempts)
	return err
}

// NewEventQueueManager returns a2a-go's pull event queue reading the task_events table,
// so a subscriber on any replica follows a task executed on another one.
func NewEventQueueManager(db *DB, poll time.Duration) eventqueue.Manager {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	inner := eventqueue.NewPullQueueManager(func(ctx context.Context, taskID a2a.TaskID) (eventqueue.Puller, error) {
		var from int64
		if err := db.Pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM task_events WHERE task_id = $1`, taskID).Scan(&from); err != nil {
			return nil, err
		}
		return &puller{db: db, from: from}, nil
	}, eventqueue.PullConfig{PollInterval: poll})
	return &eventManager{Manager: inner, db: db}
}

// eventManager persists the events the task store doesn't. a2a-go's pull manager only
// writes events to memory and expects the task store to persist them, which it does for
// task events. An agent that answers with a message and no task changes no task, so
// without this a subscriber on another replica, or the frontend of this one, would
// never see the answer.
type eventManager struct {
	eventqueue.Manager
	db *DB
}

func (m *eventManager) CreateWriter(ctx context.Context, taskID a2a.TaskID) (eventqueue.Writer, error) {
	w, err := m.Manager.CreateWriter(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return &eventWriter{Writer: w, db: m.db, taskID: taskID}, nil
}

type eventWriter struct {
	eventqueue.Writer
	db     *DB
	taskID a2a.TaskID
}

func (w *eventWriter) Write(ctx context.Context, msg *eventqueue.Message) error {
	if m, ok := msg.Event.(*a2a.Message); ok && m.Role == a2a.MessageRoleAgent && msg.TaskVersion == taskstore.TaskVersionMissing {
		raw, err := json.Marshal(&eventqueue.Message{Event: m, TaskVersion: taskstore.TaskVersionMissing, Protocol: a2a.Version})
		if err != nil {
			return err
		}
		if _, err := w.db.Pool.Exec(ctx, `INSERT INTO task_events (task_id, version, message) VALUES ($1, 0, $2)`, w.taskID, raw); err != nil {
			return fmt.Errorf("pg: persist message reply: %w", err)
		}
	}
	return w.Writer.Write(ctx, msg)
}

type puller struct {
	db   *DB
	from int64
}

func (p *puller) Pull(ctx context.Context, taskID a2a.TaskID, cursor eventqueue.PullCursor) (*eventqueue.PullResponse, error) {
	after, ok := cursor.(int64)
	if !ok {
		after = p.from
	}
	rows, err := p.db.Pool.Query(ctx, `SELECT id, message FROM task_events WHERE task_id = $1 AND id > $2 ORDER BY id LIMIT 50`, taskID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &eventqueue.PullResponse{Cursor: after}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var msg eventqueue.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, fmt.Errorf("pg: decode event %d: %w", id, err)
		}
		resp.Messages = append(resp.Messages, &msg)
		resp.Cursor = id
	}
	return resp, rows.Err()
}

func (p *puller) Close(context.Context) error { return nil }
