package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/three-agent-problem/lagrange/directory"
)

// OAuthProvider logs people in with an account elsewhere (GitHub, Google).
type OAuthProvider struct {
	// Name is the provider's ID in URLs and in the identities table, such as "github".
	Name string
	// Label is what the login button says.
	Label string
	// Config holds the client and endpoints. The platform sets RedirectURL to
	// <PublicURL>/api/auth/<Name>/callback.
	Config oauth2.Config
	// Profile reads the signed-in account with a client that carries its token.
	Profile func(ctx context.Context, c *http.Client) (*OAuthProfile, error)
}

// OAuthProfile is the account a person signed in with.
type OAuthProfile struct {
	// Subject is the provider's stable account ID.
	Subject string
	Email   string
	// EmailVerified: the provider vouches that the person owns Email.
	EmailVerified bool
	Name          string
	// Login is a username to suggest a handle from.
	Login string
}

// GitHubProvider logs in with GitHub (an OAuth app with callback
// <PublicURL>/api/auth/github/callback).
func GitHubProvider(clientID, secret string) *OAuthProvider {
	return &OAuthProvider{
		Name:  "github",
		Label: "GitHub",
		Config: oauth2.Config{
			ClientID: clientID, ClientSecret: secret, Scopes: []string{"read:user", "user:email"},
			Endpoint: oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token"},
		},
		Profile: githubProfile("https://api.github.com"),
	}
}

func githubProfile(api string) func(context.Context, *http.Client) (*OAuthProfile, error) {
	return func(ctx context.Context, c *http.Client) (*OAuthProfile, error) {
		var user struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Name  string `json:"name"`
		}
		if err := getJSON(ctx, c, api+"/user", &user); err != nil {
			return nil, err
		}
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := getJSON(ctx, c, api+"/user/emails", &emails); err != nil {
			return nil, err
		}
		p := &OAuthProfile{Subject: strconv.FormatInt(user.ID, 10), Name: user.Name, Login: user.Login}
		for _, e := range emails {
			if e.Primary {
				p.Email, p.EmailVerified = e.Email, e.Verified
			}
		}
		return p, nil
	}
}

// GoogleProvider logs in with Google (an OAuth client with redirect URI
// <PublicURL>/api/auth/google/callback).
func GoogleProvider(clientID, secret string) *OAuthProvider {
	return &OAuthProvider{
		Name:  "google",
		Label: "Google",
		Config: oauth2.Config{
			ClientID: clientID, ClientSecret: secret, Scopes: []string{"openid", "email", "profile"},
			Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
		},
		Profile: func(ctx context.Context, c *http.Client) (*OAuthProfile, error) {
			var info struct {
				Sub           string `json:"sub"`
				Email         string `json:"email"`
				EmailVerified bool   `json:"email_verified"`
				Name          string `json:"name"`
			}
			if err := getJSON(ctx, c, "https://openidconnect.googleapis.com/v1/userinfo", &info); err != nil {
				return nil, err
			}
			return &OAuthProfile{Subject: info.Sub, Email: info.Email, EmailVerified: info.EmailVerified, Name: info.Name, Login: info.Email}, nil
		},
	}
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (p *Platform) provider(name string) *OAuthProvider {
	for _, o := range p.cfg.OAuth {
		if o.Name == name {
			return o
		}
	}
	return nil
}

const oauthCookie = "lagrange_oauth"

// oauthState travels in a short-lived cookie from start to callback.
type oauthState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Provider string `json:"p"`
	// Next is where to go after signing in (a path on this server).
	Next string `json:"n,omitempty"`
	// Invite is an invitation token being accepted with this provider.
	Invite string `json:"i,omitempty"`
}

// handleOAuthStart sends the browser to the provider. ?next=/path returns there after;
// ?invite=<token> accepts an invitation with the provider's account.
func (p *Platform) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	o := p.provider(r.PathValue("provider"))
	if o == nil {
		http.NotFound(w, r)
		return
	}
	st := oauthState{
		State: rand.Text(), Verifier: oauth2.GenerateVerifier(), Provider: o.Name,
		Next: safeNext(r.URL.Query().Get("next")), Invite: r.URL.Query().Get("invite"),
	}
	raw, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/api/auth/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: p.secure(), MaxAge: int((10 * time.Minute).Seconds()),
	})
	http.Redirect(w, r, o.Config.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

// safeNext keeps only paths on this server, so the redirect can't send people elsewhere.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return ""
	}
	return next
}

