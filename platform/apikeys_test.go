package platform_test

import (
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/three-agent-problem/lagrange/directory"
)

// withKey is a client that authenticates with an API key only, no cookie.
func (e *env) withKey(t *testing.T, key string) *api {
	return &api{t: t, url: e.url, c: &http.Client{}, key: key}
}

type createdKey struct {
	Key struct {
		ID        int64
		Prefix    string
		Role      string
		Workspace string
	}
	Secret string
}

func TestAPIKeyAuth(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	ada := e.login(t, adminEmail, adminPassword)
	if _, err := e.p.Directory.CreateWorkspace(ctx, "Other", "other", 1); err != nil {
		t.Fatal(err)
	}

	var k createdKey
	ada.do(http.MethodPost, "/api/keys", map[string]string{"name": "ci", "workspace": directory.DefaultWorkspace, "role": "member"}, http.StatusCreated, &k)
	if len(k.Secret) < 20 || k.Secret[:4] != directory.APIKeyPrefix || k.Key.Prefix != k.Secret[:12] || k.Key.Role != "member" {
		t.Fatalf("key = %+v", k)
	}
	ci := e.withKey(t, k.Secret)

	// It acts as Ada, in its workspace, capped at member.
	var me struct {
		User struct{ Handle string }
		Key  struct{ Workspace, Role string }
	}
	ci.do(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	if me.User.Handle != "ada" || me.Key.Workspace != directory.DefaultWorkspace || me.Key.Role != "member" {
		t.Fatalf("me = %+v", me)
	}
	ci.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "echo"}, http.StatusCreated, nil)
	ci.do(http.MethodGet, "/api/runs", nil, http.StatusOK, nil)
	ci.do(http.MethodPost, "/api/addresses/echo/tokens", map[string]string{"label": "ci"}, http.StatusCreated, nil)
	// Ada is the owner, but the key is a member: admin routes are refused.
	ci.do(http.MethodPut, "/api/settings/wait-policy", map[string]any{}, http.StatusForbidden, nil)
	// Another workspace, even one Ada owns, is refused.
	ci.in("other").do(http.MethodGet, "/api/runs", nil, http.StatusForbidden, nil)
	// Keys can't manage keys, passwords or workspaces.
	ci.do(http.MethodGet, "/api/keys", nil, http.StatusForbidden, nil)
	ci.do(http.MethodPost, "/api/keys", map[string]string{"name": "x", "workspace": "default", "role": "viewer"}, http.StatusForbidden, nil)
	ci.do(http.MethodPost, "/api/workspaces", map[string]string{"name": "x"}, http.StatusForbidden, nil)
	ci.do(http.MethodPost, "/api/cli/authorize", map[string]string{"challenge": "x", "workspace": "default", "role": "viewer"}, http.StatusForbidden, nil)

	// A viewer key can look but not send or answer.
	var v createdKey
	ada.do(http.MethodPost, "/api/keys", map[string]string{"name": "dash", "workspace": "default", "role": "viewer"}, http.StatusCreated, &v)
	viewer := e.withKey(t, v.Secret)
	viewer.do(http.MethodGet, "/api/waiting", nil, http.StatusOK, nil)
	viewer.do(http.MethodPost, "/api/send", map[string]string{"to": "echo", "text": "hi"}, http.StatusForbidden, nil)
	// Viewer keys are read-only, even for tasks that wait on their person.
	viewer.do(http.MethodPost, "/api/tasks/x/answer", map[string]string{"text": "ok"}, http.StatusForbidden, nil)

	// Bob, a member, can't make an admin key.
	if _, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	bob := e.login(t, "bob@example.com", "hunter2hunter2")
	bob.do(http.MethodPost, "/api/keys", map[string]string{"name": "x", "workspace": "default", "role": "admin"}, http.StatusForbidden, nil)
	bob.do(http.MethodPost, "/api/keys", map[string]string{"name": "x", "workspace": "other", "role": "viewer"}, http.StatusForbidden, nil)
	// Nor revoke Ada's.
	bob.do(http.MethodDelete, "/api/keys/"+strconv.FormatInt(k.Key.ID, 10), struct{}{}, http.StatusNotFound, nil)

	var list struct{ Keys []directory.APIKey }
	ada.do(http.MethodGet, "/api/keys", nil, http.StatusOK, &list)
	if len(list.Keys) != 2 || list.Keys[1].Name != "ci" || list.Keys[1].LastUsed == nil {
		t.Fatalf("keys = %+v", list.Keys)
	}

	// Revoked and unknown keys get 401.
	ada.do(http.MethodDelete, "/api/keys/"+strconv.FormatInt(k.Key.ID, 10), struct{}{}, http.StatusNoContent, nil)
	ci.do(http.MethodGet, "/api/runs", nil, http.StatusUnauthorized, nil)
	e.withKey(t, "lgk_nope").do(http.MethodGet, "/api/runs", nil, http.StatusUnauthorized, nil)

	// Losing the role in the workspace lowers what the key can do.
	var bk createdKey
	bob.do(http.MethodPost, "/api/keys", map[string]string{"name": "bob", "workspace": "default", "role": "member"}, http.StatusCreated, &bk)
	def, _ := e.p.Directory.Default(ctx)
	bobUser, _ := e.p.Directory.UserByHandle(ctx, "bob")
	if err := e.p.Directory.SetRole(ctx, def.ID, bobUser.ID, directory.RoleViewer); err != nil {
		t.Fatal(err)
	}
	e.withKey(t, bk.Secret).do(http.MethodPost, "/api/send", map[string]string{"to": "echo", "text": "hi"}, http.StatusForbidden, nil)
}

