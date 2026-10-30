package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/pg/pgtest"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/relay"
	"github.com/three-agent-problem/lagrange/tunnel"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

const (
	adminEmail    = "ada@example.com"
	adminPassword = "correct horse battery staple"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	p   *platform.Platform
	url string
}

// newEnv starts a platform on dbURL (a new database if empty) behind a test server.
func newEnv(t *testing.T, dbURL string) *env {
	t.Helper()
	if dbURL == "" {
		dbURL = pgtest.URL(t)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, err := platform.New(t.Context(), platform.Config{
		DatabaseURL:      dbURL,
		PublicURL:        srv.URL,
		AdminEmail:       adminEmail,
		AdminPassword:    adminPassword,
		AdminName:        "Ada",
		AdminHandle:      "ada",
		WorkerID:         t.Name(),
		AllowPrivatePush: true,
		Queue:            pg.WorkQueueConfig{Lease: 3 * time.Second, Poll: 100 * time.Millisecond},
		EventPoll:        20 * time.Millisecond,
		Logger:           quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	mux.Handle("/", p.Handler())
	return &env{p: p, url: srv.URL}
}

// agent creates an agent address and returns a token for it.
func (e *env) agent(t *testing.T, handle string) string {
	t.Helper()
	ctx := t.Context()
	if err := e.p.Directory.CreateAgent(ctx, &directory.Address{Handle: handle}); err != nil {
		t.Fatal(err)
	}
	tok, err := e.p.Directory.MintToken(ctx, handle, "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// connect runs a tunnel for handle serving h, and waits until the mailbox has it.
func (e *env) connect(t *testing.T, handle, token string, h http.Handler, capacity int) (stop func()) {
	t.Helper()
	want := e.p.Mailbox.Stats(handle).Sessions + 1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tunnel.Run(ctx, tunnel.ClientConfig{Server: e.url, Token: token, Capacity: capacity, Handler: h, Logger: quiet})
	}()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	waitFor(t, "the tunnel to register", func() bool { return e.p.Mailbox.Stats(handle).Sessions >= want })
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type bearer struct {
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func httpClient(token string) *http.Client {
	if token == "" {
		return http.DefaultClient
	}
	return &http.Client{Transport: bearer{token}}
}

// client returns an A2A client for an address, resolved from its platform card like any
// A2A client would. protocol picks one of the card's interfaces ("" = the first).
func (e *env) client(t *testing.T, handle, token string, protocol a2a.TransportProtocol) *a2aclient.Client {
	t.Helper()
	card, err := agentcard.DefaultResolver.Resolve(t.Context(), e.url+"/a/"+handle)
	if err != nil {
		t.Fatal(err)
	}
	if protocol != "" {
		var keep []*a2a.AgentInterface
		for _, i := range card.SupportedInterfaces {
			if i.ProtocolBinding == protocol {
				keep = append(keep, i)
			}
		}
		card.SupportedInterfaces = keep
	}
	hc := httpClient(token)
	c, err := a2aclient.NewFromCard(t.Context(), card, a2aclient.WithJSONRPCTransport(hc), a2aclient.WithRESTTransport(hc))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// agentServer serves exec as an A2A agent (JSON-RPC), for a tunnel.
func agentServer(name string, exec a2asrv.AgentExecutor) http.Handler {
	card := &a2a.AgentCard{
		Name: name, Description: "A test agent.", Version: "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://"+name+".internal/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{ID: "echo", Name: "Echo", Description: "Echoes.", Tags: []string{"echo"}}},
	}
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec)))
	return mux
}

// echo answers with a task: an artifact "<name>: <text>", then COMPLETED.
func echo(name string, delay time.Duration, served *atomic.Int32, active *atomic.Int32, maxActive *atomic.Int32) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if served != nil {
				served.Add(1)
			}
			if active != nil {
				n := active.Add(1)
				defer active.Add(-1)
				for m := maxActive.Load(); n > m && !maxActive.CompareAndSwap(m, n); m = maxActive.Load() {
				}
			}
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			if delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
			if !yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart(name+": "+relay.Text(ec.Message))), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
		}
	})
}

