package directory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Purposes of an AuthLink.
const (
	LinkVerify = "verify"
	LinkLogin  = "login"
	LinkReset  = "reset"
	LinkInvite = "invite"
)

// AuthLink is a one-time link sent by email.
type AuthLink struct {
	Purpose string
	Email   string
	// UserID is the person it is for; zero for an invitation (they don't exist yet).
	UserID int64
	// InvitedBy is who sent an invitation; zero otherwise.
	InvitedBy int64
	ExpiresAt time.Time
}

// MintAuthLink creates a link token. Only its hash is stored.
func (d *Directory) MintAuthLink(ctx context.Context, l AuthLink, ttl time.Duration) (string, error) {
	token := newSecret("lga_")
	_, err := d.db.Pool.Exec(ctx, `INSERT INTO auth_links (token_hash, purpose, email, user_id, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		hashToken(token), l.Purpose, NormalizeEmail(l.Email), nullID(l.UserID), nullID(l.InvitedBy), time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	return token, nil
}

// AuthLink returns an unused, unexpired link for purpose.
func (d *Directory) AuthLink(ctx context.Context, token, purpose string) (*AuthLink, error) {
	return d.authLink(ctx, `SELECT purpose, email, coalesce(user_id, 0), coalesce(invited_by, 0), expires_at FROM auth_links
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()`, token, purpose)
}

// ClaimAuthLink marks a link used and returns it, unless it was used, expired or is for
// another purpose. Of two concurrent claims, only one succeeds.
func (d *Directory) ClaimAuthLink(ctx context.Context, token, purpose string) (*AuthLink, error) {
	return d.authLink(ctx, `UPDATE auth_links SET used_at = now()
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING purpose, email, coalesce(user_id, 0), coalesce(invited_by, 0), expires_at`, token, purpose)
}

// ReleaseAuthLink makes a claimed link usable again, after what it was for failed.
func (d *Directory) ReleaseAuthLink(ctx context.Context, token string) error {
	_, err := d.db.Pool.Exec(ctx, `UPDATE auth_links SET used_at = NULL WHERE token_hash = $1`, hashToken(token))
	return err
}

func (d *Directory) authLink(ctx context.Context, query, token, purpose string) (*AuthLink, error) {
	var l AuthLink
	err := d.db.Pool.QueryRow(ctx, query, hashToken(token), purpose).Scan(&l.Purpose, &l.Email, &l.UserID, &l.InvitedBy, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func nullID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// UserByEmail returns the person with that email.
func (d *Directory) UserByEmail(ctx context.Context, email string) (*User, error) {
	return d.userWhere(ctx, "email = $1", NormalizeEmail(email))
}

// UserByID returns the person with that ID.
func (d *Directory) UserByID(ctx context.Context, id int64) (*User, error) {
	return d.userWhere(ctx, "id = $1", id)
}

// MarkVerified records that the person proved they own their email.
func (d *Directory) MarkVerified(ctx context.Context, userID int64) error {
	_, err := d.db.Pool.Exec(ctx, `UPDATE users SET email_verified_at = coalesce(email_verified_at, now()) WHERE id = $1`, userID)
	return err
}

// SetPassword replaces a person's password.
func (d *Directory) SetPassword(ctx context.Context, userID int64, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tag, err := d.db.Pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ChangePassword sets a new password after checking the current one. Someone without a
// password (links or a provider only) sets their first one without it.
func (d *Directory) ChangePassword(ctx context.Context, userID int64, current, password string) error {
	var hash *string
	if err := d.db.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash); err != nil {
		return err
	}
	if hash != nil && bcrypt.CompareHashAndPassword([]byte(*hash), []byte(current)) != nil {
		return ErrBadCredentials
	}
	return d.SetPassword(ctx, userID, password)
}

// EndSessions logs a person out everywhere except the session with keepToken (may be empty).
func (d *Directory) EndSessions(ctx context.Context, userID int64, keepToken string) error {
	_, err := d.db.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, hashToken(keepToken))
	return err
}

// Identity is an OAuth account that logs a person in.
type Identity struct {
	Provider  string    `json:"provider"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

// UserByIdentity returns the person an OAuth account belongs to.
func (d *Directory) UserByIdentity(ctx context.Context, provider, subject string) (*User, error) {
	var id int64
	err := d.db.Pool.QueryRow(ctx, `SELECT user_id FROM identities WHERE provider = $1 AND subject = $2`, provider, subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return d.UserByID(ctx, id)
}

// LinkIdentity lets an OAuth account log the person in. An account already linked to
// someone else is ErrTaken.
func (d *Directory) LinkIdentity(ctx context.Context, userID int64, provider, subject, email string) error {
	tag, err := d.db.Pool.Exec(ctx, `INSERT INTO identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, subject) DO UPDATE SET email = excluded.email WHERE identities.user_id = excluded.user_id`,
		provider, subject, userID, NormalizeEmail(email))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrTaken
	}
	return err
}

// Identities lists a person's OAuth accounts.
func (d *Directory) Identities(ctx context.Context, userID int64) ([]*Identity, error) {
	rows, err := d.db.Pool.Query(ctx, `SELECT provider, email, created_at FROM identities WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Identity, error) {
		var i Identity
		return &i, row.Scan(&i.Provider, &i.Email, &i.CreatedAt)
	})
}

// FreeHandle turns a hint (a provider login, a name, an email) into a valid handle that
// nobody has yet, adding -2, -3, … when needed.
func (d *Directory) FreeHandle(ctx context.Context, hint string) (string, error) {
	base := handleFrom(hint)
	for i := 1; i <= 50; i++ {
		h := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			h = strings.TrimRight(base[:min(len(base), 40-len(suffix))], "-") + suffix
		}
		var taken bool
		if err := d.db.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM addresses WHERE handle = $1)`, h).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return h, nil
		}
	}
	return "", ErrTaken
}

func handleFrom(hint string) string {
	hint, _, _ = strings.Cut(strings.ToLower(hint), "@")
	var b strings.Builder
	dash := false
	for _, r := range hint {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	h := strings.Trim(b.String(), "-")
	if len(h) > 40 {
		h = strings.TrimRight(h[:40], "-")
	}
	if len(h) < 2 {
		h = "person"
	}
	return h
}
