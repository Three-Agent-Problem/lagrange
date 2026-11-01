package directory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/pg"
)

// Roles in a workspace, from most to least power.
const (
	// RoleOwner manages members and roles, and everything an admin does.
	RoleOwner = "owner"
	// RoleAdmin manages agents, tokens, invitations and the waiting rules.
	RoleAdmin = "admin"
	// RoleMember sends, answers, cancels and hides runs.
	RoleMember = "member"
	// RoleViewer only looks.
	RoleViewer = "viewer"
)

var roleRank = map[string]int{RoleViewer: 1, RoleMember: 2, RoleAdmin: 3, RoleOwner: 4}

// ValidRole reports whether role is one of the four roles.
func ValidRole(role string) bool { return roleRank[role] > 0 }

// RoleAtLeast reports whether role has at least the power of min.
func RoleAtLeast(role, min string) bool { return roleRank[role] >= roleRank[min] }

// DefaultWorkspace is the slug of the workspace that existed before there were
// workspaces; people an admin creates join it.
const DefaultWorkspace = "default"

var (
	// ErrLastOwner means the change would leave a workspace without an owner.
	ErrLastOwner = errors.New("directory: a workspace needs at least one owner")
	// ErrNotMember means the person isn't a member of the workspace.
	ErrNotMember = errors.New("directory: not a member of that workspace")
)

// Workspace is a team: its agents and its members.
type Workspace struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Role is the reader's role in it, when listed for someone.
	Role string `json:"role,omitempty"`
}

// CreateWorkspace creates a workspace owned by ownerID. The slug comes from hint (made
// unique); the name is name.
func (d *Directory) CreateWorkspace(ctx context.Context, name, hint string, ownerID int64) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("directory: name the workspace")
	}
	slug, err := d.free(ctx, firstHint(hint, name), `SELECT EXISTS (SELECT 1 FROM workspaces WHERE slug = $1)`)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{Slug: slug, Name: name, Role: RoleOwner}
	err = pgx.BeginFunc(ctx, d.db.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (slug, name) VALUES ($1, $2) RETURNING id`, slug, name).Scan(&ws.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, ws.ID, ownerID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ws, nil
}

func firstHint(hints ...string) string {
	for _, h := range hints {
		if strings.TrimSpace(h) != "" {
			return h
		}
	}
	return ""
}

// Default returns the default workspace, creating it if needed.
func (d *Directory) Default(ctx context.Context) (*Workspace, error) {
	ws := &Workspace{}
	err := d.db.Pool.QueryRow(ctx, `INSERT INTO workspaces (slug, name) VALUES ($1, 'Default')
		ON CONFLICT (slug) DO UPDATE SET slug = excluded.slug RETURNING id, slug, name`, DefaultWorkspace).Scan(&ws.ID, &ws.Slug, &ws.Name)
	return ws, err
}

// Workspaces lists the workspaces a person is a member of, oldest membership first,
// with their role.
func (d *Directory) Workspaces(ctx context.Context, userID int64) ([]*Workspace, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT w.id, w.slug, w.name, m.role FROM members m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1 ORDER BY m.created_at, w.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Workspace, error) {
		var w Workspace
		return &w, row.Scan(&w.ID, &w.Slug, &w.Name, &w.Role)
	})
}

// Membership returns the workspace with slug and the person's role in it, or
// ErrNotMember.
func (d *Directory) Membership(ctx context.Context, userID int64, slug string) (*Workspace, error) {
	var w Workspace
	err := d.db.Pool.QueryRow(ctx, `SELECT w.id, w.slug, w.name, m.role FROM members m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1 AND w.slug = $2`, userID, slug).Scan(&w.ID, &w.Slug, &w.Name, &w.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotMember
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// RenameWorkspace changes a workspace's name (its slug stays).
func (d *Directory) RenameWorkspace(ctx context.Context, id int64, name string) error {
	if name = strings.TrimSpace(name); name == "" {
		return errors.New("directory: name the workspace")
	}
	_, err := d.db.Pool.Exec(ctx, `UPDATE workspaces SET name = $2 WHERE id = $1`, id, name)
	return err
}

// Member is a person in a workspace.
type Member struct {
	User
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

// Members lists a workspace's members, owners first.
func (d *Directory) Members(ctx context.Context, workspaceID int64) ([]*Member, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT `+userColumns+`, m.role, m.created_at
		FROM members m JOIN users ON users.id = m.user_id WHERE m.workspace_id = $1 ORDER BY m.created_at, users.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Member, error) {
		var m Member
		return &m, row.Scan(&m.ID, &m.Email, &m.Name, &m.Handle, &m.Admin, &m.Verified, &m.HasPassword, &m.Role, &m.JoinedAt)
	})
	slices.SortStableFunc(members, func(a, b *Member) int { return roleRank[b.Role] - roleRank[a.Role] })
	return members, err
}

// AddMember adds a person to a workspace. Someone already in it is ErrTaken.
func (d *Directory) AddMember(ctx context.Context, workspaceID, userID int64, role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("directory: no such role %q", role)
	}
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO members (workspace_id, user_id, role) VALUES ($1, $2, $3)`, workspaceID, userID, role)
	if pg.IsUniqueViolation(err) {
		return ErrTaken
	}
	return err
}

