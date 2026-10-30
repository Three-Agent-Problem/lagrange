package pg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/eventqueue"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/jackc/pgx/v5"
)

// TaskStore implements taskstore.Store. A task belongs to the tenant (the address) and
// the user (the sender) of the call that created it, both taken from the a2asrv
// CallContext. Calls from another user or tenant see a2a.ErrTaskNotFound, as the spec
// asks (3.3.2). Each change also appends an event for the pull event queue.
type TaskStore struct {
	db *DB
}

var _ taskstore.Store = (*TaskStore)(nil)

// NewTaskStore returns a task store backed by db.
func NewTaskStore(db *DB) *TaskStore { return &TaskStore{db: db} }

func caller(ctx context.Context) (user, tenant string) {
	if callCtx, ok := a2asrv.CallContextFrom(ctx); ok {
		if callCtx.User != nil {
			user = callCtx.User.Name
		}
		tenant = callCtx.Tenant()
	}
	return user, tenant
}

// Create implements taskstore.Store.
func (s *TaskStore) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	if err := validateTask(task); err != nil {
		return taskstore.TaskVersionMissing, err
	}
	user, tenant := caller(ctx)
	raw, err := json.Marshal(task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	const version = 1
	err = pgx.BeginFunc(ctx, s.db.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tasks (id, tenant, owner, context_id, state, version, task)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			task.ID, tenant, user, task.ContextID, task.Status.State, version, raw)
		if IsUniqueViolation(err) {
			return taskstore.ErrTaskAlreadyExists
		}
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, task.ID, version, task)
	})
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	return version, nil
}

// Update implements taskstore.Store.
func (s *TaskStore) Update(ctx context.Context, req *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	if err := validateTask(req.Task); err != nil {
		return taskstore.TaskVersionMissing, err
	}
	user, tenant := caller(ctx)
	raw, err := json.Marshal(req.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	var version int64
	err = pgx.BeginFunc(ctx, s.db.Pool, func(tx pgx.Tx) error {
		var owner, storedTenant string
		var current int64
		err := tx.QueryRow(ctx, `SELECT owner, tenant, version FROM tasks WHERE id = $1 FOR UPDATE`, req.Task.ID).
			Scan(&owner, &storedTenant, &current)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !visible(owner, storedTenant, user, tenant)) {
			return a2a.ErrTaskNotFound
		}
		if err != nil {
			return err
		}
		if req.PrevVersion != taskstore.TaskVersionMissing && current != int64(req.PrevVersion) {
			return taskstore.ErrConcurrentModification
		}
		version = current + 1
		if _, err := tx.Exec(ctx, `UPDATE tasks SET context_id = $2, state = $3, version = $4, task = $5, updated_at = now()
			WHERE id = $1`, req.Task.ID, req.Task.ContextID, req.Task.Status.State, version, raw); err != nil {
			return err
		}
		return insertEvent(ctx, tx, req.Task.ID, version, req.Event)
	})
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	return taskstore.TaskVersion(version), nil
}

// Get implements taskstore.Store.
func (s *TaskStore) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	stored, tenant, err := s.Load(ctx, taskID)
	if err != nil {
		return nil, err
	}
	user, callTenant := caller(ctx)
	if !visible(stored.User, tenant, user, callTenant) {
		return nil, a2a.ErrTaskNotFound
	}
	return stored, nil
}

// Load returns a task and its tenant without checking who is asking. It is for the
// platform's own code (the inbox, the graph), never for A2A callers.
func (s *TaskStore) Load(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, string, error) {
	var raw []byte
	var owner, tenant string
	var version int64
	err := s.db.Pool.QueryRow(ctx, `SELECT task, owner, tenant, version FROM tasks WHERE id = $1`, taskID).
		Scan(&raw, &owner, &tenant, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, "", err
	}
	var task a2a.Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, "", fmt.Errorf("pg: decode task %s: %w", taskID, err)
	}
	return &taskstore.StoredTask{Task: &task, Version: taskstore.TaskVersion(version), User: owner}, tenant, nil
}

// visible reports whether a caller may see a task. An empty call tenant means the call
// did not come through an address (the platform's own calls).
func visible(owner, tenant, user, callTenant string) bool {
	return owner == user && (callTenant == "" || callTenant == tenant)
}

const defaultPageSize = 50

