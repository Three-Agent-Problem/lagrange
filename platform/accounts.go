package platform

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/three-agent-problem/lagrange/directory"
)

// Signup modes (Config.Signup).
const (
	// SignupInvite lets only invited people join (the default).
	SignupInvite = "invite"
	// SignupOpen lets anyone create an account.
	SignupOpen = "open"
)

// How long each kind of emailed link works.
const (
	verifyTTL = 24 * time.Hour
	loginTTL  = 15 * time.Minute
	resetTTL  = time.Hour
	inviteTTL = 7 * 24 * time.Hour
)

func (p *Platform) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/methods", p.handleAuthMethods)
	mux.Handle("POST /api/signup", p.public(p.handleSignup))
	mux.Handle("POST /api/auth/verify", p.public(p.handleVerify))
	mux.Handle("POST /api/auth/verify/resend", p.authedAny(p.handleResendVerify))
	mux.Handle("POST /api/login/link", p.public(p.handleLoginLink))
	mux.Handle("POST /api/login/link/claim", p.public(p.handleLoginLinkClaim))
	mux.Handle("POST /api/password/reset", p.public(p.handleResetRequest))
	mux.Handle("POST /api/password/reset/confirm", p.public(p.handleResetConfirm))
	mux.Handle("PUT /api/me/password", p.authed(p.handleChangePassword))
	mux.Handle("POST /api/invites", p.in(directory.RoleAdmin, p.handleCreateInvite))
	mux.HandleFunc("GET /api/invites/{token}", p.handleInvite)
	mux.Handle("POST /api/invites/{token}/accept", p.public(p.handleAcceptInvite))
	mux.HandleFunc("GET /api/auth/{provider}/start", p.handleOAuthStart)
	mux.HandleFunc("GET /api/auth/{provider}/callback", p.handleOAuthCallback)
}

// public guards an endpoint anyone may call: JSON only (against CSRF, as authed does)
// and a per-client rate limit.
func (p *Platform) public(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			apiError(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		if !p.limits.allow("ip:" + p.clientIP(r)) {
			apiError(w, http.StatusTooManyRequests, "too many attempts; wait a minute and try again")
			return
		}
		h(w, r)
	})
}

// clientIP is the caller's address. Behind a reverse proxy (Config.TrustProxy) it is the
// last X-Forwarded-For entry, the one the proxy added.
func (p *Platform) clientIP(r *http.Request) string {
	if p.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (p *Platform) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: p.secure(), MaxAge: int(directory.SessionTTL / time.Second),
	})
}

func (p *Platform) secure() bool { return strings.HasPrefix(p.cfg.PublicURL, "https://") }

// login starts a session for u and answers with the user.
func (p *Platform) login(w http.ResponseWriter, r *http.Request, userID int64) {
	u, token, err := p.Directory.StartSession(r.Context(), userID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.setSession(w, token)
	writeJSON(w, http.StatusOK, u)
}

func (p *Platform) signupMode() string {
	if p.cfg.Signup == SignupOpen {
		return SignupOpen
	}
	return SignupInvite
}

// AuthMethods is how people can sign in on this server.
type AuthMethods struct {
	// Signup is "open" (anyone) or "invite" (invited people only).
	Signup string `json:"signup"`
	// Email: the server sends email, so login links, resets and verification work.
	Email     bool           `json:"email"`
	Providers []ProviderView `json:"providers"`
}

// ProviderView is an OAuth provider as the login page shows it.
type ProviderView struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

func (p *Platform) handleAuthMethods(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, p.authMethods())
}

func (p *Platform) authMethods() AuthMethods {
	m := AuthMethods{Signup: p.signupMode(), Email: p.cfg.Email != nil, Providers: []ProviderView{}}
	for _, o := range p.cfg.OAuth {
		m.Providers = append(m.Providers, ProviderView{Name: o.Name, Label: o.Label})
	}
	return m
}

type signupBody struct {
	Email, Name, Handle, Password string
}

func (p *Platform) handleSignup(w http.ResponseWriter, r *http.Request) {
	if p.signupMode() != SignupOpen {
		apiError(w, http.StatusForbidden, "this server is invite-only; ask someone on it for an invitation")
		return
	}
	var body signupBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {email, name, handle, password}")
		return
	}
	// Without email there is no way to verify, so the address is taken on trust.
	u, err := p.Directory.Register(r.Context(), directory.Registration{
		Email: body.Email, Name: body.Name, Handle: body.Handle, Password: body.Password, Verified: p.cfg.Email == nil,
	})
	if err != nil {
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	p.addressChanged(r.Context(), u.Handle)
	if !u.Verified {
		p.sendVerify(r.Context(), u)
	}
	p.login(w, r, u.ID)
}

