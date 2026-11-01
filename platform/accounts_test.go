package platform_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/three-agent-problem/lagrange/platform"
)

var authLinkPattern = regexp.MustCompile(`/auth/(verify|login|reset|invite)/(lga_[0-9a-f]+)`)

// authLink returns the token of the purpose's link in an email body.
func authLink(t *testing.T, body, purpose string) string {
	t.Helper()
	m := authLinkPattern.FindStringSubmatch(body)
	if m == nil || m[1] != purpose {
		t.Fatalf("no %s link in %q", purpose, body)
	}
	return m[2]
}

// guest is a browser without a session yet.
func (e *env) guest(t *testing.T) *api {
	jar, _ := cookiejar.New(nil)
	return &api{t: t, url: e.url, c: &http.Client{Jar: jar}}
}

type me struct {
	User struct {
		ID          int64  `json:"id"`
		Email       string `json:"email"`
		Handle      string `json:"handle"`
		Verified    bool   `json:"verified"`
		HasPassword bool   `json:"hasPassword"`
	} `json:"user"`
	Identities []struct{ Provider, Email string } `json:"identities"`
	Auth       platform.AuthMethods               `json:"auth"`
}

func openSignup(c *platform.Config) { c.Signup = platform.SignupOpen }

func TestSignupVerify(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", openSignup, func(c *platform.Config) { c.Email = out })

	var methods platform.AuthMethods
	anon(t, e, http.MethodGet, "/api/auth/methods", "", "", http.StatusOK, &methods)
	if methods.Signup != "open" || !methods.Email || len(methods.Providers) != 0 {
		t.Fatalf("methods = %+v", methods)
	}

	g := e.guest(t)
	g.do(http.MethodPost, "/api/signup", map[string]string{"email": " Grace@Example.com ", "name": "Grace", "handle": "grace", "password": "short"}, http.StatusBadRequest, nil)
	g.do(http.MethodPost, "/api/signup", map[string]string{"email": "grace@example.com", "name": "Grace", "handle": "ada", "password": "long enough"}, http.StatusConflict, nil)
	g.do(http.MethodPost, "/api/signup", map[string]string{"email": " Grace@Example.com ", "name": "Grace", "handle": "grace", "password": "long enough"}, http.StatusOK, nil)

	// Logged in, but nothing works until the email is confirmed.
	var m me
	g.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Email != "grace@example.com" || m.User.Verified || !m.User.HasPassword {
		t.Fatalf("me = %+v", m.User)
	}
	g.do(http.MethodGet, "/api/runs", nil, http.StatusForbidden, nil)

	mail := out.next(t, 1)
	if mail.to != "grace@example.com" || mail.subject != "Confirm your email for Lagrange" {
		t.Fatalf("mail = %+v", mail)
	}
	token := authLink(t, mail.body, "verify")
	// Another link's purpose doesn't work for this one.
	anon(t, e, http.MethodPost, "/api/login/link/claim", "application/json", `{"token":"`+token+`"}`, http.StatusGone, nil)
	g.do(http.MethodPost, "/api/auth/verify", map[string]string{"token": token}, http.StatusOK, nil)
	g.do(http.MethodGet, "/api/runs", nil, http.StatusOK, nil)
	g.do(http.MethodPost, "/api/auth/verify", map[string]string{"token": token}, http.StatusGone, nil)
	g.do(http.MethodPost, "/api/auth/verify/resend", map[string]any{}, http.StatusConflict, nil)

	// The email is case-insensitive at login too.
	e.login(t, "GRACE@example.com", "long enough")
}

func TestSignupInviteOnly(t *testing.T) {
	e := newEnv(t, "")
	anon(t, e, http.MethodPost, "/api/signup", "application/json",
		`{"email":"grace@example.com","name":"Grace","handle":"grace","password":"long enough"}`, http.StatusForbidden, nil)
	// Not JSON: refused before anything else (CSRF).
	anon(t, e, http.MethodPost, "/api/signup", "text/plain", `{}`, http.StatusUnsupportedMediaType, nil)
}

