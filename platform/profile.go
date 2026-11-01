package platform

import (
	"context"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/three-agent-problem/registry/lint"

	"github.com/three-agent-problem/lagrange/directory"
)

// Every address has a public page at its URL: a browser asking for /a/<handle> gets the
// web app, which shows the address's card, how to call it and, for a signed-in visitor,
// a way to try it or to ask for access. A2A clients asking for the same URL get the card
// as JSON, as before.

// ProfileView is what anyone may see about an address, and for a signed-in visitor, how
// each of their workspaces stands with it.
type ProfileView struct {
	Handle      string         `json:"handle"`
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	CardURL     string         `json:"cardUrl"`
	Card        *a2a.AgentCard `json:"card"`
	Access      string         `json:"access"`
	Online      bool           `json:"online"`
	// Workspace is the name of an agent's workspace.
	Workspace string `json:"workspace,omitempty"`
	// Check is the card as served here, graded by the registry's linter.
	Check  lint.Report `json:"check"`
	Viewer *Viewer     `json:"viewer,omitempty"`
}

// Viewer is the signed-in visitor of a public page.
type Viewer struct {
	Handle     string            `json:"handle"`
	Workspaces []ViewerWorkspace `json:"workspaces"`
}

// ViewerWorkspace is how one of the visitor's workspaces stands with the address:
// "in" (the address is part of it), a connection's status, or "" (no connection).
type ViewerWorkspace struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

func (p *Platform) profileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/public/addresses/{handle}", p.handleProfile)
}

// handleProfile serves an address's public page data. It needs no session; with one
// (verified), it adds the visitor's standing.
func (p *Platform) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !p.limits.allow("ip:" + p.clientIP(r)) {
		apiError(w, http.StatusTooManyRequests, "too many requests; wait a minute and try again")
		return
	}
	addr, err := p.Directory.Address(ctx, r.PathValue("handle"))
	if err != nil {
		apiError(w, http.StatusNotFound, "no such address")
		return
	}
	view := p.addressView(addr)
	v := &ProfileView{
		Handle: addr.Handle, Kind: addr.Kind, Name: view.Card.Name, Description: view.Card.Description,
		URL: p.cfg.PublicURL + "/a/" + addr.Handle, CardURL: view.CardURL, Card: view.Card,
		Access: addr.Access, Online: view.Online,
	}
	if raw, err := json.Marshal(view.Card); err == nil {
		v.Check = lint.Check(raw)
	}
	if addr.WorkspaceID != 0 {
		if ws, err := p.Directory.WorkspaceByID(ctx, addr.WorkspaceID); err == nil {
			v.Workspace = ws.Name
		}
	}
	if me := p.sessionUser(r); me != nil && me.Verified {
		if v.Viewer, err = p.viewer(ctx, me, addr.Handle); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func (p *Platform) viewer(ctx context.Context, me *directory.User, handle string) (*Viewer, error) {
	all, err := p.Directory.Workspaces(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	v := &Viewer{Handle: me.Handle, Workspaces: []ViewerWorkspace{}}
	for _, ws := range all {
		vw := ViewerWorkspace{Slug: ws.Slug, Name: ws.Name, Role: ws.Role}
		handles, err := p.Directory.WorkspaceHandles(ctx, ws.ID)
		if err != nil {
			return nil, err
		}
		if contains(handles, handle) {
			vw.Status = "in"
		} else {
			conns, err := p.Directory.ConnectionsFrom(ctx, ws.ID)
			if err != nil {
				return nil, err
			}
			for _, c := range conns {
				if c.To == handle {
					vw.Status = c.Status
				}
			}
		}
		v.Workspaces = append(v.Workspaces, vw)
	}
	return v, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// wantsPage reports whether a request for an address URL comes from a browser: it asks
// for HTML. A2A clients ask for JSON or anything.
func wantsPage(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch mt {
		case "text/html":
			return true
		case "application/json", "application/a2a+json":
			return false
		}
	}
	return false
}

// servePage serves the web app for an address's page, with the title and description in
// the HTML, so links shared in chats and search results name the address.
func (p *Platform) servePage(w http.ResponseWriter, r *http.Request, addr *directory.Address) {
	page, err := fs.ReadFile(p.cfg.Web, "index.html")
	if p.cfg.Web == nil || err != nil {
		webHandler(p.cfg.Web).ServeHTTP(w, r)
		return
	}
	status := http.StatusOK
	title, desc := "No such address · Lagrange", "This address doesn't exist on Lagrange."
	if addr != nil {
		card := p.Card(addr)
		title = card.Name + " (@" + addr.Handle + ") · Lagrange"
		desc = clip(card.Description, 300)
	} else {
		status = http.StatusNotFound
	}
	head := "<title>" + html.EscapeString(title) + "</title>" +
		`<meta name="description" content="` + html.EscapeString(desc) + `" />` +
		`<meta property="og:title" content="` + html.EscapeString(title) + `" />` +
		`<meta property="og:description" content="` + html.EscapeString(desc) + `" />`
	if addr != nil {
		// Lets a2a tooling that reads the page find the card.
		head += `<link rel="alternate" type="application/json" href="` + html.EscapeString(p.cfg.PublicURL+"/a/"+addr.Handle+a2asrv.WellKnownAgentCardPath) + `" />`
	}
	out := strings.Replace(string(page), "<title>Lagrange</title>", head, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(out))
}