// userMessage is an error as a person should read it.
func userMessage(err error) string {
	if errors.Is(err, directory.ErrTaken) {
		return "that email or handle is already taken"
	}
	return strings.TrimPrefix(err.Error(), "directory: ")
}

func (p *Platform) sendVerify(ctx context.Context, u *directory.User) {
	p.mailLink(ctx, directory.AuthLink{Purpose: directory.LinkVerify, Email: u.Email, UserID: u.ID}, verifyTTL,
		"Confirm your email for Lagrange", func(link string) string {
			return fmt.Sprintf("Hi %s,\n\nConfirm your email to start using Lagrange as @%s:\n%s\n\nThe link works once and expires in 24 hours. If you didn't sign up, ignore this email.\n", u.Name, u.Handle, link)
		})
}

// mailLink mints a link and emails it in the background, at most a few times an hour per
// address. It returns the link (empty when it wasn't minted).
func (p *Platform) mailLink(ctx context.Context, l directory.AuthLink, ttl time.Duration, subject string, body func(link string) string) string {
	if !p.limits.allowMail(l.Email) {
		p.log.Warn("platform: too many emails to one address; skipped", "purpose", l.Purpose)
		return ""
	}
	token, err := p.Directory.MintAuthLink(ctx, l, ttl)
	if err != nil {
		p.log.Error("platform: minting a link", "purpose", l.Purpose, "error", err)
		return ""
	}
	link := p.cfg.PublicURL + "/auth/" + l.Purpose + "/" + token
	if p.cfg.Email == nil {
		return link
	}
	text := body(link)
	p.mail.Add(1)
	go func() {
		defer p.mail.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.cfg.Email.Send(ctx, l.Email, subject, text); err != nil {
			p.log.Error("platform: sending email", "purpose", l.Purpose, "error", err)
		}
	}()
	return link
}

type tokenBody struct{ Token string }

func (p *Platform) handleVerify(w http.ResponseWriter, r *http.Request) {
	var body tokenBody
	if err := decode(r, &body); err != nil || body.Token == "" {
		apiError(w, http.StatusBadRequest, "send {token}")
		return
	}
	l, err := p.Directory.ClaimAuthLink(r.Context(), body.Token, directory.LinkVerify)
	if err != nil {
		apiError(w, http.StatusGone, "this link doesn't work any more; log in and send a new one")
		return
	}
	if err := p.Directory.MarkVerified(r.Context(), l.UserID); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if me := p.sessionUser(r); me != nil && me.ID == l.UserID {
		writeJSON(w, http.StatusOK, me)
		return
	}
	p.login(w, r, l.UserID)
}

func (p *Platform) handleResendVerify(w http.ResponseWriter, r *http.Request, me *directory.User) {
	if me.Verified {
		apiError(w, http.StatusConflict, "your email is already confirmed")
		return
	}
	if p.cfg.Email == nil {
		apiError(w, http.StatusServiceUnavailable, "this server can't send email")
		return
	}
	p.sendVerify(r.Context(), me)
	w.WriteHeader(http.StatusAccepted)
}

type emailBody struct{ Email string }

// handleLoginLink emails a login link. It answers the same whether or not the email has
// an account, so it can't be used to find out who is on the server.
func (p *Platform) handleLoginLink(w http.ResponseWriter, r *http.Request) {
	var body emailBody
	if err := decode(r, &body); err != nil || !strings.Contains(body.Email, "@") {
		apiError(w, http.StatusBadRequest, "send {email}")
		return
	}
	if p.cfg.Email == nil {
		apiError(w, http.StatusServiceUnavailable, "this server can't send email; log in with your password")
		return
	}
	if u, err := p.Directory.UserByEmail(r.Context(), body.Email); err == nil {
		p.mailLink(r.Context(), directory.AuthLink{Purpose: directory.LinkLogin, Email: u.Email, UserID: u.ID}, loginTTL,
			"Your Lagrange login link", func(link string) string {
				return fmt.Sprintf("Hi %s,\n\nLog in to Lagrange as @%s:\n%s\n\nThe link works once and expires in 15 minutes. If you didn't ask for it, ignore this email.\n", u.Name, u.Handle, link)
			})
	}
	w.WriteHeader(http.StatusAccepted)
}

func (p *Platform) handleLoginLinkClaim(w http.ResponseWriter, r *http.Request) {
	var body tokenBody
	if err := decode(r, &body); err != nil || body.Token == "" {
		apiError(w, http.StatusBadRequest, "send {token}")
		return
	}
	l, err := p.Directory.ClaimAuthLink(r.Context(), body.Token, directory.LinkLogin)
	if err != nil {
		apiError(w, http.StatusGone, "this login link doesn't work any more; ask for a new one")
		return
	}
	// Opening the link proves the email.
	_ = p.Directory.MarkVerified(r.Context(), l.UserID)
	p.login(w, r, l.UserID)
}