// Without email, signups can't be verified, so they are trusted.
func TestSignupWithoutEmail(t *testing.T) {
	e := newEnv(t, "", openSignup)
	g := e.guest(t)
	g.do(http.MethodPost, "/api/signup", map[string]string{"email": "grace@example.com", "name": "Grace", "handle": "grace", "password": "long enough"}, http.StatusOK, nil)
	g.do(http.MethodGet, "/api/runs", nil, http.StatusOK, nil)
	anon(t, e, http.MethodPost, "/api/login/link", "application/json", `{"email":"grace@example.com"}`, http.StatusServiceUnavailable, nil)
}

func TestMagicLinkLogin(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })

	// An unknown email gets the same answer and no email.
	anon(t, e, http.MethodPost, "/api/login/link", "application/json", `{"email":"nobody@example.com"}`, http.StatusAccepted, nil)
	anon(t, e, http.MethodPost, "/api/login/link", "application/json", `{"email":"ADA@example.com"}`, http.StatusAccepted, nil)
	mail := out.next(t, 1)
	if mail.to != adminEmail || mail.subject != "Your Lagrange login link" {
		t.Fatalf("mail = %+v", mail)
	}
	token := authLink(t, mail.body, "login")

	g := e.guest(t)
	g.do(http.MethodGet, "/api/me", nil, http.StatusUnauthorized, nil)
	g.do(http.MethodPost, "/api/login/link/claim", map[string]string{"token": token}, http.StatusOK, nil)
	var m me
	g.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Handle != "ada" {
		t.Fatalf("logged in as %+v", m.User)
	}
	e.guest(t).do(http.MethodPost, "/api/login/link/claim", map[string]string{"token": token}, http.StatusGone, nil)
}

func TestPasswordReset(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	before := e.login(t, adminEmail, adminPassword)

	anon(t, e, http.MethodPost, "/api/password/reset", "application/json", `{"email":"ada@example.com"}`, http.StatusAccepted, nil)
	token := authLink(t, out.next(t, 1).body, "reset")

	g := e.guest(t)
	g.do(http.MethodPost, "/api/password/reset/confirm", map[string]string{"token": token, "password": "short"}, http.StatusBadRequest, nil)
	g.do(http.MethodPost, "/api/password/reset/confirm", map[string]string{"token": token, "password": "a brand new password"}, http.StatusOK, nil)
	g.do(http.MethodGet, "/api/runs", nil, http.StatusOK, nil)
	// Sessions from before the reset are gone; the old password too.
	before.do(http.MethodGet, "/api/me", nil, http.StatusUnauthorized, nil)
	e.guest(t).do(http.MethodPost, "/api/login", map[string]string{"email": adminEmail, "password": adminPassword}, http.StatusUnauthorized, nil)
	e.login(t, adminEmail, "a brand new password")
	e.guest(t).do(http.MethodPost, "/api/password/reset/confirm", map[string]string{"token": token, "password": "yet another one"}, http.StatusGone, nil)
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, "")
	a := e.login(t, adminEmail, adminPassword)
	other := e.login(t, adminEmail, adminPassword)
	a.do(http.MethodPut, "/api/me/password", map[string]string{"current": "wrong", "password": "a brand new password"}, http.StatusForbidden, nil)
	a.do(http.MethodPut, "/api/me/password", map[string]string{"current": adminPassword, "password": "a brand new password"}, http.StatusNoContent, nil)
	// This session stays; the others end.
	a.do(http.MethodGet, "/api/me", nil, http.StatusOK, nil)
	other.do(http.MethodGet, "/api/me", nil, http.StatusUnauthorized, nil)
	e.login(t, adminEmail, "a brand new password")
}

