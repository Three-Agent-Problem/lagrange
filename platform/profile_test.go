package platform_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/platform"
)

type profile struct {
	Handle, Kind, Name, Access, Workspace string
	Online                                bool
	Check                                 struct{ Grade string }
	Viewer                                *struct {
		Handle     string
		Workspaces []struct{ Slug, Role, Status string }
	}
}

// get fetches path with an Accept header and returns the status, content type and body.
func get(t *testing.T, e *env, path, accept string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
}

// An address URL is a public page for browsers and the card for A2A clients; the page's
// data says how the visitor's workspaces stand with the address.
func TestPublicPage(t *testing.T) {
	web := fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><head><title>Lagrange</title></head><div id="root"></div>`)}}
	e := newEnv(t, "", func(c *platform.Config) { c.Web = web })
	ctx := t.Context()
	nora, err := e.p.Directory.Register(ctx, directory.Registration{Email: "nora@example.com", Name: "Nora", Handle: "nora", Password: "northwind wins", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := e.p.Directory.Membership(ctx, nora.ID, "nora")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.p.Directory.CreateAgent(ctx, &directory.Address{Handle: "quotes", DisplayName: "Northwind <Quotes>", Description: "Quotes merch.", WorkspaceID: ws.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.p.Directory.CreateAgent(ctx, &directory.Address{Handle: "ledger", WorkspaceID: ws.ID, Access: directory.AccessWorkspace}); err != nil {
		t.Fatal(err)
	}

	status, ctype, body := get(t, e, "/a/quotes", "text/html,application/xhtml+xml,*/*;q=0.8")
	if status != http.StatusOK || !strings.HasPrefix(ctype, "text/html") || !strings.Contains(body, "<title>Northwind &lt;Quotes&gt; (@quotes) · Lagrange</title>") || !strings.Contains(body, `content="Quotes merch."`) {
		t.Fatalf("page = %d %s %s", status, ctype, body)
	}
	for _, accept := range []string{"", "*/*", "application/json"} {
		if status, ctype, body := get(t, e, "/a/quotes", accept); status != http.StatusOK || !strings.HasPrefix(ctype, "application/json") || !strings.Contains(body, `"securitySchemes"`) {
			t.Fatalf("card with Accept %q = %d %s %s", accept, status, ctype, body)
		}
	}
	if status, ctype, _ := get(t, e, "/a/quotes/.well-known/agent-card.json", "text/html"); status != http.StatusOK || !strings.HasPrefix(ctype, "application/json") {
		t.Fatalf("well-known card for a browser = %d %s", status, ctype)
	}
	if status, ctype, body := get(t, e, "/a/nobody", "text/html"); status != http.StatusNotFound || !strings.HasPrefix(ctype, "text/html") || !strings.Contains(body, "No such address") {
		t.Fatalf("missing page = %d %s %s", status, ctype, body)
	}

	var p profile
	anon(t, e, http.MethodGet, "/api/public/addresses/quotes", "", "", http.StatusOK, &p)
	if p.Name != "Northwind <Quotes>" || p.Access != directory.AccessConnections || p.Workspace != "Nora" || p.Check.Grade == "" || p.Viewer != nil {
		t.Fatalf("anonymous profile = %+v", p)
	}
	anon(t, e, http.MethodGet, "/api/public/addresses/nobody", "", "", http.StatusNotFound, nil)

	ada := e.login(t, adminEmail, adminPassword)
	nw := e.login(t, "nora@example.com", "northwind wins")
	status_ := func(a *api, handle string) string {
		var p profile
		a.do(http.MethodGet, "/api/public/addresses/"+handle, nil, http.StatusOK, &p)
		if p.Viewer == nil || len(p.Viewer.Workspaces) != 1 {
			t.Fatalf("viewer = %+v", p.Viewer)
		}
		return p.Viewer.Workspaces[0].Status
	}
	if s := status_(ada, "quotes"); s != "" {
		t.Fatalf("Ada before asking = %q", s)
	}
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "quotes"}, http.StatusOK, nil)
	if s := status_(ada, "quotes"); s != directory.ConnPending {
		t.Fatalf("Ada after asking = %q", s)
	}
	if s := status_(nw, "quotes"); s != "in" {
		t.Fatalf("Nora = %q", s)
	}

	// The registry search lists agents others may ask for, graded by the linter.
	var reg struct {
		Agents []struct {
			URL    string
			Report struct{ Grade string }
		}
	}
	anon(t, e, http.MethodGet, "/registry/agents?all=1", "", "", http.StatusOK, &reg)
	var urls []string
	for _, a := range reg.Agents {
		urls = append(urls, a.URL)
		if a.Report.Grade == "" {
			t.Fatalf("ungraded entry %+v", a)
		}
	}
	if joined := strings.Join(urls, " "); !strings.Contains(joined, "/a/quotes") || strings.Contains(joined, "/a/ledger") {
		t.Fatalf("registry lists %v", urls)
	}
}