// SetRole changes a member's role, keeping at least one owner.
func (d *Directory) SetRole(ctx context.Context, workspaceID, userID int64, role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("directory: no such role %q", role)
	}
	return d.changeMembers(ctx, workspaceID, `UPDATE members SET role = $3 WHERE workspace_id = $1 AND user_id = $2`, userID, role)
}

// RemoveMember takes a person out of a workspace, keeping at least one owner.
func (d *Directory) RemoveMember(ctx context.Context, workspaceID, userID int64) error {
	return d.changeMembers(ctx, workspaceID, `DELETE FROM members WHERE workspace_id = $1 AND user_id = $2`, userID)
}

func (d *Directory) changeMembers(ctx context.Context, workspaceID int64, query string, userID int64, args ...any) error {
	return pgx.BeginFunc(ctx, d.db.Pool, func(tx pgx.Tx) error {
		// Lock the workspace so two owners can't demote each other at the same time.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE`, workspaceID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, query, append([]any{workspaceID, userID}, args...)...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotMember
		}
		var owners int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM members WHERE workspace_id = $1 AND role = 'owner'`, workspaceID).Scan(&owners); err != nil {
			return err
		}
		if owners == 0 {
			return ErrLastOwner
		}
		return nil
	})
}

// WorkspaceHandles lists the addresses a workspace sees the traffic of: its agents and
// its members' own addresses.
func (d *Directory) WorkspaceHandles(ctx context.Context, workspaceID int64) ([]string, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT handle FROM addresses WHERE workspace_id = $1
		UNION SELECT u.handle FROM members m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// AddressesIn lists a workspace's addresses, people first, then agents, by handle.
func (d *Directory) AddressesIn(ctx context.Context, workspaceID int64) ([]*Address, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT `+addressColumns+` FROM addresses WHERE workspace_id = $1
			OR handle IN (SELECT u.handle FROM members m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1)
		ORDER BY kind DESC, handle`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Address, error) { return scanAddress(row) })
}

// HandleWorkspaces maps each address to the workspaces it belongs to: an agent's one
// workspace, or every workspace a person is a member of (oldest first).
func (d *Directory) HandleWorkspaces(ctx context.Context) (map[string][]int64, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT handle, workspace_id FROM addresses WHERE workspace_id IS NOT NULL
		UNION ALL SELECT u.handle, m.workspace_id FROM members m JOIN users u ON u.id = m.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var h string
		var id int64
		if err := rows.Scan(&h, &id); err != nil {
			return nil, err
		}
		out[h] = append(out[h], id)
	}
	return out, rows.Err()
}

// free turns hint into a slug-shaped name for which exists (a query taking the name)
// is false, adding -2, -3, … when needed.
func (d *Directory) free(ctx context.Context, hint, exists string) (string, error) {
	base := handleFrom(hint)
	for i := 1; i <= 50; i++ {
		h := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			h = strings.TrimRight(base[:min(len(base), 40-len(suffix))], "-") + suffix
		}
		var taken bool
		if err := d.db.Pool.QueryRow(ctx, exists, h).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return h, nil
		}
	}
	return "", ErrTaken
}

// WorkspaceByID returns a workspace (without a role).
func (d *Directory) WorkspaceByID(ctx context.Context, id int64) (*Workspace, error) {
	var w Workspace
	err := d.db.Pool.QueryRow(ctx, `SELECT id, slug, name FROM workspaces WHERE id = $1`, id).Scan(&w.ID, &w.Slug, &w.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}