func (p *Platform) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	o := p.provider(r.PathValue("provider"))
	if o == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(msg string) {
		http.Redirect(w, r, "/?authError="+url.QueryEscape(msg), http.StatusFound)
	}
	var st oauthState
	c, err := r.Cookie(oauthCookie)
	if err == nil {
		raw, _ := base64.RawURLEncoding.DecodeString(c.Value)
		err = json.Unmarshal(raw, &st)
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/api/auth/", MaxAge: -1})
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(fmt.Sprintf("%s sign-in was cancelled (%s)", o.Label, firstNonEmpty(q.Get("error_description"), e)))
		return
	}
	if err != nil || st.State == "" || q.Get("state") != st.State || st.Provider != o.Name {
		fail("That sign-in took too long or came from another tab. Try again.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := o.Config.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		p.log.Warn("platform: oauth exchange", "provider", o.Name, "error", err)
		fail(o.Label + " didn't confirm the sign-in. Try again.")
		return
	}
	prof, err := o.Profile(ctx, o.Config.Client(ctx, tok))
	if err != nil || prof.Subject == "" {
		p.log.Warn("platform: oauth profile", "provider", o.Name, "error", err)
		fail("Couldn't read your " + o.Label + " account. Try again.")
		return
	}
	userID, next, msg := p.oauthUser(r, o, prof, st)
	if msg != "" {
		fail(msg)
		return
	}
	if userID != 0 {
		_, token, err := p.Directory.StartSession(r.Context(), userID)
		if err != nil {
			fail("Couldn't start your session. Try again.")
			return
		}
		p.setSession(w, token)
	}
	http.Redirect(w, r, firstNonEmpty(next, st.Next, "/"), http.StatusFound)
}

// oauthUser decides who a provider account logs in as. It returns the user to start a
// session for (zero: keep the current one), where to go, or a message for the person.
func (p *Platform) oauthUser(r *http.Request, o *OAuthProvider, prof *OAuthProfile, st oauthState) (int64, string, string) {
	ctx := r.Context()
	me := p.sessionUser(r)
	email := directory.NormalizeEmail(prof.Email)
	known, err := p.Directory.UserByIdentity(ctx, o.Name, prof.Subject)
	switch {
	case err == nil:
		if me != nil && me.ID != known.ID {
			return 0, "", fmt.Sprintf("That %s account already belongs to @%s.", o.Label, known.Handle)
		}
		return known.ID, "", ""
	case !errors.Is(err, directory.ErrNotFound):
		return 0, "", "Something went wrong. Try again."
	}
	// Signed in already: connect the account (from Settings).
	if me != nil {
		if err := p.Directory.LinkIdentity(ctx, me.ID, o.Name, prof.Subject, email); err != nil {
			return 0, "", "Couldn't connect that account."
		}
		return 0, "/settings?linked=" + o.Name, ""
	}
	// Accepting an invitation: the link proves the invited email, whatever the provider's is.
	if st.Invite != "" {
		l, err := p.Directory.ClaimAuthLink(ctx, st.Invite, directory.LinkInvite)
		if err != nil {
			return 0, "", "This invitation doesn't work any more; ask for a new one."
		}
		u, err := p.registerFromProvider(ctx, o, prof, l.Email, true)
		if err != nil {
			_ = p.Directory.ReleaseAuthLink(ctx, st.Invite)
			return 0, "", userMessage(err)
		}
		return u.ID, "", ""
	}
	// Someone already here with the same, provider-verified email: connect and log in.
	if prof.EmailVerified && email != "" {
		if u, err := p.Directory.UserByEmail(ctx, email); err == nil {
			if err := p.Directory.LinkIdentity(ctx, u.ID, o.Name, prof.Subject, email); err != nil {
				return 0, "", "Couldn't connect that account."
			}
			_ = p.Directory.MarkVerified(ctx, u.ID)
			return u.ID, "", ""
		}
	}
	if p.signupMode() != SignupOpen {
		return 0, "", "There's no account for that " + o.Label + " login here yet. Ask someone on this server for an invitation."
	}
	if email == "" {
		return 0, "", "Your " + o.Label + " account has no email address we can use."
	}
	u, err := p.registerFromProvider(ctx, o, prof, email, prof.EmailVerified)
	if err != nil {
		return 0, "", userMessage(err)
	}
	if !u.Verified {
		p.sendVerify(ctx, u)
	}
	return u.ID, "", ""
}

func (p *Platform) registerFromProvider(ctx context.Context, o *OAuthProvider, prof *OAuthProfile, email string, verified bool) (*directory.User, error) {
	handle, err := p.Directory.FreeHandle(ctx, firstNonEmpty(prof.Login, prof.Name, email))
	if err != nil {
		return nil, err
	}
	u, err := p.Directory.Register(ctx, directory.Registration{
		Email: email, Name: firstNonEmpty(prof.Name, prof.Login, handle), Handle: handle,
		Verified: verified || p.cfg.Email == nil, Provider: o.Name, Subject: prof.Subject,
	})
	if err != nil {
		return nil, err
	}
	p.addressChanged(ctx, u.Handle)
	return u, nil
}