func TestInviteAccept(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	ada := e.login(t, adminEmail, adminPassword)

	var inv platform.InviteView
	ada.do(http.MethodPost, "/api/invites", map[string]string{"email": adminEmail}, http.StatusConflict, nil)
	ada.do(http.MethodPost, "/api/invites", map[string]string{"email": "Grace@example.com"}, http.StatusCreated, &inv)
	if inv.Email != "grace@example.com" || !inv.Emailed || !strings.Contains(inv.Link, "/auth/invite/lga_") {
		t.Fatalf("invite = %+v", inv)
	}
	mail := out.next(t, 1)
	if mail.to != "grace@example.com" || mail.subject != "Ada invited you to Lagrange" {
		t.Fatalf("mail = %+v", mail)
	}
	token := authLink(t, mail.body, "invite")

	var view platform.InviteView
	anon(t, e, http.MethodGet, "/api/invites/"+token, "", "", http.StatusOK, &view)
	if view.Email != "grace@example.com" || view.InvitedBy != "Ada" || view.Link != "" {
		t.Fatalf("view = %+v", view)
	}
	g := e.guest(t)
	// A taken handle leaves the invitation usable.
	g.do(http.MethodPost, "/api/invites/"+token+"/accept", map[string]string{"name": "Grace", "handle": "ada", "password": "long enough"}, http.StatusConflict, nil)
	g.do(http.MethodPost, "/api/invites/"+token+"/accept", map[string]string{"name": "Grace", "handle": "grace", "password": "long enough"}, http.StatusOK, nil)
	var m me
	g.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Handle != "grace" || !m.User.Verified {
		t.Fatalf("me = %+v", m.User)
	}
	anon(t, e, http.MethodGet, "/api/invites/"+token, "", "", http.StatusGone, nil)

	// Only admins invite.
	g.do(http.MethodPost, "/api/invites", map[string]string{"email": "linus@example.com"}, http.StatusForbidden, nil)
}

// Without email, the invitation link comes back to share by hand.
func TestInviteWithoutEmail(t *testing.T) {
	e := newEnv(t, "")
	var inv platform.InviteView
	e.login(t, adminEmail, adminPassword).do(http.MethodPost, "/api/invites", map[string]string{"email": "grace@example.com"}, http.StatusCreated, &inv)
	if inv.Emailed || !strings.Contains(inv.Link, e.url+"/auth/invite/lga_") {
		t.Fatalf("invite = %+v", inv)
	}
}

func TestAuthRateLimit(t *testing.T) {
	e := newEnv(t, "", func(c *platform.Config) { c.AuthRateLimit = 3 })
	g := e.guest(t)
	for range 3 {
		g.do(http.MethodPost, "/api/login", map[string]string{"email": adminEmail, "password": "wrong"}, http.StatusUnauthorized, nil)
	}
	g.do(http.MethodPost, "/api/login", map[string]string{"email": adminEmail, "password": adminPassword}, http.StatusTooManyRequests, nil)
}

// fakeProvider is an OAuth provider whose token endpoint answers any code, and whose
// account is whatever *profile holds.
func fakeProvider(t *testing.T, profile **platform.OAuthProfile) *platform.OAuthProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code") == "" || r.Form.Get("code_verifier") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	t.Cleanup(srv.Close)
	return &platform.OAuthProvider{
		Name: "fake", Label: "Fake",
		Config: oauth2.Config{ClientID: "id", ClientSecret: "secret", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/authorize", TokenURL: srv.URL + "/token"}},
		Profile: func(context.Context, *http.Client) (*platform.OAuthProfile, error) {
			p := **profile
			return &p, nil
		},
	}
}