func text(s string) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(s))}
}

func artifactText(t *testing.T, task *a2a.Task) string {
	t.Helper()
	var parts []string
	for _, a := range task.Artifacts {
		for _, p := range a.Parts {
			parts = append(parts, p.Text())
		}
	}
	return strings.Join(parts, "")
}

// asTask returns func(SendMessage's results) that fails the test unless they are a task.
func asTask(t *testing.T) func(a2a.SendMessageResult, error) *a2a.Task {
	return func(res a2a.SendMessageResult, err error) *a2a.Task {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		task, ok := res.(*a2a.Task)
		if !ok {
			t.Fatalf("got %T, want a task", res)
		}
		return task
	}
}

func waitState(t *testing.T, c *a2aclient.Client, id a2a.TaskID, want a2a.TaskState) *a2a.Task {
	t.Helper()
	var task *a2a.Task
	waitFor(t, fmt.Sprintf("task %s to be %s", id, want), func() bool {
		var err error
		task, err = c.GetTask(context.Background(), &a2a.GetTaskRequest{ID: id})
		return err == nil && task.Status.State == want
	})
	return task
}

func TestTunnelRoundTrip(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	echoToken := e.agent(t, "echo")
	callerToken := e.agent(t, "caller")
	e.connect(t, "echo", echoToken, agentServer("echo", echo("echo", 0, nil, nil, nil)), 2)

	t.Run("card", func(t *testing.T) {
		card, err := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/echo")
		if err != nil {
			t.Fatal(err)
		}
		if card.Name != "echo" || len(card.SupportedInterfaces) != 2 || card.SupportedInterfaces[0].URL != e.url+"/a/echo/rpc" {
			t.Fatalf("card = %+v", card)
		}
		if !card.Capabilities.Streaming || !card.Capabilities.PushNotifications {
			t.Fatalf("capabilities = %+v", card.Capabilities)
		}
	})

	for _, protocol := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON} {
		t.Run(string(protocol), func(t *testing.T) {
			c := e.client(t, "echo", callerToken, protocol)
			task := asTask(t)(c.SendMessage(ctx, text("hello")))
			if task.Status.State != a2a.TaskStateCompleted || artifactText(t, task) != "echo: hello" {
				t.Fatalf("task = %s %q", task.Status.State, artifactText(t, task))
			}
			link, err := e.p.Links.Get(ctx, task.ID)
			if err != nil || link == nil || link.Sender != "caller" || link.Recipient != "echo" || link.RemoteTaskID == "" || link.RemoteTaskID == task.ID {
				t.Fatalf("link = %+v, %v", link, err)
			}
			got, err := c.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID})
			if err != nil || got.Status.State != a2a.TaskStateCompleted {
				t.Fatalf("GetTask = %v, %v", got, err)
			}
		})
	}

	t.Run("streaming", func(t *testing.T) {
		c := e.client(t, "echo", callerToken, "")
		var last a2a.Event
		var n int
		for ev, err := range c.SendStreamingMessage(ctx, text("stream me")) {
			if err != nil {
				t.Fatal(err)
			}
			last, n = ev, n+1
		}
		st, ok := last.(*a2a.TaskStatusUpdateEvent)
		if !ok || st.Status.State != a2a.TaskStateCompleted || n < 3 {
			t.Fatalf("last of %d events = %#v", n, last)
		}
		entries, err := e.p.Activity.Task(ctx, string(st.TaskID))
		if err != nil || len(entries) < 3 {
			t.Fatalf("activity = %d entries, %v", len(entries), err)
		}
	})

	t.Run("bad token", func(t *testing.T) {
		c := e.client(t, "echo", "lgr_wrong", "")
		if _, err := c.SendMessage(ctx, text("hi")); err == nil {
			t.Fatal("a wrong token was accepted")
		}
	})

	t.Run("unknown address", func(t *testing.T) {
		resp, err := http.Get(e.url + "/a/nobody/.well-known/agent-card.json")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

func TestOfflineDelivery(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "later")
	c := e.client(t, "later", "", "")

	req := text("are you there?")
	req.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	task := asTask(t)(c.SendMessage(ctx, req))
	if task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("state = %s, want submitted while the agent is offline", task.Status.State)
	}
	waitFor(t, "the queued entry", func() bool {
		entries, _ := e.p.Activity.Task(ctx, string(task.ID))
		for _, en := range entries {
			if en.Kind == "queued" {
				return true
			}
		}
		return false
	})

	e.connect(t, "later", tok, agentServer("later", echo("later", 0, nil, nil, nil)), 1)
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := artifactText(t, done); got != "later: are you there?" {
		t.Fatalf("artifact = %q", got)
	}
}

