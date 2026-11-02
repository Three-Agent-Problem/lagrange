package directory

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIKeyPrefix starts every personal API key, so it can't be mistaken for an address
// token (lgr_) or a session (lgs_).
const APIKeyPrefix = "lgk_"

// CLILoginTTL is how long a `lagrange login` code can be traded for a key.
const CLILoginTTL = 5 * time.Minute

var (
	// ErrRoleTooHigh means a key would get more power than its person has.
	ErrRoleTooHigh = errors.New("directory: a key can't have a higher role than yours")
	// ErrBadCode means a login code is unknown, used, expired, or its verifier is wrong.
	ErrBadCode = errors.New("directory: that login code is wrong or expired")
)

// APIKey is a personal API key, without its secret.
type APIKey struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Role      string     `json:"role"`
	Workspace string     `json:"workspace"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

// KeyGrant is what a key may do: act as its person in Workspace with at most Role.
type KeyGrant struct {
	WorkspaceID int64
	Workspace   string
	Role        string
}

// CreateAPIKey creates a key for userID in workspace wsID with at most role, and returns
// its secret, which is visible only now. The role can't exceed the person's own.
func (d *Directory) CreateAPIKey(ctx context.Context, userID, wsID int64, name, role string) (string, *APIKey, error) {
	if !ValidRole(role) {
		return "", nil, errors.New("directory: unknown role " + role)
	}
	if name = strings.TrimSpace(name); name == "" {
		return "", nil, errors.New("directory: name the key")
	}
	var member string
	err := d.db.Pool.QueryRow(ctx, `SELECT role FROM members WHERE user_id = $1 AND workspace_id = $2`, userID, wsID).Scan(&member)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotMember
	}
	if err != nil {
		return "", nil, err
	}
	if !RoleAtLeast(member, role) {
		return "", nil, ErrRoleTooHigh
	}
	secret := newSecret(APIKeyPrefix)
	k := &APIKey{Name: name, Prefix: secret[:12], Role: role}
	err = d.db.Pool.QueryRow(ctx, `INSERT INTO api_keys (user_id, workspace_id, name, prefix, key_hash, role) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, (SELECT slug FROM workspaces WHERE id = $2)`, userID, wsID, name, k.Prefix, hashToken(secret), role).Scan(&k.ID, &k.CreatedAt, &k.Workspace)
	if err != nil {
		return "", nil, err
	}
	return secret, k, nil
}

// APIKeys lists a person's live keys, newest first.
func (d *Directory) APIKeys(ctx context.Context, userID int64) ([]*APIKey, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT k.id, k.name, k.prefix, k.role, w.slug, k.created_at, k.last_used_at
		FROM api_keys k JOIN workspaces w ON w.id = k.workspace_id
		WHERE k.user_id = $1 AND k.revoked_at IS NULL ORDER BY k.created_at DESC, k.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*APIKey, error) {
		var k APIKey
		return &k, row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Role, &k.Workspace, &k.CreatedAt, &k.LastUsed)
	})
}

// RevokeAPIKey revokes one of a person's keys.
func (d *Directory) RevokeAPIKey(ctx context.Context, userID, id int64) error {
	tag, err := d.db.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// APIKeyUser returns the person a live key acts as and what it may do.
func (d *Directory) APIKeyUser(ctx context.Context, secret string) (*User, *KeyGrant, error) {
	var userID int64
	g := &KeyGrant{}
	err := d.db.Pool.QueryRow(ctx, `UPDATE api_keys k SET last_used_at = now() FROM workspaces w
		WHERE k.key_hash = $1 AND k.revoked_at IS NULL AND w.id = k.workspace_id
		RETURNING k.user_id, k.workspace_id, w.slug, k.role`, hashToken(secret)).Scan(&userID, &g.WorkspaceID, &g.Workspace, &g.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	u, err := d.userWhere(ctx, "id = $1", userID)
	if err != nil {
		return nil, nil, err
	}
	return u, g, nil
}

// StartCLILogin records that userID approved a CLI login for workspace wsID with at most
// role, and returns the one-time code the CLI trades for a key. challenge is the CLI's
// PKCE S256 challenge, so only the CLI that started the login can claim the code.
func (d *Directory) StartCLILogin(ctx context.Context, userID, wsID int64, name, role, challenge string) (string, error) {
	if challenge == "" {
		return "", errors.New("directory: missing challenge")
	}
	if !ValidRole(role) {
		return "", errors.New("directory: unknown role " + role)
	}
	code := newSecret("lgc_")
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO cli_logins (code_hash, challenge, user_id, workspace_id, role, name, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		hashToken(code), challenge, userID, wsID, role, name, time.Now().Add(CLILoginTTL))
	if err != nil {
		return "", err
	}
	return code, nil
}

// ClaimCLILogin trades a login code and its PKCE verifier for a new key, once.
func (d *Directory) ClaimCLILogin(ctx context.Context, code, verifier string) (string, *APIKey, error) {
	var userID, wsID int64
	var challenge, role, name string
	var expires time.Time
	err := d.db.Pool.QueryRow(ctx, `DELETE FROM cli_logins WHERE code_hash = $1 RETURNING challenge, user_id, workspace_id, role, name, expires_at`,
		hashToken(code)).Scan(&challenge, &userID, &wsID, &role, &name, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrBadCode
	}
	if err != nil {
		return "", nil, err
	}
	if time.Now().After(expires) || subtle.ConstantTimeCompare([]byte(PKCEChallenge(verifier)), []byte(challenge)) != 1 {
		return "", nil, ErrBadCode
	}
	return d.CreateAPIKey(ctx, userID, wsID, name, role)
}

// PKCEChallenge is the S256 challenge of a verifier (RFC 7636).
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