func TestCLILogin(t *testing.T) {
	e := newEnv(t, "")
	ada := e.login(t, adminEmail, adminPassword)
	verifier := "a-verifier-that-is-long-enough-for-pkce-0123456789"
	challenge := directory.PKCEChallenge(verifier)

	var auth struct{ Code, Redirect string }
	ada.do(http.MethodPost, "/api/cli/authorize", map[string]any{"challenge": challenge, "workspace": "default", "role": "admin", "port": 51234}, http.StatusOK, &auth)
	if auth.Redirect != "http://127.0.0.1:51234/callback?code="+auth.Code {
		t.Fatalf("redirect = %q", auth.Redirect)
	}
	guest := e.guest(t)
	guest.do(http.MethodPost, "/api/cli/token", map[string]string{"code": auth.Code, "verifier": "wrong"}, http.StatusBadRequest, nil)
	// A wrong verifier burns the code.
	guest.do(http.MethodPost, "/api/cli/token", map[string]string{"code": auth.Code, "verifier": verifier}, http.StatusBadRequest, nil)

	auth.Redirect = ""
	ada.do(http.MethodPost, "/api/cli/authorize", map[string]any{"challenge": challenge, "workspace": "default", "role": "admin"}, http.StatusOK, &auth)
	if auth.Redirect != "" {
		t.Fatalf("paste flow got a redirect: %q", auth.Redirect)
	}
	var tok struct{ Key, Workspace, Role, Server string }
	guest.do(http.MethodPost, "/api/cli/token", map[string]string{"code": auth.Code, "verifier": verifier}, http.StatusOK, &tok)
	if tok.Workspace != "default" || tok.Role != "admin" || tok.Server != e.url {
		t.Fatalf("token = %+v", tok)
	}
	e.withKey(t, tok.Key).do(http.MethodGet, "/api/waiting", nil, http.StatusOK, nil)
	// Once only.
	guest.do(http.MethodPost, "/api/cli/token", map[string]string{"code": auth.Code, "verifier": verifier}, http.StatusBadRequest, nil)

	// Bad ports and roles above your own are refused.
	ada.do(http.MethodPost, "/api/cli/authorize", map[string]any{"challenge": challenge, "workspace": "default", "role": "admin", "port": 80}, http.StatusBadRequest, nil)
	if _, err := e.p.Directory.CreateUser(t.Context(), "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	e.login(t, "bob@example.com", "hunter2hunter2").do(http.MethodPost, "/api/cli/authorize",
		map[string]any{"challenge": challenge, "workspace": "default", "role": "admin"}, http.StatusForbidden, nil)
}

func TestOpenAPIValid(t *testing.T) {
	e := newEnv(t, "")
	resp, err := http.Get(e.url + "/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	doc, err := openapi3.NewLoader().LoadFromData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/send", "/api/runs", "/api/waiting", "/api/tasks/{id}/answer", "/api/cli/token"} {
		if doc.Paths.Find(path) == nil {
			t.Errorf("spec has no %s", path)
		}
	}
}
