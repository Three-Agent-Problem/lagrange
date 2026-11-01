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
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/activity"
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
// opts adjust the config.
func newEnv(t *testing.T, dbURL string, opts ...func(*platform.Config)) *env {
	t.Helper()
	if dbURL == "" {
		dbURL = pgtest.URL(t)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := platform.Config{
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
	}
	for _, o := range opts {
		o(&cfg)
	}
	p, err := platform.New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	mux.Handle("/", p.Handler())
	return &env{p: p, url: srv.URL}
}

// agent creates an agent address and returns a token for it.
func (e *env) agent(t *testing.T, handle string) string { return e.agentIn(t, handle, 0) }

// agentIn creates an agent in a workspace (zero: the default one) and returns its token.
func (e *env) agentIn(t *testing.T, handle string, workspace int64) string {
	t.Helper()
	ctx := t.Context()
	if err := e.p.Directory.CreateAgent(ctx, &directory.Address{Handle: handle, WorkspaceID: workspace}); err != nil {
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

// api is a logged-in web app session, acting in workspace ws (empty: the oldest one).
type api struct {
	t   *testing.T
	url string
	c   *http.Client
	ws  string
}

// in returns the session acting in workspace slug.
func (a *api) in(slug string) *api { b := *a; b.ws = slug; return &b }

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
	if a.ws != "" {
		req.Header.Set(platform.WorkspaceHeader, a.ws)
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
	// The answer is in the activity log, as input from Ada.
	entries, _ := e.p.Activity.Task(ctx, string(task.ID))
	if i := slices.IndexFunc(entries, func(en *activity.Entry) bool { return en.Kind == activity.KindInput }); i < 0 ||
		entries[i].Data["from"] != "ada" || entries[i].Summary != "Approved, ship it." {
		t.Fatalf("no input entry from ada in %+v", entries)
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
	// The agent's question waits on Ada, who sent the task.
	var waiting runList
	ada.do(http.MethodGet, "/api/runs?status=waiting", nil, http.StatusOK, &waiting)
	if len(waiting.Runs) != 1 || waiting.Runs[0].ID != id || strings.Join(waiting.Runs[0].Waiting, ",") != "ada" {
		t.Fatalf("waiting runs = %+v", waiting.Runs)
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
func delegator(e *env, token string) a2asrv.AgentExecutor { return delegatorTo(e, token, "echo") }

// delegatorTo hands every task to the agent at target and returns its answer.
func delegatorTo(e *env, token, target string) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			parent, _ := ec.ServiceParams.Get(relay.HeaderTask)
			card, err := agentcard.DefaultResolver.Resolve(ctx, e.url+"/a/"+target)
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

// A tunnel whose agent isn't running fails the task with the reason, instead of
// retrying in the background while the caller waits.
func TestAgentNotAnswering(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "down")
	dead := httptest.NewServer(http.NotFoundHandler())
	target, _ := url.Parse(dead.URL)
	dead.Close()
	e.connect(t, "down", tok, tunnel.Proxy(target, quiet), 1)

	c := e.client(t, "down", "", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	task := asTask(t)(c.SendMessage(ctx, text("hi")))
	if task.Status.State != a2a.TaskStateFailed {
		t.Fatalf("state = %s, want failed", task.Status.State)
	}
	if got := relay.Text(task.Status.Message); !strings.Contains(got, "nothing answers at "+dead.URL) {
		t.Fatalf("status = %q, want the unreachable target in it", got)
	}
	if f, _ := task.Status.Message.Metadata[relay.FailureKey].(map[string]any); f["kind"] != relay.FailureAgentDown || f["address"] != "down" {
		t.Fatalf("failure metadata = %v", task.Status.Message.Metadata)
	}
}

// An agent whose endpoint refuses connections fails the task too (a run that says why),
// instead of an error the caller has to make sense of.
func TestEndpointUnreachable(t *testing.T) {
	e := newEnv(t, "")
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if err := e.p.Directory.CreateAgent(t.Context(), &directory.Address{Handle: "gone", Endpoint: dead.URL}); err != nil {
		t.Fatal(err)
	}
	ada := e.login(t, adminEmail, adminPassword)
	var sent struct {
		Result struct {
			ID     string
			Status struct{ State string }
		}
	}
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "gone", "text": "hi"}, http.StatusOK, &sent)
	var got struct{ Task TaskViewJSON }
	waitFor(t, "the task to fail", func() bool {
		ada.do(http.MethodGet, "/api/tasks/"+sent.Result.ID, nil, http.StatusOK, &got)
		return got.Task.State == string(a2a.TaskStateFailed)
	})
	f, _ := got.Task.Task.Status.Message.Metadata[relay.FailureKey].(map[string]any)
	if f["kind"] != relay.FailureUnreachable {
		t.Fatalf("failure = %v (%q)", f, relay.Text(got.Task.Task.Status.Message))
	}
}

// TaskViewJSON is the part of a TaskView the failure tests read.
type TaskViewJSON struct {
	State string    `json:"state"`
	Task  *a2a.Task `json:"task"`
}

// ?waitingOn finds the runs blocked on someone, with how long they have waited.
func TestRunsWaitingOn(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "buyer")
	c := e.client(t, "ada", tok, "")
	task := asTask(t)(c.SendMessage(t.Context(), text("Approve 5,416 EUR?")))
	ada := e.login(t, adminEmail, adminPassword)
	var runs struct {
		Runs []struct {
			ID           string
			Waiting      []string
			WaitingSince *time.Time
		}
	}
	waitFor(t, "the run to wait on ada", func() bool {
		ada.do(http.MethodGet, "/api/runs?waitingOn=ada", nil, http.StatusOK, &runs)
		return len(runs.Runs) == 1
	})
	if runs.Runs[0].ID != string(task.ID) || runs.Runs[0].WaitingSince == nil || time.Since(*runs.Runs[0].WaitingSince) > time.Minute {
		t.Fatalf("runs = %+v", runs.Runs)
	}
	ada.do(http.MethodGet, "/api/runs?waitingOn=bob", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 0 {
		t.Fatalf("runs waiting on bob = %+v", runs.Runs)
	}
}

// Archived runs leave the list and come back with ?archived=1.
func TestArchiveRun(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "pinger")
	e.connect(t, "pinger", tok, agentServer("pinger", replier()), 1)
	if _, err := e.client(t, "pinger", "", "").SendMessage(t.Context(), text("ping")); err != nil {
		t.Fatal(err)
	}
	ada := e.login(t, adminEmail, adminPassword)
	var runs runList
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 {
		t.Fatalf("runs = %+v", runs.Runs)
	}
	id := runs.Runs[0].ID
	ada.do(http.MethodPost, "/api/runs/"+id+"/archive", struct{}{}, http.StatusNoContent, nil)
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 0 {
		t.Fatalf("runs after archiving = %+v", runs.Runs)
	}
	ada.do(http.MethodGet, "/api/runs?archived=1", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != id {
		t.Fatalf("archived runs = %+v", runs.Runs)
	}
	ada.do(http.MethodDelete, "/api/runs/"+id+"/archive", struct{}{}, http.StatusNoContent, nil)
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 {
		t.Fatalf("runs after unarchiving = %+v", runs.Runs)
	}
}

type runList struct {
	Runs []struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Outcome      string   `json:"outcome"`
		State        string   `json:"state"`
		Participants []string `json:"participants"`
		Waiting      []string `json:"waiting"`
		Tasks        int      `json:"tasks"`
		Hidden       int      `json:"hidden"`
	} `json:"runs"`
}

type runDetail struct {
	Run struct {
		ID string `json:"id"`
	} `json:"run"`
	Tasks     []struct{ ID string } `json:"tasks"`
	Entries   []*activity.Entry     `json:"entries"`
	Exchanges []*activity.Exchange  `json:"exchanges"`
}

// A person's request, the agent's delegation and the delegate's answer form one run,
// with every HTTP exchange behind it, and no token in any of them.
func TestRunsAPI(t *testing.T) {
	e := newEnv(t, "")
	echoToken := e.agent(t, "echo")
	buyerToken := e.agent(t, "buyer")
	e.connect(t, "echo", echoToken, agentServer("echo", echo("echo", 0, nil, nil, nil)), 2)
	e.connect(t, "buyer", buyerToken, agentServer("buyer", delegator(e, buyerToken)), 2)
	ada := e.login(t, adminEmail, adminPassword)

	var sent struct {
		Result struct{ ID string } `json:"result"`
	}
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@buyer", "text": "quote 200 hoodies"}, http.StatusOK, &sent)
	root := sent.Result.ID
	var runs runList
	waitFor(t, "the run to complete", func() bool {
		ada.do(http.MethodGet, "/api/runs?status=done", nil, http.StatusOK, &runs)
		return len(runs.Runs) == 1 && runs.Runs[0].Tasks == 2
	})
	if r := runs.Runs[0]; r.ID != root || r.State != string(a2a.TaskStateCompleted) || strings.Join(r.Participants, ",") != "ada,buyer,echo" {
		t.Fatalf("run = %+v", r)
	}
	ada.do(http.MethodGet, "/api/runs?status=failed", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 0 {
		t.Fatalf("failed runs = %+v", runs.Runs)
	}

	children, _ := e.p.Links.Children(t.Context(), a2a.TaskID(root))
	if len(children) != 1 {
		t.Fatalf("children = %+v", children)
	}
	child := string(children[0].TaskID)
	var d runDetail
	// Exchanges are stored when their response ends, just after the task completes.
	waitFor(t, "the exchanges", func() bool {
		ada.do(http.MethodGet, "/api/runs/"+child, nil, http.StatusOK, &d)
		return len(d.Exchanges) >= 3
	})
	if d.Run.ID != root || len(d.Tasks) != 2 {
		t.Fatalf("run %s, %d tasks", d.Run.ID, len(d.Tasks))
	}
	var in, outEcho, outBuyer *activity.Exchange
	for _, x := range d.Exchanges {
		switch {
		case x.Direction == activity.DirIn && x.Recipient == "echo":
			in = x
		case x.Direction == activity.DirOut && x.Recipient == "echo" && x.Method == http.MethodPost:
			outEcho = x
		case x.Direction == activity.DirOut && x.Recipient == "buyer" && x.Method == http.MethodPost:
			outBuyer = x
		}
		raw, _ := json.Marshal(x)
		if strings.Contains(string(raw), buyerToken) || strings.Contains(string(raw), echoToken) || strings.Contains(string(raw), "Bearer") {
			t.Fatalf("a token was stored: %s", raw)
		}
	}
	if in == nil || in.Sender != "buyer" || in.TaskID != child || !strings.Contains(in.Request, "quote 200 hoodies") || in.Status != http.StatusOK {
		t.Fatalf("inbound exchange = %+v", in)
	}
	if outEcho == nil || outEcho.TaskID != child || !strings.Contains(outEcho.Response, "echo: quote 200 hoodies") {
		t.Fatalf("exchange with echo = %+v", outEcho)
	}
	if outBuyer == nil || outBuyer.TaskID != root || outBuyer.Sender != "ada" {
		t.Fatalf("exchange with buyer = %+v", outBuyer)
	}
}

// A plain message exchange is a run of its own, with its exchanges.
func TestMessageRun(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "pinger")
	e.connect(t, "pinger", tok, agentServer("pinger", replier()), 1)
	if _, err := e.client(t, "pinger", "", "").SendMessage(t.Context(), text("ping")); err != nil {
		t.Fatal(err)
	}
	ada := e.login(t, adminEmail, adminPassword)
	var runs runList
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 || !strings.HasPrefix(runs.Runs[0].ID, "msg-") || runs.Runs[0].State != "message" {
		t.Fatalf("runs = %+v", runs.Runs)
	}
	var d runDetail
	waitFor(t, "the exchanges", func() bool {
		ada.do(http.MethodGet, "/api/runs/"+runs.Runs[0].ID, nil, http.StatusOK, &d)
		return len(d.Exchanges) >= 2
	})
	dirs := map[string]bool{}
	for _, x := range d.Exchanges {
		dirs[x.Direction] = true
	}
	if !dirs[activity.DirIn] || !dirs[activity.DirOut] || len(d.Entries) != 1 {
		t.Fatalf("exchanges %v, entries %d", dirs, len(d.Entries))
	}
}

// A push through an agent's exposed HTTP is recorded under the task it reports.
func TestPushRecorded(t *testing.T) {
	e := newEnv(t, "")
	tok := e.agent(t, "hooky")
	got := make(chan string, 1)
	hook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/webhook" {
			http.NotFound(w, r) // the platform fetches the agent card on connect
			return
		}
		raw, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + " " + r.Header.Get("Authorization") + " " + string(raw)
		w.WriteHeader(http.StatusNoContent)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, tunnel.ClientConfig{Server: e.url, Token: tok, Capacity: 1, Handler: hook, Expose: true, Logger: quiet})
	}()
	waitFor(t, "the tunnel to register", func() bool { return e.p.Mailbox.Stats("hooky").Sessions == 1 })

	body := `{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING"}}}`
	req, _ := http.NewRequest(http.MethodPost, e.url+"/t/hooky/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer push-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if delivered := <-got; !strings.HasPrefix(delivered, "/webhook Bearer push-secret") {
		t.Fatalf("delivered = %q", delivered)
	}
	var xs []*activity.Exchange
	waitFor(t, "the push exchange", func() bool {
		xs, _ = e.p.Activity.Exchanges(t.Context(), []string{"task-1"}, nil)
		return len(xs) == 1
	})
	x := xs[0]
	if x.Direction != activity.DirPush || x.Recipient != "hooky" || x.ContextID != "ctx-1" || x.Request != body || x.Status != http.StatusNoContent {
		t.Fatalf("exchange = %+v", x)
	}
	if raw, _ := json.Marshal(x); strings.Contains(string(raw), "push-secret") {
		t.Fatalf("the push secret was stored: %s", raw)
	}
}
