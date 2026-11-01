package platform_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/three-agent-problem/lagrange/directory"
)

type connView struct {
	ID         int64
	From       int64
	FromName   string
	To         string
	Status     string
	RatePerMin int
}

type connList struct {
	Outgoing []connView
	Incoming []connView
}

// Who may start a task with an address: anonymous callers only public ones, others
// their own workspace's, or another workspace's once it accepted their connection.
func TestAccessPolicy(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	nora, err := e.p.Directory.Register(ctx, directory.Registration{Email: "nora@example.com", Name: "Nora", Handle: "nora", Password: "northwind wins", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := e.p.Directory.Membership(ctx, nora.ID, "nora")
	if err != nil {
		t.Fatal(err)
	}
	buyer := e.agent(t, "buyer")
	vault := e.agentIn(t, "vault", ws.ID)
	clerk := e.agentIn(t, "clerk", ws.ID)
	e.connect(t, "vault", vault, agentServer("vault", echo("vault", 0, nil, nil, nil)), 4)
	ada := e.login(t, adminEmail, adminPassword)
	nw := e.login(t, "nora@example.com", "northwind wins")
	nw.do(http.MethodPut, "/api/addresses/vault/access", map[string]string{"access": "connections"}, http.StatusNoContent, nil)

	call := func(token string) error {
		_, err := e.client(t, "vault", token, "").SendMessage(ctx, text("open up"))
		return err
	}
	if err := call(""); !errors.Is(err, a2a.ErrUnauthenticated) {
		t.Fatalf("anonymous call: %v", err)
	}
	if err := call(buyer); !errors.Is(err, a2a.ErrUnauthorized) || !strings.Contains(err.Error(), "/a/vault") {
		t.Fatalf("call from another workspace: %v", err)
	}
	if err := call(clerk); err != nil {
		t.Fatalf("call from the same workspace: %v", err)
	}
	nw.do(http.MethodPost, "/api/send", map[string]string{"to": "vault", "text": "hi"}, http.StatusOK, nil)
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "vault", "text": "hi"}, http.StatusForbidden, nil)

	// The card says how to call: a bearer token, for non-public addresses only.
	card, err := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/vault")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := card.SecuritySchemes["lagrange"].(a2a.HTTPAuthSecurityScheme); !ok || len(card.SecurityRequirements) != 1 {
		t.Fatalf("vault's card security = %+v / %+v", card.SecuritySchemes, card.SecurityRequirements)
	}
	if card, err = agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/buyer"); err != nil || card.SecuritySchemes != nil {
		t.Fatalf("buyer's card security = %+v, %v", card.SecuritySchemes, err)
	}

	// Ada's workspace asks; only Nora's side decides.
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "@buyer"}, http.StatusBadRequest, nil)
	var req struct{ Connection connView }
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "@vault", "note": "for quotes"}, http.StatusOK, &req)
	if req.Connection.Status != directory.ConnPending || req.Connection.FromName != "Default" {
		t.Fatalf("request = %+v", req.Connection)
	}
	var list connList
	ada.do(http.MethodGet, "/api/connections", nil, http.StatusOK, &list)
	if len(list.Outgoing) != 1 || len(list.Incoming) != 0 {
		t.Fatalf("Ada's connections = %+v", list)
	}
	nw.do(http.MethodGet, "/api/connections", nil, http.StatusOK, &list)
	if len(list.Incoming) != 1 || list.Incoming[0].To != "vault" {
		t.Fatalf("Nora's connections = %+v", list)
	}
	path := "/api/connections/" + strconv.FormatInt(req.Connection.ID, 10)
	ada.do(http.MethodPut, path, map[string]any{"status": "accepted"}, http.StatusNotFound, nil)
	ada.do(http.MethodPut, "/api/addresses/vault/access", map[string]string{"access": "public"}, http.StatusNotFound, nil)
	if err := call(buyer); !errors.Is(err, a2a.ErrUnauthorized) {
		t.Fatalf("call before acceptance: %v", err)
	}

	nw.do(http.MethodPut, path, map[string]any{"status": "accepted"}, http.StatusOK, nil)
	if err := call(buyer); err != nil {
		t.Fatalf("call over the connection: %v", err)
	}
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "vault", "text": "hi"}, http.StatusOK, nil)

	// The connection's rate: one new task a minute.
	nw.do(http.MethodPut, path, map[string]any{"status": "accepted", "ratePerMin": 1}, http.StatusOK, nil)
	if err := call(buyer); err != nil {
		t.Fatalf("call within the rate: %v", err)
	}
	if err := call(buyer); !errors.Is(err, a2a.ErrUnauthorized) || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("call over the rate: %v", err)
	}

	// Workspace-only access ignores connections; a block refuses even a public address.
	nw.do(http.MethodPut, "/api/addresses/vault/access", map[string]string{"access": "workspace"}, http.StatusNoContent, nil)
	if err := call(buyer); !errors.Is(err, a2a.ErrUnauthorized) || !strings.Contains(err.Error(), "own workspace") {
		t.Fatalf("call to a workspace-only address: %v", err)
	}
	nw.do(http.MethodPut, "/api/addresses/vault/access", map[string]string{"access": "public"}, http.StatusNoContent, nil)
	if err := call(""); err != nil {
		t.Fatalf("anonymous call to a public address: %v", err)
	}
	nw.do(http.MethodPut, path, map[string]any{"status": "blocked"}, http.StatusOK, nil)
	if err := call(buyer); !errors.Is(err, a2a.ErrUnauthorized) || !strings.Contains(err.Error(), "doesn't take tasks") {
		t.Fatalf("call from a blocked workspace: %v", err)
	}
	nw.do(http.MethodPut, "/api/addresses/vault/access", map[string]string{"access": "connections"}, http.StatusNoContent, nil)
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "vault"}, http.StatusForbidden, nil)
}

