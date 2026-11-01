// Package directory stores who exists on a Lagrange instance: people (users, with a
// human address each), agent addresses, the bearer tokens agents use, and web sessions.
//
// An address is a handle such as "acme-buyer" or "kaan". It is the A2A tenant: the
// address's agent card lives at /a/<handle>/.well-known/agent-card.json, and every call
// to /a/<handle> is a task for it. An agent address is reached through a tunnel (no
// endpoint) or directly at a public endpoint.
package directory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/three-agent-problem/lagrange/pg"
)

// Kinds of address.
const (
	KindAgent = "agent"
	KindHuman = "human"
)

var (
	// ErrNotFound means no such user, address, token or session.
	ErrNotFound = errors.New("directory: not found")
	// ErrTaken means the handle or email is already in use.
	ErrTaken = errors.New("directory: already taken")
	// ErrInvalidHandle means the handle doesn't match HandlePattern.
	ErrInvalidHandle = errors.New("directory: a handle is 2-40 characters: lowercase letters, digits and dashes, starting with a letter or digit")
	// ErrBadCredentials means the email or password is wrong.
	ErrBadCredentials = errors.New("directory: wrong email or password")
	// ErrShortPassword means the password has fewer than 8 characters.
	ErrShortPassword = errors.New("directory: the password needs at least 8 characters")
	// ErrInvalidEmail means the email has no @.
	ErrInvalidEmail = errors.New("directory: that doesn't look like an email address")
)

// HandlePattern is what a handle must look like.
var HandlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

// SessionTTL is how long a web session lasts.
const SessionTTL = 30 * 24 * time.Hour