func TestWorkerPool(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "pool")
	var a, b, active, maxActive atomic.Int32
	e.connect(t, "pool", tok, agentServer("pool", echo("pool", 300*time.Millisecond, &a, &active, &maxActive)), 1)
	e.connect(t, "pool", tok, agentServer("pool", echo("pool", 300*time.Millisecond, &b, &active, &maxActive)), 1)
	if s := e.p.Mailbox.Stats("pool"); s.Sessions != 2 || s.Capacity != 2 {
		t.Fatalf("stats = %+v", s)
	}

	c := e.client(t, "pool", "", "")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		wg.Go(func() {
			res, err := c.SendMessage(ctx, text(fmt.Sprint("job ", i)))
			if err != nil {
				errs <- err
				return
			}
			if task, ok := res.(*a2a.Task); !ok || task.Status.State != a2a.TaskStateCompleted {
				errs <- fmt.Errorf("job %d: %v", i, res)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if a.Load() == 0 || b.Load() == 0 || a.Load()+b.Load() != 6 {
		t.Fatalf("served %d and %d, want both workers busy and 6 in total", a.Load(), b.Load())
	}
	if maxActive.Load() > 2 {
		t.Fatalf("%d tasks ran at once, capacity is 2", maxActive.Load())
	}
}

// api is a logged-in web app session.
type api struct {
	t   *testing.T
	url string
	c   *http.Client
}

func (e *env) login(t *testing.T, email, password string) *api {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &api{t: t, url: e.url, c: &http.Client{Jar: jar}}
	a.do(http.MethodPost, "/api/login", map[string]string{"email": email, "password": password}, http.StatusOK, nil)
	return a
}

func (a *api) do(method, path string, body any, want int, out any) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, a.url+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		a.t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
}

type taskList struct {
	Tasks []struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Sender   string `json:"sender"`
		Awaiting string `json:"awaiting"`
	} `json:"tasks"`
}

// An agent asks a person; the person answers from the inbox and the task completes.
func TestHumanAnswers(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "buyer")
	c := e.client(t, "ada", tok, "")

	card, err := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/ada")
	if err != nil || card.Name != "Ada" || len(card.Skills) != 1 || card.Skills[0].ID != "ask" {
		t.Fatalf("human card = %+v, %v", card, err)
	}

	task := asTask(t)(c.SendMessage(ctx, text("Approve 5,416 EUR for 200 hoodies?")))
	if task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s", task.Status.State)
	}
	if got := task.Status.Message.Metadata[relay.AwaitingKey]; got != "ada" {
		t.Fatalf("awaiting = %v", got)
	}

	ada := e.login(t, adminEmail, adminPassword)
	var inbox taskList
	ada.do(http.MethodGet, "/api/inbox", nil, http.StatusOK, &inbox)
	if len(inbox.Tasks) != 1 || inbox.Tasks[0].ID != string(task.ID) || inbox.Tasks[0].Sender != "buyer" || inbox.Tasks[0].Awaiting != "ada" {
		t.Fatalf("inbox = %+v", inbox)
	}

	// Someone else can't answer for Ada.
	if _, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	bob := e.login(t, "bob@example.com", "hunter2hunter2")
	bob.do(http.MethodPost, "/api/tasks/"+string(task.ID)+"/answer", map[string]string{"text": "sure"}, http.StatusForbidden, nil)

	ada.do(http.MethodPost, "/api/tasks/"+string(task.ID)+"/answer", map[string]string{"text": "Approved, ship it."}, http.StatusAccepted, nil)
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := relay.Text(done.Status.Message); got != "Approved, ship it." {
		t.Fatalf("answer = %q", got)
	}
	if got := done.Status.Message.Metadata[relay.AnsweredByKey]; got != "ada" {
		t.Fatalf("answered by = %v", got)
	}

	// A rejection ends the task as rejected.
	task = asTask(t)(c.SendMessage(ctx, text("Approve 90,000 EUR?")))
	ada.do(http.MethodPost, "/api/tasks/"+string(task.ID)+"/answer", map[string]string{"text": "No.", "decision": "reject"}, http.StatusAccepted, nil)
	waitState(t, c, task.ID, a2a.TaskStateRejected)
}