func (p *Platform) handleResetRequest(w http.ResponseWriter, r *http.Request) {
	var body emailBody
	if err := decode(r, &body); err != nil || !strings.Contains(body.Email, "@") {
		apiError(w, http.StatusBadRequest, "send {email}")
		return
	}
	if p.cfg.Email == nil {
		apiError(w, http.StatusServiceUnavailable, "this server can't send email; ask an admin to reset your password")
		return
	}
	if u, err := p.Directory.UserByEmail(r.Context(), body.Email); err == nil {
		p.mailLink(r.Context(), directory.AuthLink{Purpose: directory.LinkReset, Email: u.Email, UserID: u.ID}, resetTTL,
			"Reset your Lagrange password", func(link string) string {
				return fmt.Sprintf("Hi %s,\n\nChoose a new password for @%s:\n%s\n\nThe link works once and expires in an hour. If you didn't ask for it, ignore this email; your password stays the same.\n", u.Name, u.Handle, link)
			})
	}
	w.WriteHeader(http.StatusAccepted)
}

func (p *Platform) handleResetConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token, Password string }
	if err := decode(r, &body); err != nil || body.Token == "" {
		apiError(w, http.StatusBadRequest, "send {token, password}")
		return
	}
	if len(body.Password) < 8 {
		apiError(w, http.StatusBadRequest, userMessage(directory.ErrShortPassword))
		return
	}
	l, err := p.Directory.ClaimAuthLink(r.Context(), body.Token, directory.LinkReset)
	if err != nil {
		apiError(w, http.StatusGone, "this reset link doesn't work any more; ask for a new one")
		return
	}
	if err := p.Directory.SetPassword(r.Context(), l.UserID, body.Password); err != nil {
		_ = p.Directory.ReleaseAuthLink(r.Context(), body.Token)
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	// Whoever knew the old password is logged out.
	_ = p.Directory.EndSessions(r.Context(), l.UserID, "")
	_ = p.Directory.MarkVerified(r.Context(), l.UserID)
	p.login(w, r, l.UserID)
}