// User is a person who can log in.
type User struct {
	ID     int64  `json:"id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Handle string `json:"handle"`
	Admin  bool   `json:"admin"`
	// Verified means the person proved they own Email (or an admin vouched for them).
	Verified bool `json:"verified"`
	// HasPassword is false for people who log in only with links or a provider.
	HasPassword bool `json:"hasPassword"`
}

// Address is an agent or a person that can receive tasks.
type Address struct {
	Handle      string `json:"handle"`
	Kind        string `json:"kind"`
	OwnerID     int64  `json:"ownerId,omitempty"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// Endpoint is the agent's public base URL. Empty means it connects through a tunnel.
	Endpoint string `json:"endpoint,omitempty"`
	// Access is who may start a task with the address (AccessPublic, AccessWorkspace or
	// AccessConnections).
	Access string `json:"access"`
	// WorkspaceID is the agent's workspace. People's addresses have none: they belong to
	// every workspace the person is a member of.
	WorkspaceID int64 `json:"workspaceId,omitempty"`
	// Card is the agent's own card as last fetched from the agent.
	Card      *a2a.AgentCard `json:"card,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Name returns the display name, or the handle when there is none.
func (a *Address) Name() string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Handle
}

// Directory is the Postgres-backed directory.
type Directory struct {
	db *pg.DB
}

// New returns a directory on db.
func New(db *pg.DB) *Directory { return &Directory{db: db} }

// CreateUser creates a person and their human address in the default workspace (admins
// as owners). An admin vouches for the email, so it counts as verified.
func (d *Directory) CreateUser(ctx context.Context, email, name, handle, password string, admin bool) (*User, error) {
	ws, err := d.Default(ctx)
	if err != nil {
		return nil, err
	}
	role := RoleMember
	if admin {
		role = RoleOwner
	}
	return d.Register(ctx, Registration{Email: email, Name: name, Handle: handle, Password: password, Admin: admin, Verified: true,
		Workspace: ws.ID, Role: role})
}

// Registration is a new person.
type Registration struct {
	Email, Name, Handle string
	// Password may be empty when the person logs in with a provider (Provider is set).
	Password string
	Admin    bool
	// Verified marks the email as proven: by an admin, an invitation link or a provider.
	Verified bool
	// Provider and Subject link an OAuth account at the same time.
	Provider, Subject string
	// Workspace, with Role, is the workspace the person joins. Zero gives them a
	// workspace of their own, as its owner.
	Workspace int64
	Role      string
}

// Register creates a person, their human address and, with Provider, their identity.
func (d *Directory) Register(ctx context.Context, r Registration) (*User, error) {
	r.Email = NormalizeEmail(r.Email)
	if !HandlePattern.MatchString(r.Handle) {
		return nil, ErrInvalidHandle
	}
	if !strings.Contains(r.Email, "@") {
		return nil, ErrInvalidEmail
	}
	if r.Name = strings.TrimSpace(r.Name); r.Name == "" {
		r.Name = r.Handle
	}
	var hash *string
	if r.Password != "" || r.Provider == "" {
		h, err := hashPassword(r.Password)
		if err != nil {
			return nil, err
		}
		hash = &h
	}
	var verified *time.Time
	if r.Verified {
		now := time.Now()
		verified = &now
	}
	u := &User{Email: r.Email, Name: r.Name, Handle: r.Handle, Admin: r.Admin, Verified: r.Verified, HasPassword: hash != nil}
	err := pgx.BeginFunc(ctx, d.db.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, name, handle, password_hash, admin, email_verified_at) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			r.Email, r.Name, r.Handle, hash, r.Admin, verified).Scan(&u.ID); err != nil {
			return err
		}
		if r.Provider != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)`, r.Provider, r.Subject, u.ID, r.Email); err != nil {
				return err
			}
		}
		// People are reached by their workspaces until they open up.
		if _, err := tx.Exec(ctx, `INSERT INTO addresses (handle, kind, owner_id, display_name, access) VALUES ($1, 'human', $2, $3, 'workspace')`, r.Handle, u.ID, r.Name); err != nil {
			return err
		}
		ws, role := r.Workspace, r.Role
		if ws == 0 {
			// The handle is free among addresses; a workspace may still have that slug.
			if err := tx.QueryRow(ctx, `INSERT INTO workspaces (slug, name)
				VALUES (CASE WHEN EXISTS (SELECT 1 FROM workspaces WHERE slug = $1) THEN $1 || '-' || substr(md5(random()::text), 1, 6) ELSE $1 END, $2)
				RETURNING id`, r.Handle, r.Name).Scan(&ws); err != nil {
				return err
			}
			role = RoleOwner
		}
		if !ValidRole(role) {
			return fmt.Errorf("directory: no such role %q", role)
		}
		_, err := tx.Exec(ctx, `INSERT INTO members (workspace_id, user_id, role) VALUES ($1, $2, $3)`, ws, u.ID, role)
		return err
	})
	if pg.IsUniqueViolation(err) {
		return nil, ErrTaken
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// NormalizeEmail trims and lowercases an email, so lookups don't depend on case.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func hashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", ErrShortPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// EnsureAdmin creates the admin user unless a user with that email exists.
func (d *Directory) EnsureAdmin(ctx context.Context, email, name, handle, password string) (*User, error) {
	if u, err := d.userWhere(ctx, "email = $1", NormalizeEmail(email)); err == nil {
		return u, nil
	}
	return d.CreateUser(ctx, email, name, handle, password, true)
}

// Login checks the password and starts a session. It returns the session token.
func (d *Directory) Login(ctx context.Context, email, password string) (*User, string, error) {
	var hash *string
	var id int64
	err := d.db.Pool.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE email = $1`, NormalizeEmail(email)).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && hash == nil {
		// Spend the same time as a real check so the response doesn't reveal the email.
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)) != nil {
		return nil, "", ErrBadCredentials
	}
	return d.StartSession(ctx, id)
}

// StartSession starts a web session for a user and returns the session token.
func (d *Directory) StartSession(ctx context.Context, userID int64) (*User, string, error) {
	token := newSecret("lgs_")
	if _, err := d.db.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), userID, time.Now().Add(SessionTTL)); err != nil {
		return nil, "", err
	}
	u, err := d.userWhere(ctx, "id = $1", userID)
	return u, token, err
}

// Session returns the user of a live session token.
func (d *Directory) Session(ctx context.Context, token string) (*User, error) {
	var id int64
	err := d.db.Pool.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash = $1 AND expires_at > now()`, hashToken(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return d.userWhere(ctx, "id = $1", id)
}

// Logout ends a session.
func (d *Directory) Logout(ctx context.Context, token string) error {
	_, err := d.db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

const userColumns = `id, email, name, handle, admin, email_verified_at IS NOT NULL, password_hash IS NOT NULL`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	return &u, row.Scan(&u.ID, &u.Email, &u.Name, &u.Handle, &u.Admin, &u.Verified, &u.HasPassword)
}

// Users lists everyone.
func (d *Directory) Users(ctx context.Context) ([]*User, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*User, error) { return scanUser(row) })
}

