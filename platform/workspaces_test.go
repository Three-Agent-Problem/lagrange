package platform_test

import (
	"bufio"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/platform"
)

// Two workspaces: Ada's buyer calls Northwind's agent, which asks its own stock agent.
// Each side sees the call between them and only its own internal work.
func TestWorkspaceIsolation(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	nora, err := e.p.Directory.Register(ctx, directory.Registration{Email: "nora@example.com", Name: "Nora", Handle: "nora", Password: "northwind wins", Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := e.p.Directory.Membership(ctx, nora.ID, "nora")
	if err != nil || ws.Role != directory.RoleOwner {
		t.Fatalf("nora's own workspace = %+v, %v", ws, err)
	}
	buyer := e.agent(t, "buyer")
	northwind := e.agentIn(t, "northwind", ws.ID)
	stock := e.agentIn(t, "stock", ws.ID)
	e.connect(t, "buyer", buyer, agentServer("buyer", delegatorTo(e, buyer, "northwind")), 1)
	e.connect(t, "northwind", northwind, agentServer("northwind", delegatorTo(e, northwind, "stock")), 1)
	e.connect(t, "stock", stock, agentServer("stock", echo("stock", 0, nil, nil, nil)), 1)

	ada := e.login(t, adminEmail, adminPassword)
	nw := e.login(t, "nora@example.com", "northwind wins")
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@buyer", "text": "200 hoodies"}, http.StatusOK, nil)

	var runs runList
	// The run ends before every link of Northwind's internal call may be recorded, so
	// wait for the whole run, not just its end.
	waitFor(t, "Ada's run to end with Northwind's call hidden", func() bool {
		ada.do(http.MethodGet, "/api/runs?status=done", nil, http.StatusOK, &runs)
		return len(runs.Runs) == 1 && runs.Runs[0].Hidden == 1
	})
	if r := runs.Runs[0]; r.Tasks != 2 || r.Hidden != 1 || slices.Contains(r.Participants, "stock") {
		t.Fatalf("Ada's run = %+v", r)
	}
	nw.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 {
		t.Fatalf("Nora's runs = %+v", runs.Runs)
	}
	if r := runs.Runs[0]; r.Tasks != 2 || r.Hidden != 1 || slices.Contains(r.Participants, "ada") || r.Title == "" {
		t.Fatalf("Nora's run = %+v", r)
	}
	var detail platform.RunDetail
	nw.do(http.MethodGet, "/api/runs/"+runs.Runs[0].ID, nil, http.StatusOK, &detail)
	for _, x := range detail.Tasks {
		if x.Sender == "ada" || x.Recipient == "ada" {
			t.Fatalf("Nora sees Ada's task %+v", x)
		}
	}
	var stockTask string
	for _, x := range detail.Tasks {
		if x.Recipient == "stock" {
			stockTask = string(x.ID)
		}
	}
	if stockTask == "" {
		t.Fatalf("Nora's run lacks the stock task: %+v", detail.Tasks)
	}
	ada.do(http.MethodGet, "/api/tasks/"+stockTask, nil, http.StatusNotFound, nil)
	ada.do(http.MethodGet, "/api/runs/"+stockTask, nil, http.StatusNotFound, nil)

	// The graph: own addresses, plus the other side of calls, marked external.
	var graph struct {
		Addresses []struct {
			Handle   string
			External bool
		}
	}
	ada.do(http.MethodGet, "/api/graph", nil, http.StatusOK, &graph)
	seen := map[string]bool{}
	for _, a := range graph.Addresses {
		seen[a.Handle] = a.External
	}
	if ext, ok := seen["northwind"]; !ok || !ext || seen["buyer"] {
		t.Fatalf("Ada's graph = %+v", graph.Addresses)
	}
	if _, ok := seen["stock"]; ok {
		t.Fatalf("Ada's graph has stock: %+v", graph.Addresses)
	}
	if _, ok := seen["nora"]; ok {
		t.Fatalf("Ada's graph has nora: %+v", graph.Addresses)
	}

	var addrs struct{ Addresses []directory.Address }
	ada.do(http.MethodGet, "/api/addresses", nil, http.StatusOK, &addrs)
	for _, a := range addrs.Addresses {
		if a.Handle == "northwind" || a.Handle == "stock" || a.Handle == "nora" {
			t.Fatalf("Ada's addresses have @%s", a.Handle)
		}
	}
	ada.do(http.MethodDelete, "/api/addresses/stock", struct{}{}, http.StatusNotFound, nil)

	var convs struct{ Conversations []platform.Conversation }
	ada.do(http.MethodGet, "/api/conversations", nil, http.StatusOK, &convs)
	for _, c := range convs.Conversations {
		if c.A == "northwind" && c.B == "stock" {
			t.Fatalf("Ada sees northwind|stock")
		}
	}
	ada.do(http.MethodGet, "/api/conversations/northwind/stock", nil, http.StatusNotFound, nil)

	var log struct {
		Entries []struct{ Sender, Recipient string }
	}
	ada.do(http.MethodGet, "/api/activity", nil, http.StatusOK, &log)
	for _, en := range log.Entries {
		if en.Sender == "stock" || en.Recipient == "stock" {
			t.Fatalf("Ada's activity has %+v", en)
		}
	}
	if got := stream(t, ada, `"recipient":"northwind"`); strings.Contains(got, `"stock"`) {
		t.Fatalf("Ada's stream has Northwind's internal work:\n%s", got)
	}
	if got := stream(t, nw, `"recipient":"stock"`); strings.Contains(got, `"ada"`) {
		t.Fatalf("Nora's stream has Ada's work:\n%s", got)
	}

	// Nora isn't in the default workspace.
	nw.in(directory.DefaultWorkspace).do(http.MethodGet, "/api/runs", nil, http.StatusForbidden, nil)
}

// stream reads the event stream from the start until it has seen want (which it must),
// and returns it.
func stream(t *testing.T, a *api, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.url+"/api/events?since=0", nil)
	resp, err := a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
		if strings.Contains(sc.Text(), want) {
			// Read on briefly for the rest of the backlog.
			time.AfterFunc(300*time.Millisecond, cancel)
		}
	}
	if !strings.Contains(b.String(), want) {
		t.Fatalf("the stream lacks %s:\n%s", want, b.String())
	}
	return b.String()
}

