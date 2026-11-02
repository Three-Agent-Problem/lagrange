package platform

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/three-agent-problem/lagrange/directory"
)

// Personal API keys (lgk_…) let scripts, CI, the CLI and MCP clients use /api as a
// person: in one workspace, with at most the role chosen when the key was made. Keys are
// managed with a web session only, so a leaked key can't mint more keys.

type keyGrantKey struct{}

// bearerKey returns the API key in the Authorization header, if there is one.
func bearerKey(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(token, directory.APIKeyPrefix) {
		return "", false
	}
	return token, true
}

func keyGrantOf(ctx context.Context) *directory.KeyGrant {
	g, _ := ctx.Value(keyGrantKey{}).(*directory.KeyGrant)
	return g
}

func (p *Platform) keyRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/keys", p.authed(p.handleAPIKeys))
	mux.Handle("POST /api/keys", p.authed(p.handleCreateAPIKey))
	mux.Handle("DELETE /api/keys/{id}", p.authed(p.handleRevokeAPIKey))
	mux.Handle("POST /api/cli/authorize", p.authed(p.handleCLIAuthorize))
	mux.Handle("POST /api/cli/token", p.public(p.handleCLIToken))
	mux.HandleFunc("GET /api/openapi.yaml", handleOpenAPI)
}

func (p *Platform) handleAPIKeys(w http.ResponseWriter, r *http.Request, me *directory.User) {
	keys, err := p.Directory.APIKeys(r.Context(), me.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type keyBody struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
}

// workspaceFor resolves the workspace a new key or CLI login is for.
func (p *Platform) workspaceFor(w http.ResponseWriter, r *http.Request, me *directory.User, slug string) (*directory.Workspace, bool) {
	ws, err := p.Directory.Membership(r.Context(), me.ID, slug)
	if errors.Is(err, directory.ErrNotMember) {
		apiError(w, http.StatusForbidden, "you aren't a member of "+slug)
		return nil, false
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return ws, true
}

func (p *Platform) keyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrRoleTooHigh), errors.Is(err, directory.ErrNotMember):
		apiError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, directory.ErrBadCode):
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		apiError(w, http.StatusBadRequest, err.Error())
	}
}

// handleCreateAPIKey makes a key and shows its secret once.
func (p *Platform) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body keyBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {name, workspace, role}")
		return
	}
	ws, ok := p.workspaceFor(w, r, me, body.Workspace)
	if !ok {
		return
	}
	secret, key, err := p.Directory.CreateAPIKey(r.Context(), me.ID, ws.ID, body.Name, body.Role)
	if err != nil {
		p.keyError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "secret": secret})
}

func (p *Platform) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, me *directory.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "keys are named by their ID")
		return
	}
	if err := p.Directory.RevokeAPIKey(r.Context(), me.ID, id); errors.Is(err, directory.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such key")
		return
	} else if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type cliAuthorizeBody struct {
	keyBody
	Challenge string `json:"challenge"`
	// Port is where `lagrange login` listens on 127.0.0.1; zero means the person pastes
	// the code into the terminal.
	Port int `json:"port"`
}

// handleCLIAuthorize is the browser half of `lagrange login`: the person approves, and
// gets a one-time code for the CLI, either through a redirect to the CLI's loopback
// listener or to paste.
func (p *Platform) handleCLIAuthorize(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body cliAuthorizeBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {name, workspace, role, challenge, port}")
		return
	}
	if body.Port != 0 && (body.Port < 1024 || body.Port > 65535) {
		apiError(w, http.StatusBadRequest, "port must be between 1024 and 65535")
		return
	}
	ws, ok := p.workspaceFor(w, r, me, body.Workspace)
	if !ok {
		return
	}
	if !directory.ValidRole(body.Role) {
		apiError(w, http.StatusBadRequest, "unknown role "+body.Role)
		return
	}
	if !directory.RoleAtLeast(ws.Role, body.Role) {
		apiError(w, http.StatusForbidden, directory.ErrRoleTooHigh.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "lagrange CLI"
	}
	code, err := p.Directory.StartCLILogin(r.Context(), me.ID, ws.ID, name, body.Role, body.Challenge)
	if err != nil {
		p.keyError(w, err)
		return
	}
	out := map[string]any{"code": code}
	if body.Port != 0 {
		// Loopback only: the code never leaves this machine's browser and CLI.
		out["redirect"] = "http://127.0.0.1:" + strconv.Itoa(body.Port) + "/callback?code=" + url.QueryEscape(code)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCLIToken is the CLI half: it trades the code and its PKCE verifier for a key.
func (p *Platform) handleCLIToken(w http.ResponseWriter, r *http.Request) {
	var body struct{ Code, Verifier string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {code, verifier}")
		return
	}
	secret, key, err := p.Directory.ClaimCLILogin(r.Context(), strings.TrimSpace(body.Code), body.Verifier)
	if err != nil {
		p.keyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": secret, "workspace": key.Workspace, "role": key.Role, "server": p.cfg.PublicURL})
}