// UserByHandle returns the person whose address is handle.
func (d *Directory) UserByHandle(ctx context.Context, handle string) (*User, error) {
	return d.userWhere(ctx, "handle = $1", handle)
}

func (d *Directory) userWhere(ctx context.Context, cond string, arg any) (*User, error) {
	u, err := scanUser(d.db.Pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE `+cond, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// CreateAgent creates an agent address in a.WorkspaceID (the default workspace when
// zero), owned by a.OwnerID.
func (d *Directory) CreateAgent(ctx context.Context, a *Address) error {
	if !HandlePattern.MatchString(a.Handle) {
		return ErrInvalidHandle
	}
	var owner *int64
	if a.OwnerID != 0 {
		owner = &a.OwnerID
	}
	if a.WorkspaceID == 0 {
		ws, err := d.Default(ctx)
		if err != nil {
			return err
		}
		a.WorkspaceID = ws.ID
	}
	ws := a.WorkspaceID
	if a.Access == "" {
		a.Access = AccessConnections
	}
	if !ValidAccess(a.Access) {
		return fmt.Errorf("directory: access is public, workspace or connections")
	}
	err := d.db.Pool.QueryRow(ctx, `INSERT INTO addresses (handle, kind, owner_id, display_name, description, endpoint, workspace_id, access)
		VALUES ($1, 'agent', $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		a.Handle, owner, a.DisplayName, a.Description, a.Endpoint, ws, a.Access).Scan(&a.CreatedAt)
	if pg.IsUniqueViolation(err) {
		return ErrTaken
	}
	a.Kind = KindAgent
	return err
}

const addressColumns = `handle, kind, coalesce(owner_id, 0), coalesce(workspace_id, 0), access, display_name, description, endpoint, card, created_at`

func scanAddress(row pgx.Row) (*Address, error) {
	var a Address
	var card []byte
	if err := row.Scan(&a.Handle, &a.Kind, &a.OwnerID, &a.WorkspaceID, &a.Access, &a.DisplayName, &a.Description, &a.Endpoint, &card, &a.CreatedAt); err != nil {
		return nil, err
	}
	if len(card) > 0 {
		a.Card = new(a2a.AgentCard)
		if err := json.Unmarshal(card, a.Card); err != nil {
			return nil, fmt.Errorf("directory: card of %s: %w", a.Handle, err)
		}
	}
	return &a, nil
}

// Address returns the address with handle.
func (d *Directory) Address(ctx context.Context, handle string) (*Address, error) {
	a, err := scanAddress(d.db.Pool.QueryRow(ctx, `SELECT `+addressColumns+` FROM addresses WHERE handle = $1`, handle))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// Addresses lists all addresses, people first, then agents, by handle.
func (d *Directory) Addresses(ctx context.Context) ([]*Address, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT `+addressColumns+` FROM addresses ORDER BY kind DESC, handle`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Address, error) { return scanAddress(row) })
}

// SetCard stores the card an agent serves. The display name and description follow the
// card unless they were set by hand.
func (d *Directory) SetCard(ctx context.Context, handle string, card *a2a.AgentCard) error {
	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	_, err = d.db.Pool.Exec(ctx, `UPDATE addresses SET card = $2,
			display_name = CASE WHEN display_name = '' THEN $3 ELSE display_name END,
			description = CASE WHEN description = '' THEN $4 ELSE description END,
			updated_at = now()
		WHERE handle = $1`, handle, raw, card.Name, card.Description)
	return err
}

// DeleteAddress removes an agent address and its tokens.
func (d *Directory) DeleteAddress(ctx context.Context, handle string) error {
	tag, err := d.db.Pool.Exec(ctx, `DELETE FROM addresses WHERE handle = $1 AND kind = 'agent'`, handle)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Token is a minted bearer token's public part.
type Token struct {
	Handle    string     `json:"handle"`
	Label     string     `json:"label"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

// MintToken creates a bearer token for an address and returns it. Only its hash is
// stored, so this is the only time the token is visible.
func (d *Directory) MintToken(ctx context.Context, handle, label string) (string, error) {
	token := newSecret("lgr_")
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO tokens (token_hash, handle, label) VALUES ($1, $2, $3)`, hashToken(token), handle, label+"|"+token[:10])
	if err != nil {
		return "", err
	}
	return token, nil
}

// Tokens lists an address's tokens without their secrets.
func (d *Directory) Tokens(ctx context.Context, handle string) ([]*Token, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT handle, label, created_at, last_used_at FROM tokens WHERE handle = $1 ORDER BY created_at`, handle)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Token, error) {
		var t Token
		var label string
		if err := row.Scan(&t.Handle, &label, &t.CreatedAt, &t.LastUsed); err != nil {
			return nil, err
		}
		t.Label, t.Prefix = splitLabel(label)
		return &t, nil
	})
}

