package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
)

// PasswordLogin makes an API key with an email and a password, for a machine
// without a browser: it logs in (a session cookie), makes a key named name in
// workspace (its slug; empty: the person's first workspace) with their role there,
// and logs the session out. Accounts that sign in only with GitHub or Google
// can't use it.
func PasswordLogin(ctx context.Context, server, email, password, workspace, name string) (*Login, error) {
	jar, _ := cookiejar.New(nil)
	c := New(server, "")
	c.HTTP = &http.Client{Timeout: defaultHTTP.Timeout, Jar: jar}
	if err := c.Do(ctx, http.MethodPost, "/api/login", map[string]string{"email": email, "password": password}, nil); err != nil {
		return nil, err
	}
	defer func() { _ = c.Do(context.WithoutCancel(ctx), http.MethodPost, "/api/logout", nil, nil) }()
	var me struct {
		Workspaces []struct{ Slug, Role string }
	}
	if err := c.Do(ctx, http.MethodGet, "/api/me", nil, &me); err != nil {
		return nil, err
	}
	var slug, role string
	for _, w := range me.Workspaces {
		if workspace == "" || w.Slug == workspace {
			slug, role = w.Slug, w.Role
			break
		}
	}
	if slug == "" {
		if workspace != "" {
			return nil, errors.New("you aren't a member of " + workspace)
		}
		return nil, errors.New("you aren't in any workspace yet")
	}
	var out struct{ Secret string }
	if err := c.Do(ctx, http.MethodPost, "/api/keys", map[string]string{"name": name, "workspace": slug, "role": role}, &out); err != nil {
		return nil, err
	}
	return &Login{Key: out.Secret, Workspace: slug, Role: role, Server: c.Server}, nil
}