// asker asks its sender to confirm, then completes with the answer.
func asker() a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if ec.StoredTask == nil {
				if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
					return
				}
				q := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("Which size?"))
				yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateInputRequired, q), nil)
				return
			}
			reply := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("Ordering size "+relay.Text(ec.Message)))
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, reply), nil)
		}
	})
}

// A person delegates to an agent from the web app, the agent asks back, and the
// person's answer reaches the agent's own task through the tunnel.
func TestPersonDelegates(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "shop")
	e.connect(t, "shop", tok, agentServer("shop", asker()), 1)
	ada := e.login(t, adminEmail, adminPassword)

	var sent struct {
		Result struct {
			ID     string `json:"id"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@shop", "text": "One hoodie please"}, http.StatusOK, &sent)
	id := sent.Result.ID
	if id == "" {
		t.Fatalf("send result = %+v", sent)
	}
	var view struct {
		Task struct {
			State  string `json:"state"`
			Sender string `json:"sender"`
		} `json:"task"`
	}
	waitFor(t, "the question", func() bool {
		ada.do(http.MethodGet, "/api/tasks/"+id, nil, http.StatusOK, &view)
		return view.Task.State == string(a2a.TaskStateInputRequired)
	})
	if view.Task.Sender != "ada" {
		t.Fatalf("sender = %q", view.Task.Sender)
	}
	var inbox taskList
	ada.do(http.MethodGet, "/api/inbox", nil, http.StatusOK, &inbox)
	if len(inbox.Tasks) != 1 || inbox.Tasks[0].ID != id {
		t.Fatalf("inbox = %+v", inbox)
	}

	ada.do(http.MethodPost, "/api/tasks/"+id+"/answer", map[string]string{"text": "L"}, http.StatusAccepted, nil)
	var full struct {
		Task struct {
			State string    `json:"state"`
			Task  *a2a.Task `json:"task"`
		} `json:"task"`
	}
	waitFor(t, "the order", func() bool {
		ada.do(http.MethodGet, "/api/tasks/"+id, nil, http.StatusOK, &full)
		return full.Task.State == string(a2a.TaskStateCompleted)
	})
	if got := relay.Text(full.Task.Task.Status.Message); got != "Ordering size L" {
		t.Fatalf("reply = %q", got)
	}
}

// delegator calls @echo through the platform with its own token, propagating the
// platform's task header, and completes with echo's answer.
func delegator(e *env, token string) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			parent, _ := ec.ServiceParams.Get(relay.HeaderTask)
			card, err := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/echo")
			if err != nil {
				yield(nil, err)
				return
			}
			hc := httpClient(token)
			c, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(hc), a2aclient.WithCallInterceptors(header{relay.HeaderTask, strings.Join(parent, "")}))
			if err != nil {
				yield(nil, err)
				return
			}
			res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, ec.Message.Parts...)})
			if err != nil {
				yield(nil, err)
				return
			}
			child := res.(*a2a.Task)
			if !yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart(artifactTextOf(child))), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
		}
	})
}

func artifactTextOf(task *a2a.Task) string {
	var b strings.Builder
	for _, a := range task.Artifacts {
		for _, p := range a.Parts {
			b.WriteString(p.Text())
		}
	}
	return b.String()
}

type header struct{ k, v string }

func (h header) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	req.ServiceParams[h.k] = []string{h.v}
	return ctx, nil, nil
}

func (header) After(context.Context, *a2aclient.Response) error { return nil }

func TestDelegationTree(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	echoToken := e.agent(t, "echo")
	buyerToken := e.agent(t, "buyer")
	e.connect(t, "echo", echoToken, agentServer("echo", echo("echo", 0, nil, nil, nil)), 2)
	e.connect(t, "buyer", buyerToken, agentServer("buyer", delegator(e, buyerToken)), 2)

	c := e.client(t, "buyer", "", "")
	task := asTask(t)(c.SendMessage(ctx, text("quote 200 hoodies")))
	if task.Status.State != a2a.TaskStateCompleted || artifactText(t, task) != "echo: quote 200 hoodies" {
		t.Fatalf("task = %s %q", task.Status.State, artifactText(t, task))
	}
	children, err := e.p.Links.Children(ctx, task.ID)
	if err != nil || len(children) != 1 || children[0].Sender != "buyer" || children[0].Recipient != "echo" {
		t.Fatalf("children = %+v, %v", children, err)
	}

	// A forged parent header from someone who isn't working on the task is ignored.
	other := e.agent(t, "mallory")
	hc := httpClient(other)
	card, _ := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/echo")
	mc, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(hc), a2aclient.WithCallInterceptors(header{relay.HeaderTask, string(task.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	forged := asTask(t)(mc.SendMessage(ctx, text("me too")))
	if link, _ := e.p.Links.Get(ctx, forged.ID); link == nil || link.ParentTaskID != "" {
		t.Fatalf("forged link = %+v", link)
	}
}

// A task queued on one server completes on the next one, after a restart.
func TestRestartRecovery(t *testing.T) {
	dbURL := pgtest.URL(t)
	first := newEnv(t, dbURL)
	tok := first.agent(t, "worker")
	c := first.client(t, "worker", "", "")
	req := text("survive a restart")
	req.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	task := asTask(t)(c.SendMessage(t.Context(), req))
	first.p.Close()

	second := newEnv(t, dbURL)
	second.connect(t, "worker", tok, agentServer("worker", echo("worker", 0, nil, nil, nil)), 1)
	done := waitState(t, second.client(t, "worker", "", ""), task.ID, a2a.TaskStateCompleted)
	if got := artifactText(t, done); got != "worker: survive a restart" {
		t.Fatalf("artifact = %q", got)
	}
}

// The web app's view of the platform: addresses, the graph snapshot and live events.
func TestAPI(t *testing.T) {
	e := newEnv(t, "")
	ada := e.login(t, adminEmail, adminPassword)
	anon := &api{t: t, url: e.url, c: http.DefaultClient}
	anon.do(http.MethodGet, "/api/me", nil, http.StatusUnauthorized, nil)
	anon.do(http.MethodPost, "/api/login", map[string]string{"email": adminEmail, "password": "wrong"}, http.StatusUnauthorized, nil)
	// State-changing calls must be JSON (CSRF): a form post is refused.
	req, _ := http.NewRequest(http.MethodPost, e.url+"/api/send", strings.NewReader("to=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := ada.c.Do(req); err != nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post = %v, %v", resp, err)
	}

	var created struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	ada.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "helper", "displayName": "Helper"}, http.StatusCreated, &created)
	if !strings.HasPrefix(created.Token, "lgr_") || !strings.Contains(created.Command, created.Token) {
		t.Fatalf("created = %+v", created)
	}
	ada.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "helper"}, http.StatusConflict, nil)
	ada.do(http.MethodPost, "/api/addresses", map[string]string{"handle": "No Spaces"}, http.StatusBadRequest, nil)

	// Live events: subscribe, then connect the agent and see it come online.
	sreq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.url+"/api/events", nil)
	resp, err := ada.c.Do(sreq)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %v, %v", resp, err)
	}
	defer resp.Body.Close()
	e.connect(t, "helper", created.Token, agentServer("helper", echo("helper", 0, nil, nil, nil)), 3)
	presence := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 1024)
		for {
			n, err := resp.Body.Read(chunk)
			buf = append(buf, chunk[:n]...)
			if strings.Contains(string(buf), `"kind":"presence"`) {
				presence <- string(buf)
				return
			}
			if err != nil {
				close(presence)
				return
			}
		}
	}()
	select {
	case got, ok := <-presence:
		if !ok || !strings.Contains(got, "event: activity") {
			t.Fatalf("stream = %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no presence event")
	}

	var addrs struct {
		Addresses []struct {
			Handle   string `json:"handle"`
			Kind     string `json:"kind"`
			Online   bool   `json:"online"`
			Sessions []struct {
				Capacity int `json:"capacity"`
			} `json:"sessions"`
			Card struct {
				Name string `json:"name"`
			} `json:"card"`
		} `json:"addresses"`
	}
	ada.do(http.MethodGet, "/api/addresses", nil, http.StatusOK, &addrs)
	found := false
	for _, a := range addrs.Addresses {
		if a.Handle == "helper" {
			found = true
			if !a.Online || len(a.Sessions) != 1 || a.Sessions[0].Capacity != 3 || a.Card.Name != "Helper" {
				t.Fatalf("helper = %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("addresses = %+v", addrs)
	}

	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "helper", "text": "ping"}, http.StatusOK, nil)
	var graph struct {
		Seq   int64 `json:"seq"`
		Tasks []struct {
			Sender    string `json:"sender"`
			Recipient string `json:"recipient"`
		} `json:"tasks"`
	}
	waitFor(t, "the task in the graph", func() bool {
		ada.do(http.MethodGet, "/api/graph", nil, http.StatusOK, &graph)
		return len(graph.Tasks) == 1
	})
	if graph.Seq == 0 || graph.Tasks[0].Sender != "ada" || graph.Tasks[0].Recipient != "helper" {
		t.Fatalf("graph = %+v", graph)
	}

	var reg struct {
		Agents []struct {
			URL string `json:"url"`
		} `json:"agents"`
	}
	anon.do(http.MethodGet, "/registry/agents?skill=echo", nil, http.StatusOK, &reg)
	if len(reg.Agents) != 1 || reg.Agents[0].URL != e.url+"/a/helper" {
		t.Fatalf("registry = %+v", reg)
	}
}

// replier answers with a message and no task.
func replier() a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("pong: "+relay.Text(ec.Message))), nil)
		}
	})
}

func TestMessageReply(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "pinger")
	e.connect(t, "pinger", tok, agentServer("pinger", replier()), 2)
	c := e.client(t, "pinger", "", "")
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := c.SendMessage(cctx, text("ping"))
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(*a2a.Message)
	if !ok || relay.Text(msg) != "pong: ping" {
		t.Fatalf("result = %#v", res)
	}
}

// The agent's tunnel drops mid-task: the task waits for the agent to come back and is
// sent again, since the restarted agent doesn't know it.
func TestAgentDisconnects(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "flaky")
	var served atomic.Int32
	stop := e.connect(t, "flaky", tok, agentServer("flaky", echo("flaky", 5*time.Second, &served, nil, nil)), 1)

	c := e.client(t, "flaky", "", "")
	req := text("hold on")
	req.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	task := asTask(t)(c.SendMessage(ctx, req))
	waitFor(t, "the agent to start", func() bool { return served.Load() == 1 })
	stop()
	waitFor(t, "the agent to go offline", func() bool { return e.p.Mailbox.Stats("flaky").Sessions == 0 })

	e.connect(t, "flaky", tok, agentServer("flaky", echo("flaky", 0, &served, nil, nil)), 1)
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := artifactText(t, done); got != "flaky: hold on" || served.Load() != 2 {
		t.Fatalf("artifact = %q, served %d times", got, served.Load())
	}
}

// Canceling a task that waits for input settles it as CANCELED, however the cancel
// races the task's snapshot.
func TestCancelWaiting(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	tok := e.agent(t, "buyer")
	c := e.client(t, "ada", tok, "")
	for i := range 5 {
		task := asTask(t)(c.SendMessage(ctx, text(fmt.Sprintf("question %d", i))))
		got, err := c.CancelTask(ctx, &a2a.CancelTaskRequest{ID: task.ID})
		if err != nil || got.Status.State != a2a.TaskStateCanceled {
			t.Fatalf("cancel %d = %v, %v", i, got, err)
		}
	}
}