func splitLabel(s string) (label, prefix string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '|' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// TokenHandle returns the address a bearer token belongs to.
func (d *Directory) TokenHandle(ctx context.Context, token string) (string, error) {
	var handle string
	err := d.db.Pool.QueryRow(ctx, `UPDATE tokens SET last_used_at = now() WHERE token_hash = $1 RETURNING handle`, hashToken(token)).Scan(&handle)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return handle, err
}

// NotificationSettings is how a person wants to hear that a task waits on them.
type NotificationSettings struct {
	Email bool `json:"email"`
	// SlackWebhook is a Slack incoming webhook URL; empty means no Slack.
	SlackWebhook string `json:"slackWebhook"`
}

// NotificationSettings returns a person's settings, or the defaults (email on).
func (d *Directory) NotificationSettings(ctx context.Context, userID int64) (*NotificationSettings, error) {
	s := &NotificationSettings{Email: true}
	err := d.db.Pool.QueryRow(ctx, `SELECT email, slack_webhook FROM notification_settings WHERE user_id = $1`, userID).Scan(&s.Email, &s.SlackWebhook)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}

// SetNotificationSettings stores a person's settings.
func (d *Directory) SetNotificationSettings(ctx context.Context, userID int64, s *NotificationSettings) error {
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO notification_settings (user_id, email, slack_webhook) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET email = $2, slack_webhook = $3, updated_at = now()`, userID, s.Email, s.SlackWebhook)
	return err
}

// DecisionLink lets whoever holds it answer one task as Handle, once.
type DecisionLink struct {
	TaskID    string
	Handle    string
	ExpiresAt time.Time
}

// MintDecisionLink creates a link token for a task waiting on handle. Only its hash is
// stored.
func (d *Directory) MintDecisionLink(ctx context.Context, taskID, handle string, ttl time.Duration) (string, error) {
	token := newSecret("lgd_")
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO decision_links (token_hash, task_id, handle, expires_at) VALUES ($1, $2, $3, $4)`,
		hashToken(token), taskID, handle, time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	return token, nil
}

// DecisionLink returns an unused, unexpired link.
func (d *Directory) DecisionLink(ctx context.Context, token string) (*DecisionLink, error) {
	return d.decisionLink(ctx, `SELECT task_id, handle, expires_at FROM decision_links
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, token)
}

// ClaimDecisionLink marks a link used and returns it, unless it was used or expired.
// Of two concurrent claims, only one succeeds.
func (d *Directory) ClaimDecisionLink(ctx context.Context, token string) (*DecisionLink, error) {
	return d.decisionLink(ctx, `UPDATE decision_links SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING task_id, handle, expires_at`, token)
}

// ReleaseDecisionLink makes a claimed link usable again, after the answer failed.
func (d *Directory) ReleaseDecisionLink(ctx context.Context, token string) error {
	_, err := d.db.Pool.Exec(ctx, `UPDATE decision_links SET used_at = NULL WHERE token_hash = $1`, hashToken(token))
	return err
}

func (d *Directory) decisionLink(ctx context.Context, query, token string) (*DecisionLink, error) {
	var l DecisionLink
	err := d.db.Pool.QueryRow(ctx, query, hashToken(token)).Scan(&l.TaskID, &l.Handle, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)
	return h
})

func newSecret(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
