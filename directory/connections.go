package directory

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/pg"
)

// Who may start a task with an address (Address.Access).
const (
	// AccessPublic lets anyone call, without credentials.
	AccessPublic = "public"
	// AccessWorkspace lets the address's workspaces call.
	AccessWorkspace = "workspace"
	// AccessConnections lets the address's workspaces call, and the workspaces it
	// accepted a connection from.
	AccessConnections = "connections"
)

// ValidAccess reports whether access is one of the three.
func ValidAccess(access string) bool {
	return access == AccessPublic || access == AccessWorkspace || access == AccessConnections
}

// Statuses of a Connection.
const (
	ConnPending  = "pending"
	ConnAccepted = "accepted"
	ConnDeclined = "declined"
	ConnBlocked  = "blocked"
)

// Connection is a workspace's standing to reach an address of another workspace.
type Connection struct {
	ID int64 `json:"id"`
	// From is the workspace asking; FromName its name.
	From     int64  `json:"from"`
	FromName string `json:"fromName"`
	To       string `json:"to"`
	Status   string `json:"status"`
	Note     string `json:"note"`
	// RatePerMin caps how many calls a minute the workspace may make to the address.
	RatePerMin  int        `json:"ratePerMin"`
	RequestedBy string     `json:"requestedBy,omitempty"`
	DecidedBy   string     `json:"decidedBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
}

const connColumns = `c.id, c.from_workspace, w.name, c.to_handle, c.status, c.note, c.rate_per_min,
	coalesce(rq.handle, ''), coalesce(dc.handle, ''), c.created_at, c.decided_at`

const connFrom = ` FROM connections c JOIN workspaces w ON w.id = c.from_workspace
	LEFT JOIN users rq ON rq.id = c.requested_by LEFT JOIN users dc ON dc.id = c.decided_by `

func scanConn(row pgx.Row) (*Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.From, &c.FromName, &c.To, &c.Status, &c.Note, &c.RatePerMin, &c.RequestedBy, &c.DecidedBy, &c.CreatedAt, &c.DecidedAt)
	return &c, err
}

func (d *Directory) conns(ctx context.Context, where string, args ...any) ([]*Connection, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT `+connColumns+connFrom+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Connection, error) { return scanConn(row) })
}

// ErrBlocked means the address blocked the workspace.
var ErrBlocked = errors.New("directory: blocked")

// RequestConnection asks for workspace from to reach handle. Asking again after a
// decline asks anew; a pending or accepted connection is returned as it is; a blocked
// one is ErrBlocked.
func (d *Directory) RequestConnection(ctx context.Context, from int64, handle, note string, by int64) (*Connection, error) {
	var id int64
	var status string
	err := d.db.Pool.QueryRow(ctx, `INSERT INTO connections (from_workspace, to_handle, status, note, requested_by) VALUES ($1, $2, 'pending', $3, $4)
		ON CONFLICT (from_workspace, to_handle) DO UPDATE SET
			status = CASE WHEN connections.status = 'declined' THEN 'pending' ELSE connections.status END,
			note = CASE WHEN connections.status = 'declined' THEN excluded.note ELSE connections.note END,
			requested_by = CASE WHEN connections.status = 'declined' THEN excluded.requested_by ELSE connections.requested_by END,
			created_at = CASE WHEN connections.status = 'declined' THEN now() ELSE connections.created_at END
		RETURNING id, status`, from, handle, note, nullID(by)).Scan(&id, &status)
	if pg.IsForeignKeyViolation(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == ConnBlocked {
		return nil, ErrBlocked
	}
	return d.Connection(ctx, id)
}

// Connection returns one connection.
func (d *Directory) Connection(ctx context.Context, id int64) (*Connection, error) {
	c, err := scanConn(d.db.Pool.QueryRow(ctx, `SELECT `+connColumns+connFrom+`WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ConnectionsFrom lists the connections a workspace asked for, newest first.
func (d *Directory) ConnectionsFrom(ctx context.Context, workspaceID int64) ([]*Connection, error) {
	return d.conns(ctx, `WHERE c.from_workspace = $1 ORDER BY c.created_at DESC`, workspaceID)
}

// ConnectionsTo lists the connections asked of the given addresses, pending first.
func (d *Directory) ConnectionsTo(ctx context.Context, handles []string) ([]*Connection, error) {
	return d.conns(ctx, `WHERE c.to_handle = ANY($1) ORDER BY c.status <> 'pending', c.created_at DESC`, handles)
}

// DecideConnection sets a connection's status (and rate, when above zero).
func (d *Directory) DecideConnection(ctx context.Context, id int64, status string, ratePerMin int, by int64) error {
	switch status {
	case ConnAccepted, ConnDeclined, ConnBlocked:
	default:
		return errors.New("directory: a connection is accepted, declined or blocked")
	}
	tag, err := d.db.Pool.Exec(ctx, `UPDATE connections SET status = $2, rate_per_min = CASE WHEN $3 > 0 THEN $3 ELSE rate_per_min END,
		decided_by = $4, decided_at = now() WHERE id = $1`, id, status, ratePerMin, nullID(by))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteConnection removes a connection: withdrawn by the asker, or ended by the address.
func (d *Directory) DeleteConnection(ctx context.Context, id int64) error {
	_, err := d.db.Pool.Exec(ctx, `DELETE FROM connections WHERE id = $1`, id)
	return err
}

// Reach is how a caller may reach an address: the connection that lets it, if any.
type Reach struct {
	// SameWorkspace is true when caller and address share a workspace.
	SameWorkspace bool
	// Connection is the accepted connection from one of the caller's workspaces.
	Connection *Connection
	// Blocked is true when the address blocked one of the caller's workspaces.
	Blocked bool
}

// ReachOf works out how caller (an address) stands with the address handle: through a
// shared workspace, an accepted connection, or not at all.
func (d *Directory) ReachOf(ctx context.Context, caller, handle string) (*Reach, error) {
	var r Reach
	err := d.db.Pool.QueryRow(ctx, `SELECT EXISTS ((`+workspacesOf(1)+`) INTERSECT (`+workspacesOf(2)+`))`, caller, handle).Scan(&r.SameWorkspace)
	if err != nil || r.SameWorkspace {
		return &r, err
	}
	conns, err := d.conns(ctx, `WHERE c.to_handle = $2 AND c.from_workspace IN (`+workspacesOf(1)+`) AND c.status IN ('accepted', 'blocked')
		ORDER BY c.status = 'blocked' DESC`, caller, handle)
	if err != nil || len(conns) == 0 {
		return &r, err
	}
	if conns[0].Status == ConnBlocked {
		r.Blocked = true
	} else {
		r.Connection = conns[0]
	}
	return &r, nil
}

// workspacesOf selects the IDs of the workspaces of the address in parameter $n: an
// agent's, or a person's memberships.
func workspacesOf(n int) string {
	p := "$" + strconv.Itoa(n)
	return `SELECT workspace_id FROM addresses WHERE handle = ` + p + ` AND workspace_id IS NOT NULL
	UNION SELECT m.workspace_id FROM members m JOIN users u ON u.id = m.user_id WHERE u.handle = ` + p
}

// SetAccess changes who may start a task with an address.
func (d *Directory) SetAccess(ctx context.Context, handle, access string) error {
	if !ValidAccess(access) {
		return errors.New("directory: access is public, workspace or connections")
	}
	_, err := d.db.Pool.Exec(ctx, `UPDATE addresses SET access = $2, updated_at = now() WHERE handle = $1`, handle, access)
	return err
}
