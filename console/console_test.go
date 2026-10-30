package console_test

import (
	"context"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/three-agent-problem/lagrange/console"
	"github.com/three-agent-problem/lagrange/links"
	"github.com/three-agent-problem/lagrange/observer"
)

const pushToken = "test-token"

var approvalSchema = &jsonschema.Schema{
	Type: "object",
	Properties: map[string]*jsonschema.Schema{
		"approved": {Type: "boolean"},
		"note":     {Type: "string"},
	},
	Required: []string{"approved"},
}

// approvalAgent asks for an approval, then completes with who approved it.
type approvalAgent struct{}

func (approvalAgent) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if ec.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			req := &structuredinput.Request{ID: "approval-1", Schema: approvalSchema}
			yield(structuredinput.NewRequestEvent(ec, req, a2a.NewTextPart("Approve the 5,400 EUR order?")))
			return
		}
		req, err := structuredinput.PendingRequest(ec.StoredTask)
		if err != nil || req == nil {
			yield(nil, fmt.Errorf("not waiting for input: %v", err))
			return
		}
		var p structuredinput.Processor
		result, err := p.Process(ctx, req, ec.Message)
		if err != nil {
			yield(nil, err)
			return
		}
		var answer struct {
			Approved bool   `json:"approved"`
			Note     string `json:"note"`
		}
		if err := result.Decode(&answer); err != nil {
			yield(nil, err)
			return
		}
		by, _ := ec.Message.Metadata[console.AnsweredByKey].(string)
		text := fmt.Sprintf("approved=%t by %s (%s)", answer.Approved, by, answer.Note)
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))), nil)
	}
}

func (approvalAgent) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}

type fixture struct {
	console    *console.Console
	consoleURL string
	agentURL   string
	client     *a2aclient.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	consoleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.console.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(consoleSrv.Close)
	f.consoleURL = consoleSrv.URL
	f.console = console.New(console.Config{PublicURL: consoleSrv.URL, Token: pushToken})

	mux := http.NewServeMux()
	agentSrv := httptest.NewServer(mux)
	t.Cleanup(agentSrv.Close)
	f.agentURL = agentSrv.URL

	card := &a2a.AgentCard{
		Name:                "Approval Agent",
		Description:         "Asks for approval",
		Version:             "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(agentSrv.URL+"/invoke", a2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Capabilities: a2a.AgentCapabilities{
			PushNotifications: true,
			Extensions:        []a2a.AgentExtension{structuredinput.NewExtension(structuredinput.Draft202012)},
		},
		Skills: []a2a.AgentSkill{{ID: "approve", Name: "Approve", Description: "Asks for approval", Tags: []string{"approval"}}},
	}
	store := observer.Wrap(push.NewInMemoryStore(), f.console.PushURL(agentSrv.URL), f.console.Token())
	handler := a2asrv.NewHandler(approvalAgent{}, a2asrv.WithPushNotifications(store, push.NewHTTPPushSender(&push.HTTPSenderConfig{AllowPrivateNetworks: true})))
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(handler))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))

	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	f.client = client
	return f
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInboxApprovalResumesTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("order"))})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok || task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("SendMessage() = %#v, want a task in INPUT_REQUIRED", result)
	}

	waitFor(t, "the console to see INPUT_REQUIRED", func() bool {
		ct, ok := f.console.Task(task.ID)
		return ok && ct.State == a2a.TaskStateInputRequired
	})

	inbox := get(t, f.consoleURL+"/inbox")
	for _, want := range []string{"Approve the 5,400 EUR order?", `name="approved" value="true"`} {
		if !strings.Contains(inbox, want) {
			t.Errorf("inbox page does not contain %q", want)
		}
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.PostForm(f.consoleURL+"/inbox/"+string(task.ID), url.Values{
		"approved": {"true"}, "note": {"within budget"}, "_answered_by": {"alice@acme.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/tasks/") {
		t.Fatalf("POST answer = %s, Location %q; want a redirect to the task", resp.Status, loc)
	}

	waitFor(t, "the task to complete", func() bool {
		ct, ok := f.console.Task(task.ID)
		return ok && ct.State == a2a.TaskStateCompleted
	})
	ct, _ := f.console.Task(task.ID)
	last := ct.Events[len(ct.Events)-1]
	if last.Kind != "status" || last.State != a2a.TaskStateCompleted {
		t.Errorf("last event = %+v, want the completion after the inbox answer", last)
	}
	got := last.Summary
	if want := "approved=true by alice@acme.example (within budget)"; got != want {
		t.Errorf("final status = %q, want %q", got, want)
	}
	if ct.TimeIn[a2a.TaskStateInputRequired] <= 0 {
		t.Errorf("TimeIn[INPUT_REQUIRED] = %v, want > 0", ct.TimeIn[a2a.TaskStateInputRequired])
	}
	var sawInbox bool
	for _, e := range ct.Events {
		sawInbox = sawInbox || e.Kind == "inbox"
	}
	if !sawInbox {
		t.Errorf("timeline has no inbox event: %+v", ct.Events)
	}
	waitFor(t, "the card name to resolve", func() bool {
		ct, _ := f.console.Task(task.ID)
		return ct.AgentName == "Approval Agent"
	})
}

func TestPushRequiresToken(t *testing.T) {
	f := newFixture(t)
	resp, err := http.Post(f.console.PushURL(f.agentURL), "application/json", strings.NewReader(`{"task":{"id":"x","contextId":"c","status":{"state":"TASK_STATE_WORKING"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("push without token = %s, want 401", resp.Status)
	}
}

func TestLinksBuildTree(t *testing.T) {
	c := console.New(console.Config{PublicURL: "http://console.invalid"})
	now := time.Now()
	parent := a2a.TaskInfo{TaskID: "parent", ContextID: "ctx"}
	ev := a2a.NewStatusUpdateEvent(parent, a2a.TaskStateWorking, nil)
	links.Set(ev, links.Child{AgentURL: "http://vendor.invalid", TaskID: "child", Skill: "order"})
	c.Record("http://buyer.invalid", ev, now)
	c.Record("http://vendor.invalid", a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "child", ContextID: "v"}, a2a.TaskStateCompleted, nil), now.Add(time.Second))

	p, ok := c.Task("parent")
	if !ok || len(p.Children) != 1 || p.Children[0] != "child" {
		t.Fatalf("parent = %+v, want one child", p)
	}
	child, _ := c.Task("child")
	if child.ParentID != "parent" || child.State != a2a.TaskStateCompleted || child.AgentURL != "http://vendor.invalid" {
		t.Errorf("child = %+v", child)
	}
}

func get(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
