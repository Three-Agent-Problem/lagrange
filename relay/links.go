package relay

import (
	"context"
	"errors"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/pg"
)

// Link is what the relay knows about a platform task beyond the A2A task itself.
type Link struct {
	TaskID       a2a.TaskID `json:"taskId"`
	Sender       string     `json:"sender"`
	Recipient    string     `json:"recipient"`
	ParentTaskID a2a.TaskID `json:"parentTaskId,omitempty"`
	// RemoteTaskID and RemoteContextID are the agent's own IDs for the task, behind the
	// tunnel. They stay empty for people and for agents that answered with a message.
	RemoteTaskID    a2a.TaskID `json:"-"`
	RemoteContextID string     `json:"-"`
}

// Links stores Link rows.
type Links struct {
	db *pg.DB
}

// NewLinks returns a link store on db.
func NewLinks(db *pg.DB) *Links { return &Links{db: db} }

// Create stores l unless the task already has a link.
func (s *Links) Create(ctx context.Context, l *Link) error {
	_, err := s.db.Pool.Exec(ctx, `INSERT INTO task_links (task_id, sender, recipient, parent_task_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (task_id) DO NOTHING`, l.TaskID, l.Sender, l.Recipient, l.ParentTaskID)
	return err
}

// Get returns the link of a task, or nil.
func (s *Links) Get(ctx context.Context, taskID a2a.TaskID) (*Link, error) {
	l := &Link{TaskID: taskID}
	err := s.db.Pool.QueryRow(ctx, `SELECT sender, recipient, parent_task_id, remote_task_id, remote_context_id FROM task_links WHERE task_id = $1`, taskID).
		Scan(&l.Sender, &l.Recipient, &l.ParentTaskID, &l.RemoteTaskID, &l.RemoteContextID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// SetRemote records the agent's own task and context IDs.
func (s *Links) SetRemote(ctx context.Context, taskID, remoteTaskID a2a.TaskID, remoteContextID string) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE task_links SET remote_task_id = $2, remote_context_id = $3 WHERE task_id = $1`, taskID, remoteTaskID, remoteContextID)
	return err
}

// SetParent records that child was delegated from parent, if child doesn't have a parent
// yet and was sent by parent's recipient (so an agent can only claim its own calls).
func (s *Links) SetParent(ctx context.Context, child, parent a2a.TaskID) error {
	_, err := s.db.Pool.Exec(ctx, `UPDATE task_links c SET parent_task_id = $2
		FROM task_links p
		WHERE c.task_id = $1 AND p.task_id = $2 AND c.parent_task_id = '' AND c.sender = p.recipient`, child, parent)
	return err
}

// ValidParent reports whether parent is a task that sender is currently the recipient
// of, which is what makes "sender is delegating from parent" believable.
func (s *Links) ValidParent(ctx context.Context, parent a2a.TaskID, sender string) bool {
	if parent == "" || sender == "" {
		return false
	}
	var ok bool
	err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_links WHERE task_id = $1 AND recipient = $2)`, parent, sender).Scan(&ok)
	return err == nil && ok
}

// Children returns the links whose parent is taskID.
func (s *Links) Children(ctx context.Context, taskID a2a.TaskID) ([]*Link, error) {
	rows, err := s.db.Pool.Query(ctx, `SELECT task_id, sender, recipient, parent_task_id FROM task_links WHERE parent_task_id = $1`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Link, error) {
		var l Link
		return &l, row.Scan(&l.TaskID, &l.Sender, &l.Recipient, &l.ParentTaskID)
	})
}