// List implements taskstore.Store. Like a2a-go's in-memory store it lists the caller's
// own tasks, newest first, and rejects anonymous callers.
func (s *TaskStore) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	user, tenant := caller(ctx)
	if user == "" {
		return nil, a2a.ErrUnauthenticated
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	} else if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("page size must be between 1 and 100 inclusive, got %d: %w", pageSize, a2a.ErrInvalidRequest)
	}

	where := []string{"owner = $1"}
	args := []any{user}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if tenant != "" {
		add("tenant = $%d", tenant)
	}
	if req.ContextID != "" {
		add("context_id = $%d", req.ContextID)
	}
	if req.Status != a2a.TaskStateUnspecified {
		add("state = $%d", string(req.Status))
	}
	if req.StatusTimestampAfter != nil {
		add("coalesce((task->'status'->>'timestamp')::timestamptz, updated_at) >= $%d", *req.StatusTimestampAfter)
	}
	filter := strings.Join(where, " AND ")

	var total int
	if err := s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE "+filter, args...).Scan(&total); err != nil {
		return nil, err
	}

	pageArgs := append([]any(nil), args...)
	page := filter
	if req.PageToken != "" {
		at, id, err := decodePageToken(req.PageToken)
		if err != nil {
			return nil, err
		}
		pageArgs = append(pageArgs, at, id)
		page += fmt.Sprintf(" AND (updated_at, id) < ($%d, $%d)", len(pageArgs)-1, len(pageArgs))
	}
	pageArgs = append(pageArgs, pageSize+1)
	rows, err := s.db.Pool.Query(ctx, fmt.Sprintf(
		"SELECT task, updated_at FROM tasks WHERE %s ORDER BY updated_at DESC, id DESC LIMIT $%d", page, len(pageArgs)), pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	historyLength := 100
	if req.HistoryLength != nil {
		historyLength = *req.HistoryLength
	}
	var tasks []*a2a.Task
	var updated []time.Time
	for rows.Next() {
		var raw []byte
		var at time.Time
		if err := rows.Scan(&raw, &at); err != nil {
			return nil, err
		}
		updated = append(updated, at)
		var task a2a.Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, err
		}
		if historyLength <= 0 {
			task.History = []*a2a.Message{}
		} else if len(task.History) > historyLength {
			task.History = task.History[len(task.History)-historyLength:]
		}
		if !req.IncludeArtifacts {
			task.Artifacts = nil
		}
		tasks = append(tasks, &task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var next string
	if len(tasks) > pageSize {
		tasks = tasks[:pageSize]
		// The cursor is the last task on this page.
		next = encodePageToken(updated[pageSize-1], tasks[pageSize-1].ID)
	}
	if tasks == nil {
		tasks = []*a2a.Task{}
	}
	return &a2a.ListTasksResponse{Tasks: tasks, TotalSize: total, PageSize: pageSize, NextPageToken: next}, nil
}

func encodePageToken(at time.Time, id a2a.TaskID) string {
	return base64.URLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "_" + string(id)))
}

func decodePageToken(token string) (time.Time, a2a.TaskID, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	at, id, ok := strings.Cut(string(raw), "_")
	if !ok {
		return time.Time{}, "", a2a.ErrParseError
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", a2a.ErrParseError
	}
	return t, a2a.TaskID(id), nil
}

func validateTask(task *a2a.Task) error {
	if task == nil || task.ID == "" {
		return fmt.Errorf("task ID is required: %w", a2a.ErrInvalidParams)
	}
	if task.ContextID == "" {
		return fmt.Errorf("context ID is required: %w", a2a.ErrInvalidParams)
	}
	return nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, taskID a2a.TaskID, version int64, event a2a.Event) error {
	// A user message only appends to the task's history. Publishing it would hand it to
	// cluster-mode subscribers as if the agent had answered with a message.
	if msg, ok := event.(*a2a.Message); ok && msg.Role == a2a.MessageRoleUser {
		return nil
	}
	raw, err := json.Marshal(&eventqueue.Message{Event: event, TaskVersion: taskstore.TaskVersion(version), Protocol: a2a.Version})
	if err != nil {
		return fmt.Errorf("pg: encode event: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_events (task_id, version, message) VALUES ($1, $2, $3)`, taskID, version, raw)
	return err
}