// Roles: viewers look, members act, admins invite, owners manage members, and a
// workspace keeps an owner.
func TestWorkspaceRoles(t *testing.T) {
	e := newEnv(t, "")
	ada := e.login(t, adminEmail, adminPassword)
	e.agent(t, "shop")

	var inv platform.InviteView
	ada.do(http.MethodPost, "/api/invites", map[string]string{"email": "vic@example.com", "role": "viewer"}, http.StatusCreated, &inv)
	if inv.Role != "viewer" || inv.Workspace != "Default" {
		t.Fatalf("invite = %+v", inv)
	}
	token := authLink(t, inv.Link, "invite")
	vic := e.guest(t)
	vic.do(http.MethodPost, "/api/invites/"+token+"/accept", map[string]string{"name": "Vic", "handle": "vic", "password": "viewing only"}, http.StatusOK, nil)

	vic.do(http.MethodGet, "/api/runs", nil, http.StatusOK, nil)
	vic.do(http.MethodPost, "/api/send", map[string]string{"to": "@shop", "text": "hi"}, http.StatusForbidden, nil)
	vic.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "mine"}, http.StatusForbidden, nil)
	vic.do(http.MethodPost, "/api/invites", map[string]string{"email": "x@example.com"}, http.StatusForbidden, nil)

	var members struct {
		Members []struct {
			ID     int64
			Handle string
			Role   string
		}
	}
	ada.do(http.MethodGet, "/api/workspace/members", nil, http.StatusOK, &members)
	ids := map[string]int64{}
	for _, m := range members.Members {
		ids[m.Handle] = m.ID
	}
	if members.Members[0].Handle != "ada" || members.Members[0].Role != "owner" || ids["vic"] == 0 {
		t.Fatalf("members = %+v", members.Members)
	}
	// The last owner can't step down or leave.
	ada.do(http.MethodPut, "/api/workspace/members/"+itoa(ids["ada"]), map[string]string{"role": "admin"}, http.StatusConflict, nil)
	ada.do(http.MethodDelete, "/api/workspace/members/"+itoa(ids["ada"]), struct{}{}, http.StatusConflict, nil)
	vic.do(http.MethodPut, "/api/workspace/members/"+itoa(ids["vic"]), map[string]string{"role": "owner"}, http.StatusForbidden, nil)

	ada.do(http.MethodPut, "/api/workspace/members/"+itoa(ids["vic"]), map[string]string{"role": "member"}, http.StatusNoContent, nil)
	vic.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "vics-agent"}, http.StatusCreated, nil)

	// An existing account joins another workspace by logging in and opening the link.
	var made struct{ Workspace directory.Workspace }
	vic.do(http.MethodPost, "/api/workspaces", map[string]string{"name": "Vic's lab"}, http.StatusCreated, &made)
	if made.Workspace.Slug != "vic-s-lab" || made.Workspace.Role != "owner" {
		t.Fatalf("workspace = %+v", made.Workspace)
	}
	lab := vic.in(made.Workspace.Slug)
	lab.do(http.MethodPost, "/api/invites", map[string]string{"email": adminEmail, "role": "admin"}, http.StatusCreated, &inv)
	if !inv.Existing {
		t.Fatalf("invite = %+v", inv)
	}
	token = authLink(t, inv.Link, "invite")
	e.guest(t).do(http.MethodPost, "/api/invites/"+token+"/accept", map[string]string{}, http.StatusConflict, nil)
	ada.do(http.MethodPost, "/api/invites/"+token+"/accept", map[string]string{}, http.StatusOK, nil)
	var list struct{ Workspaces []directory.Workspace }
	ada.do(http.MethodGet, "/api/workspaces", nil, http.StatusOK, &list)
	if len(list.Workspaces) != 2 || list.Workspaces[1].Slug != made.Workspace.Slug || list.Workspaces[1].Role != "admin" {
		t.Fatalf("Ada's workspaces = %+v", list.Workspaces)
	}
	// The lab doesn't see the default workspace's agents.
	var addrs struct{ Addresses []directory.Address }
	ada.in(made.Workspace.Slug).do(http.MethodGet, "/api/addresses", nil, http.StatusOK, &addrs)
	for _, a := range addrs.Addresses {
		if a.Handle == "shop" || a.Handle == "vics-agent" {
			t.Fatalf("the lab lists @%s", a.Handle)
		}
	}
	// Leaving.
	ada.in(made.Workspace.Slug).do(http.MethodDelete, "/api/workspace/members/"+itoa(ids["ada"]), struct{}{}, http.StatusNoContent, nil)
	ada.in(made.Workspace.Slug).do(http.MethodGet, "/api/runs", nil, http.StatusForbidden, nil)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