// oauth signs in through the provider as browser a, and returns where the callback
// sends the browser.
func (a *api) oauth(t *testing.T, query string) *url.URL {
	t.Helper()
	c := *a.c
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(a.url + "/api/auth/fake/start" + query)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	to, err := resp.Location()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if to.Query().Get("code_challenge") == "" || !strings.HasSuffix(to.Query().Get("redirect_uri"), "/api/auth/fake/callback") {
		t.Fatalf("authorize URL = %s", to)
	}
	resp, err = c.Get(a.url + "/api/auth/fake/callback?code=c&state=" + url.QueryEscape(to.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, err := resp.Location()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	return back
}

func TestOAuthLogin(t *testing.T) {
	profile := &platform.OAuthProfile{Subject: "1", Email: "someone@example.com", EmailVerified: true, Name: "Someone", Login: "someone"}
	prov := fakeProvider(t, &profile)
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.OAuth = []*platform.OAuthProvider{prov}; c.Email = out })

	// Invite-only: an unknown account can't get in.
	g := e.guest(t)
	if back := g.oauth(t, ""); back.Path != "/" || !strings.Contains(back.Query().Get("authError"), "invitation") {
		t.Fatalf("back = %s", back)
	}
	g.do(http.MethodGet, "/api/me", nil, http.StatusUnauthorized, nil)

	// The same verified email as Ada: connects to her account.
	profile = &platform.OAuthProfile{Subject: "2", Email: "Ada@example.com", EmailVerified: true, Name: "Ada L", Login: "ada"}
	if back := g.oauth(t, "?next=/runs"); back.RequestURI() != "/runs" {
		t.Fatalf("back = %s", back)
	}
	var m me
	g.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Handle != "ada" || len(m.Identities) != 1 || m.Identities[0].Provider != "fake" || len(m.Auth.Providers) != 1 {
		t.Fatalf("me = %+v", m)
	}
	// Next time, the account alone logs her in, even with another email at the provider.
	profile = &platform.OAuthProfile{Subject: "2", Email: "elsewhere@example.com", EmailVerified: true}
	g2 := e.guest(t)
	if back := g2.oauth(t, "?next=//evil.example.com"); back.RequestURI() != "/" {
		t.Fatalf("back = %s", back)
	}
	g2.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Handle != "ada" {
		t.Fatalf("me = %+v", m.User)
	}

	// An unverified email at the provider doesn't take over Ada's account.
	profile = &platform.OAuthProfile{Subject: "3", Email: "ada@example.com", EmailVerified: false}
	if back := e.guest(t).oauth(t, ""); back.Query().Get("authError") == "" {
		t.Fatalf("back = %s", back)
	}

	// Signed in: connecting a second account goes back to Settings.
	profile = &platform.OAuthProfile{Subject: "4", Email: "ada@work.example.com", EmailVerified: true}
	if back := g.oauth(t, ""); back.RequestURI() != "/settings?linked=fake" {
		t.Fatalf("back = %s", back)
	}

	// An invitation accepted with the provider: the invited email, a handle from the login.
	var inv platform.InviteView
	e.login(t, adminEmail, adminPassword).do(http.MethodPost, "/api/invites", map[string]string{"email": "grace@example.com"}, http.StatusCreated, &inv)
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	profile = &platform.OAuthProfile{Subject: "5", Email: "gh@example.com", EmailVerified: true, Name: "Grace Hopper", Login: "Grace.Hopper"}
	g3 := e.guest(t)
	if back := g3.oauth(t, "?invite="+token); back.RequestURI() != "/" {
		t.Fatalf("back = %s", back)
	}
	g3.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	if m.User.Handle != "grace-hopper" || m.User.Email != "grace@example.com" || !m.User.Verified || m.User.HasPassword {
		t.Fatalf("me = %+v", m.User)
	}
	// No password: logging in with one fails; setting a first one needs no current password.
	g3.do(http.MethodPut, "/api/me/password", map[string]string{"password": "now with a password"}, http.StatusNoContent, nil)
	e.login(t, "grace@example.com", "now with a password")
}

func TestOAuthOpenSignup(t *testing.T) {
	profile := &platform.OAuthProfile{Subject: "9", Email: "linus@example.com", EmailVerified: true, Name: "Linus", Login: "ada"}
	prov := fakeProvider(t, &profile)
	e := newEnv(t, "", openSignup, func(c *platform.Config) { c.OAuth = []*platform.OAuthProvider{prov} })
	g := e.guest(t)
	if back := g.oauth(t, ""); back.RequestURI() != "/" {
		t.Fatalf("back = %s", back)
	}
	var m me
	g.do(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	// "ada" is taken, so the handle gets a suffix.
	if m.User.Handle != "ada-2" || !m.User.Verified {
		t.Fatalf("me = %+v", m.User)
	}

	// A callback without the start's cookie is refused.
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		Get(e.url + "/api/auth/fake/callback?code=c&state=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if to, _ := resp.Location(); to == nil || to.Query().Get("authError") == "" {
		t.Fatalf("callback without state = %v", to)
	}
}