// People decide for themselves: reachable by their workspaces, until they open up.
func TestPersonAccess(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	if _, err := e.p.Directory.Register(ctx, directory.Registration{Email: "nora@example.com", Name: "Nora", Handle: "nora", Password: "northwind wins", Verified: true}); err != nil {
		t.Fatal(err)
	}
	buyer := e.agent(t, "buyer")
	ada := e.login(t, adminEmail, adminPassword)
	nw := e.login(t, "nora@example.com", "northwind wins")
	if _, err := e.client(t, "nora", buyer, "").SendMessage(ctx, text("approve?")); !errors.Is(err, a2a.ErrUnauthorized) {
		t.Fatalf("a stranger's agent asking Nora: %v", err)
	}
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "nora"}, http.StatusForbidden, nil)
	nw.do(http.MethodPut, "/api/addresses/nora/access", map[string]string{"access": "connections"}, http.StatusNoContent, nil)
	ada.do(http.MethodPut, "/api/addresses/nora/access", map[string]string{"access": "public"}, http.StatusForbidden, nil)

	var req struct{ Connection connView }
	ada.do(http.MethodPost, "/api/connections", map[string]string{"to": "nora", "note": "approvals"}, http.StatusOK, &req)
	var list connList
	nw.do(http.MethodGet, "/api/connections", nil, http.StatusOK, &list)
	if len(list.Incoming) != 1 || list.Incoming[0].To != "nora" {
		t.Fatalf("Nora's requests = %+v", list)
	}
	nw.do(http.MethodPut, "/api/connections/"+strconv.FormatInt(req.Connection.ID, 10), map[string]any{"status": "accepted"}, http.StatusOK, nil)
	res, err := e.client(t, "nora", buyer, "").SendMessage(ctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("approve?")), Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	if err != nil {
		t.Fatalf("a connected agent asking Nora: %v", err)
	}
	if _, ok := res.(*a2a.Task); !ok {
		t.Fatalf("result = %T", res)
	}
	// Withdrawn: the asking side ends it.
	ada.do(http.MethodDelete, "/api/connections/"+strconv.FormatInt(req.Connection.ID, 10), struct{}{}, http.StatusNoContent, nil)
	if _, err := e.client(t, "nora", buyer, "").SendMessage(ctx, text("again?")); !errors.Is(err, a2a.ErrUnauthorized) {
		t.Fatalf("after the connection ended: %v", err)
	}
}
