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

// CreateUser creates a person and their human address.
func (d *Directory) CreateUser(ctx context.Context, email, name, handle, password string, admin bool) (*User, error) {
	if !HandlePattern.MatchString(handle) {
		return nil, ErrInvalidHandle
	}
	if len(password) < 8 {
		return nil, errors.New("directory: the password needs at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &User{Email: email, Name: name, Handle: handle, Admin: admin}
	err = pgx.BeginFunc(ctx, d.db.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, name, handle, password_hash, admin) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			email, name, handle, string(hash), admin).Scan(&u.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO addresses (handle, kind, owner_id, display_name) VALUES ($1, 'human', $2, $3)`, handle, u.ID, name)
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

// EnsureAdmin creates the admin user unless a user with that email exists.
func (d *Directory) EnsureAdmin(ctx context.Context, email, name, handle, password string) (*User, error) {
	if u, err := d.userWhere(ctx, "email = $1", email); err == nil {
		return u, nil
	}
	return d.CreateUser(ctx, email, name, handle, password, true)
}

// Login checks the password and starts a session. It returns the session token.
func (d *Directory) Login(ctx context.Context, email, password string) (*User, string, error) {
	var hash string
	var id int64
	err := d.db.Pool.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE email = $1`, email).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the same time as a real check so the response doesn't reveal the email.
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return nil, "", ErrBadCredentials
	}
	if err != nil {
		return nil, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, "", ErrBadCredentials
	}
	token := newSecret("lgs_")
	if _, err := d.db.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), id, time.Now().Add(SessionTTL)); err != nil {
		return nil, "", err
	}
	u, err := d.userWhere(ctx, "id = $1", id)
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

// Users lists everyone.
func (d *Directory) Users(ctx context.Context) ([]*User, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT id, email, name, handle, admin FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*User, error) {
		var u User
		return &u, row.Scan(&u.ID, &u.Email, &u.Name, &u.Handle, &u.Admin)
	})
}

func (d *Directory) userWhere(ctx context.Context, cond string, arg any) (*User, error) {
	var u User
	err := d.db.Pool.QueryRow(ctx, `SELECT id, email, name, handle, admin FROM users WHERE `+cond, arg).
		Scan(&u.ID, &u.Email, &u.Name, &u.Handle, &u.Admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// CreateAgent creates an agent address owned by ownerID.
func (d *Directory) CreateAgent(ctx context.Context, a *Address) error {
	if !HandlePattern.MatchString(a.Handle) {
		return ErrInvalidHandle
	}
	var owner *int64
	if a.OwnerID != 0 {
		owner = &a.OwnerID
	}
	err := d.db.Pool.QueryRow(ctx, `INSERT INTO addresses (handle, kind, owner_id, display_name, description, endpoint)
		VALUES ($1, 'agent', $2, $3, $4, $5) RETURNING created_at`,
		a.Handle, owner, a.DisplayName, a.Description, a.Endpoint).Scan(&a.CreatedAt)
	if pg.IsUniqueViolation(err) {
		return ErrTaken
	}
	a.Kind = KindAgent
	return err
}

const addressColumns = `handle, kind, coalesce(owner_id, 0), display_name, description, endpoint, card, created_at`

func scanAddress(row pgx.Row) (*Address, error) {
	var a Address
	var card []byte
	if err := row.Scan(&a.Handle, &a.Kind, &a.OwnerID, &a.DisplayName, &a.Description, &a.Endpoint, &card, &a.CreatedAt); err != nil {
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
