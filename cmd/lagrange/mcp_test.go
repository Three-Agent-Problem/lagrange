package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/mcpserver"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/tunnel"
)

// keyFor makes an API key for a person in the default workspace.
func keyFor(t *testing.T, p *platform.Platform, handle, role string) string {
	t.Helper()
	ctx := t.Context()
	u, err := p.Directory.UserByHandle(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := p.Directory.Default(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := p.Directory.CreateAPIKey(ctx, u.ID, ws.ID, "test", role)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// mcpSession connects an in-memory MCP client to a server acting with key.
func mcpSession(t *testing.T, server, key string) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	srv := mcpserver.New(client.New(server, key), "test", mcpserver.Options{Poll: 50 * time.Millisecond})
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call calls a tool and decodes its structured result into out. It returns the result's
// text when the tool reported an error.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		return res.Content[0].(*mcp.TextContent).Text
	}
	if out != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, raw)
		}
	}
	return ""
}

func TestMCP(t *testing.T) {
	p, server := testServer(t)
	ctx := t.Context()
	if _, err := p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	adaKey := keyFor(t, p, "ada", directory.RoleMember)
	ada := client.New(server, adaKey)
	tok, err := ada.CreateAgent(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(tctx, tunnel.ClientConfig{Server: server, Token: tok, Handler: echoHandler("Echo")})
	}()
	for deadline := time.Now().Add(15 * time.Second); p.Mailbox.Stats("echo").Sessions == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the echo tunnel didn't connect")
		}
	}

	cs := mcpSession(t, server, adaKey)
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "ask_human") || !strings.Contains(got, "search_runs") || len(names) != 9 {
		t.Fatalf("tools = %s", got)
	}

	var agents struct {
		Addresses []struct {
			Handle, Kind string
			Online       bool
			Skills       []string
		}
	}
	call(t, cs, "list_agents", map[string]any{"kind": "agent"}, &agents)
	if len(agents.Addresses) != 1 || agents.Addresses[0].Handle != "@echo" || !agents.Addresses[0].Online || agents.Addresses[0].Skills[0] != "Echo" {
		t.Fatalf("agents = %+v", agents)
	}
	var card struct{ Name string }
	call(t, cs, "get_agent_card", map[string]any{"handle": "@echo"}, &card)
	if card.Name != "Echo" {
		t.Fatalf("card = %+v", card)
	}

	// send_task waits for the agent's answer.
	var sent mcpserver.TaskResult
	if msg := call(t, cs, "send_task", map[string]any{"to": "@echo", "text": "hello from MCP"}, &sent); msg != "" {
		t.Fatal(msg)
	}
	if !sent.Done || sent.State != "completed" || !strings.Contains(sent.Text, "You said: hello from MCP") {
		t.Fatalf("send_task = %+v", sent)
	}
	if msg := call(t, cs, "ask_human", map[string]any{"to": "@echo", "question": "?"}, nil); !strings.Contains(msg, "use send_task") {
		t.Fatalf("ask_human to an agent = %q", msg)
	}

	// ask_human waits while Bob answers from his own MCP client.
	bob := mcpSession(t, server, keyFor(t, p, "bob", directory.RoleMember))
	asked := make(chan mcpserver.TaskResult, 1)
	go func() {
		var r mcpserver.TaskResult
		if msg := call(t, cs, "ask_human", map[string]any{"to": "@bob", "question": "Approve 200 hoodies?", "waitSeconds": 20}, &r); msg != "" {
			t.Error(msg)
		}
		asked <- r
	}()
	var waiting struct {
		Waiting []struct {
			TaskID, Question, WaitingOn string
			ForYou                      bool
		}
	}
	for deadline := time.Now().Add(10 * time.Second); len(waiting.Waiting) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the question never reached Bob")
		}
		call(t, bob, "list_waiting", nil, &waiting)
	}
	w := waiting.Waiting[0]
	if !w.ForYou || w.WaitingOn != "@bob" || !strings.Contains(w.Question, "Approve 200 hoodies?") {
		t.Fatalf("waiting = %+v", waiting)
	}
	var ans struct{ Answered bool }
	if msg := call(t, bob, "answer", map[string]any{"taskId": w.TaskID, "text": "Yes, go ahead.", "decision": "approve"}, &ans); msg != "" || !ans.Answered {
		t.Fatalf("answer = %q %+v", msg, ans)
	}
	got := <-asked
	if !got.Done || got.State != "completed" || got.AnsweredBy != "bob" || !strings.Contains(got.Text, "Yes, go ahead.") {
		t.Fatalf("ask_human = %+v", got)
	}

	// A short wait returns the task ID to check on later.
	var pending mcpserver.TaskResult
	call(t, cs, "ask_human", map[string]any{"to": "@bob", "question": "Lunch?", "waitSeconds": 0}, &pending)
	if pending.Done || pending.TaskID == "" || pending.WaitingOn != "bob" || !strings.Contains(pending.Note, "get_task") {
		t.Fatalf("pending = %+v", pending)
	}
	var again mcpserver.TaskResult
	call(t, cs, "get_task", map[string]any{"taskId": pending.TaskID}, &again)
	if again.State != "input-required" {
		t.Fatalf("get_task = %+v", again)
	}

	// Runs show both.
	var runs struct {
		Runs []struct{ RunID, Title, State string }
	}
	call(t, cs, "search_runs", map[string]any{"q": "hoodies"}, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].State != "completed" {
		t.Fatalf("runs = %+v", runs)
	}
	var run struct {
		Tasks []struct{ From, To, Answer, AnsweredBy string }
	}
	call(t, cs, "get_run", map[string]any{"runId": runs.Runs[0].RunID}, &run)
	if len(run.Tasks) != 1 || run.Tasks[0].From != "@ada" || run.Tasks[0].To != "@bob" || run.Tasks[0].AnsweredBy != "bob" {
		t.Fatalf("run = %+v", run)
	}

	// A viewer key can look, not act.
	viewer := mcpSession(t, server, keyFor(t, p, "bob", directory.RoleViewer))
	call(t, viewer, "list_waiting", nil, nil)
	if msg := call(t, viewer, "answer", map[string]any{"taskId": pending.TaskID, "text": "no"}, nil); msg == "" {
		t.Fatal("a viewer key answered")
	}
	if msg := call(t, viewer, "send_task", map[string]any{"to": "@echo", "text": "hi"}, nil); !strings.Contains(msg, "read-only") {
		t.Fatalf("viewer send_task = %q", msg)
	}
}