func (p *Platform) handleChangePassword(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body struct{ Current, Password string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {current, password}")
		return
	}
	err := p.Directory.ChangePassword(r.Context(), me.ID, body.Current, body.Password)
	if errors.Is(err, directory.ErrBadCredentials) {
		apiError(w, http.StatusForbidden, "your current password is wrong")
		return
	}
	if err != nil {
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	keep := ""
	if c, err := r.Cookie(SessionCookie); err == nil {
		keep = c.Value
	}
	_ = p.Directory.EndSessions(r.Context(), me.ID, keep)
	w.WriteHeader(http.StatusNoContent)
}

// InviteView is an invitation as its link shows it.
type InviteView struct {
	Email     string `json:"email"`
	InvitedBy string `json:"invitedBy"`
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	// Existing means the email already has an account: they log in to join.
	Existing bool `json:"existing"`
	// Link is returned to whoever created the invitation, to share it themselves.
	Link string `json:"link,omitempty"`
	// Emailed says the server sent the link by email.
	Emailed bool `json:"emailed"`
}

// handleCreateInvite invites someone into the workspace with a role (member unless
// said). People who already have an account join by logging in and opening the link.
func (p *Platform) handleCreateInvite(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body struct{ Email, Role string }
	if err := decode(r, &body); err != nil || !strings.Contains(body.Email, "@") {
		apiError(w, http.StatusBadRequest, "send {email, role}")
		return
	}
	role, ok := p.grantable(w, s, body.Role)
	if !ok {
		return
	}
	email := directory.NormalizeEmail(body.Email)
	existing := false
	if u, err := p.Directory.UserByEmail(r.Context(), email); err == nil {
		if _, err := p.Directory.Membership(r.Context(), u.ID, s.Slug); err == nil {
			apiError(w, http.StatusConflict, "@"+u.Handle+" is already in "+s.Name)
			return
		}
		existing = true
	}
	link := p.mailLink(r.Context(), directory.AuthLink{Purpose: directory.LinkInvite, Email: email, InvitedBy: me.ID, WorkspaceID: s.ID, Role: role}, inviteTTL,
		me.Name+" invited you to "+s.Name+" on Lagrange", func(link string) string {
			next := "Pick your handle and join"
			if existing {
				next = "Log in as " + email + " and join"
			}
			return fmt.Sprintf("%s (@%s) invited you to %s on Lagrange, where agents and people work together.\n\n%s:\n%s\n\nThe link works once and expires in 7 days.\n", me.Name, me.Handle, s.Name, next, link)
		})
	if link == "" {
		apiError(w, http.StatusTooManyRequests, "too many invitations to that address; try again later")
		return
	}
	writeJSON(w, http.StatusCreated, InviteView{Email: email, InvitedBy: me.Name, Workspace: s.Name, Role: role, Existing: existing, Link: link, Emailed: p.cfg.Email != nil})
}

func (p *Platform) handleInvite(w http.ResponseWriter, r *http.Request) {
	l, err := p.Directory.AuthLink(r.Context(), r.PathValue("token"), directory.LinkInvite)
	if err != nil {
		apiError(w, http.StatusGone, "this invitation doesn't work any more; ask for a new one")
		return
	}
	v := InviteView{Email: l.Email}
	if by, err := p.Directory.UserByID(r.Context(), l.InvitedBy); err == nil {
		v.InvitedBy = by.Name
	}
	if ws, role, err := p.inviteTarget(r.Context(), l); err == nil {
		v.Workspace, v.Role = ws.Name, role
	}
	_, err = p.Directory.UserByEmail(r.Context(), l.Email)
	v.Existing = err == nil
	writeJSON(w, http.StatusOK, v)
}

// inviteTarget is the workspace and role an invitation joins. Invitations from before
// workspaces join the default one.
func (p *Platform) inviteTarget(ctx context.Context, l *directory.AuthLink) (*directory.Workspace, string, error) {
	if l.WorkspaceID == 0 {
		ws, err := p.Directory.Default(ctx)
		return ws, directory.RoleMember, err
	}
	ws, err := p.Directory.WorkspaceByID(ctx, l.WorkspaceID)
	return ws, firstNonEmpty(l.Role, directory.RoleMember), err
}

// join adds someone who already has an account to the workspace of an invitation. The
// link went to their email, so it proves the address too.
func (p *Platform) join(ctx context.Context, l *directory.AuthLink, u *directory.User) error {
	ws, role, err := p.inviteTarget(ctx, l)
	if err != nil {
		return err
	}
	if err := p.Directory.AddMember(ctx, ws.ID, u.ID, role); err != nil && !errors.Is(err, directory.ErrTaken) {
		return err
	}
	_ = p.Directory.MarkVerified(ctx, u.ID)
	p.addressChanged(ctx, u.Handle)
	return nil
}

func (p *Platform) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var body signupBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {name, handle, password}")
		return
	}
	ctx, token := r.Context(), r.PathValue("token")
	l, err := p.Directory.ClaimAuthLink(ctx, token, directory.LinkInvite)
	if err != nil {
		apiError(w, http.StatusGone, "this invitation doesn't work any more; ask for a new one")
		return
	}
	release := func() { _ = p.Directory.ReleaseAuthLink(context.WithoutCancel(ctx), token) }
	if existing, err := p.Directory.UserByEmail(ctx, l.Email); err == nil {
		// An account already: only its owner, logged in, may join with it.
		if me := p.sessionUser(r); me == nil || me.ID != existing.ID {
			release()
			apiError(w, http.StatusConflict, "log in as "+l.Email+" first, then open the invitation again")
			return
		}
		if err := p.join(ctx, l, existing); err != nil {
			release()
			apiError(w, statusOf(err), userMessage(err))
			return
		}
		writeJSON(w, http.StatusOK, existing)
		return
	}
	ws, role, err := p.inviteTarget(ctx, l)
	if err != nil {
		release()
		apiError(w, http.StatusGone, "the workspace of this invitation is gone")
		return
	}
	// The link went to this email, so opening it proves the address.
	u, err := p.Directory.Register(ctx, directory.Registration{
		Email: l.Email, Name: body.Name, Handle: body.Handle, Password: body.Password, Verified: true, Workspace: ws.ID, Role: role,
	})
	if err != nil {
		release()
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	p.addressChanged(ctx, u.Handle)
	p.login(w, r, u.ID)
}

// limits throttles the public auth endpoints per client, and emails per address.
type limits struct {
	perMinute int
	mu        sync.Mutex
	hits      map[string]*window
}

type window struct {
	start time.Time
	n     int
}

const mailsPerHour = 5

func (l *limits) allow(key string) bool { return l.take(key, l.perMinute, time.Minute) }

func (l *limits) allowMail(email string) bool {
	return l.take("mail:"+directory.NormalizeEmail(email), mailsPerHour, time.Hour)
}

// take counts one hit for key in a fixed window and reports whether it is within n.
// A negative perMinute turns limits off.
func (l *limits) take(key string, n int, every time.Duration) bool {
	if l.perMinute < 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string]*window{}
	}
	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > time.Hour {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= every {
		w = &window{start: now}
		l.hits[key] = w
	}
	w.n++
	return w.n <= n
}
