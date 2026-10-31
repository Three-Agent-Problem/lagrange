package platform_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/platform"
)

// Anyone on the team can read what two agents said to each other, and a run says how it ended.
func TestConversationsBetweenAgents(t *testing.T) {
	e := newEnv(t, "")
	echoToken := e.agent(t, "echo")
	buyerToken := e.agent(t, "buyer")
	e.connect(t, "echo", echoToken, agentServer("echo", echo("echo", 0, nil, nil, nil)), 2)
	e.connect(t, "buyer", buyerToken, agentServer("buyer", delegator(e, buyerToken)), 2)
	pinger := e.agent(t, "pinger")
	e.connect(t, "pinger", pinger, agentServer("pinger", replier()), 1)
	ada := e.login(t, adminEmail, adminPassword)

	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@buyer", "text": "quote 200 hoodies"}, http.StatusOK, nil)
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@pinger", "text": "ping"}, http.StatusOK, nil)
	var runs runList
	waitFor(t, "both runs to end", func() bool {
		ada.do(http.MethodGet, "/api/runs?status=done", nil, http.StatusOK, &runs)
		return len(runs.Runs) == 2
	})
	for _, r := range runs.Runs {
		if r.Outcome == "" {
			t.Fatalf("run %s (%s) has no outcome", r.ID, r.Title)
		}
	}

	var list struct{ Conversations []platform.Conversation }
	ada.do(http.MethodGet, "/api/conversations", nil, http.StatusOK, &list)
	got := map[string]platform.Conversation{}
	for _, c := range list.Conversations {
		got[c.A+"|"+c.B] = c
	}
	if c := got["buyer|echo"]; c.Tasks != 1 || c.Last == "" {
		t.Fatalf("buyer|echo = %+v (all: %+v)", c, list.Conversations)
	}
	if c := got["ada|pinger"]; c.Messages != 1 || c.LastFrom != "pinger" {
		t.Fatalf("ada|pinger = %+v", c)
	}

	// Bob isn't in either conversation and still reads it.
	if _, err := e.p.Directory.CreateUser(t.Context(), "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	bob := e.login(t, "bob@example.com", "hunter2hunter2")
	var thread struct {
		Tasks    []platform.TaskView
		Messages []struct{ Summary string }
	}
	bob.do(http.MethodGet, "/api/conversations/echo/buyer", nil, http.StatusOK, &thread)
	if len(thread.Tasks) != 1 || thread.Tasks[0].Sender != "buyer" || thread.Tasks[0].State != string(a2a.TaskStateCompleted) ||
		thread.Tasks[0].Task == nil || !strings.Contains(thread.Tasks[0].Status, "echo: quote 200 hoodies") {
		t.Fatalf("thread = %+v", thread.Tasks)
	}
	bob.do(http.MethodGet, "/api/conversations/ada/pinger", nil, http.StatusOK, &thread)
	if len(thread.Messages) != 1 || thread.Messages[0].Summary != "ping" {
		t.Fatalf("messages = %+v", thread.Messages)
	}
}
