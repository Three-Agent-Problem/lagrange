package platform_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/webhook"
)

func fastWebhooks(c *platform.Config) {
	c.WebhookTick = 50 * time.Millisecond
	c.WebhookRetryBase = 100 * time.Millisecond
}

type received struct {
	typ, delivery string
	ev            webhook.Event
	sigErr        error
}

// receiver is a webhook endpoint that checks signatures, and fails the first failFirst
// requests with 500.
type receiver struct {
	mu        sync.Mutex
	secret    string
	failFirst int
	calls     int
	got       []received
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.calls++
	if rc.calls <= rc.failFirst {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var ev webhook.Event
	_ = json.Unmarshal(body, &ev)
	rc.got = append(rc.got, received{typ: r.Header.Get(webhook.EventHeader), delivery: r.Header.Get(webhook.DeliveryHeader), ev: ev,
		sigErr: webhook.Verify(rc.secret, r.Header.Get(webhook.SignatureHeader), body, 0)})
	w.WriteHeader(http.StatusNoContent)
}

// wait returns the first event of type typ (matching ok, if set) once it arrives.
func (rc *receiver) wait(t *testing.T, typ string, ok func(webhook.Event) bool) received {
	t.Helper()
	var out received
	waitFor(t, "a "+typ+" webhook", func() bool {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		for _, g := range rc.got {
			if g.typ == typ && (ok == nil || ok(g.ev)) {
				out = g
				return true
			}
		}
		return false
	})
	if out.sigErr != nil {
		t.Fatalf("%s: signature: %v", typ, out.sigErr)
	}
	return out
}

func (rc *receiver) types() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var out []string
	for _, g := range rc.got {
		out = append(out, g.typ)
	}
	return out
}

type createdHook struct {
	Webhook platform.Webhook
	Secret  string
}

func TestWebhooks(t *testing.T) {
	e := newEnv(t, "", fastWebhooks)
	ctx := t.Context()
	if _, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	ada := e.login(t, adminEmail, adminPassword)
	bob := e.login(t, "bob@example.com", "hunter2hunter2")

	rc := &receiver{failFirst: 1}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	var h createdHook
	ada.do(http.MethodPost, "/api/webhooks", map[string]any{"url": srv.URL + "/hook", "description": "ops",
		"events": []string{webhook.TaskWaiting, webhook.TaskAnswered, webhook.RunCompleted, webhook.AddressOnline, webhook.AddressOffline}}, http.StatusCreated, &h)
	rc.mu.Lock()
	rc.secret = h.Secret
	rc.mu.Unlock()

	// One that only wants failures hears nothing here; one in another workspace neither.
	quiet := &receiver{}
	qsrv := httptest.NewServer(quiet)
	t.Cleanup(qsrv.Close)
	ada.do(http.MethodPost, "/api/webhooks", map[string]any{"url": qsrv.URL, "events": []string{webhook.RunFailed}}, http.StatusCreated, nil)
	other := &receiver{}
	osrv := httptest.NewServer(other)
	t.Cleanup(osrv.Close)
	// Carol's own workspace: none of its addresses take part below. (A workspace sees the
	// traffic of its members' addresses, so one Ada belonged to would hear her runs.)
	if _, err := e.p.Directory.CreateUser(ctx, "carol@example.com", "Carol", "carol", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	carol := e.login(t, "carol@example.com", "hunter2hunter2")
	carol.do(http.MethodPost, "/api/workspaces", map[string]string{"name": "Carols"}, http.StatusCreated, nil)
	carol.in("carols").do(http.MethodPost, "/api/webhooks", map[string]any{"url": osrv.URL, "events": webhook.Types}, http.StatusCreated, nil)

	// Bad input and roles.
	ada.do(http.MethodPost, "/api/webhooks", map[string]any{"url": "ftp://x", "events": []string{webhook.RunFailed}}, http.StatusBadRequest, nil)
	ada.do(http.MethodPost, "/api/webhooks", map[string]any{"url": srv.URL, "events": []string{"task.exploded"}}, http.StatusBadRequest, nil)
	bob.do(http.MethodGet, "/api/webhooks", nil, http.StatusForbidden, nil)

	// The agent comes online; the first delivery fails with 500 and is retried.
	stop := e.connect(t, "echo", e.agent(t, "echo"), agentServer("echo", echo("echo", 0, nil, nil, nil)), 1)
	online := rc.wait(t, webhook.AddressOnline, nil)
	if online.ev.Data["address"] != "echo" || online.ev.Workspace != "default" || online.delivery == "" {
		t.Fatalf("online = %+v", online)
	}

	// A run completes.
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "echo", "text": "hello hooks"}, http.StatusOK, nil)
	done := rc.wait(t, webhook.RunCompleted, func(ev webhook.Event) bool { return ev.Data["to"] == "echo" })
	if done.ev.Data["from"] != "ada" || done.ev.Data["state"] != "completed" || done.ev.Data["url"] == "" {
		t.Fatalf("run.completed = %+v", done.ev)
	}

	// A question waits on Bob, he answers, the run completes with his answer.
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "bob", "text": "Approve 200 hoodies?"}, http.StatusOK, nil)
	waiting := rc.wait(t, webhook.TaskWaiting, nil)
	if waiting.ev.Data["waitingOn"] != "bob" || waiting.ev.Data["question"] != "Approve 200 hoodies?" {
		t.Fatalf("task.waiting = %+v", waiting.ev)
	}
	taskID, _ := waiting.ev.Data["taskId"].(string)
	bob.do(http.MethodPost, "/api/tasks/"+taskID+"/answer", map[string]string{"text": "Yes.", "decision": "approve"}, http.StatusAccepted, nil)
	answered := rc.wait(t, webhook.TaskAnswered, nil)
	if answered.ev.Data["answeredBy"] != "bob" || answered.ev.Data["taskId"] != taskID {
		t.Fatalf("task.answered = %+v", answered.ev)
	}
	rc.wait(t, webhook.RunCompleted, func(ev webhook.Event) bool { return ev.Data["runId"] == taskID && ev.Data["answeredBy"] == "bob" })

	// The agent goes offline.
	stop()
	rc.wait(t, webhook.AddressOffline, func(ev webhook.Event) bool { return ev.Data["address"] == "echo" })

	// Send test.
	ada.do(http.MethodPost, "/api/webhooks/"+strconv.FormatInt(h.Webhook.ID, 10)+"/test", struct{}{}, http.StatusAccepted, nil)
	rc.wait(t, webhook.Ping, nil)

	// The log shows the retry, and the list counts deliveries.
	var log struct{ Deliveries []platform.Delivery }
	ada.do(http.MethodGet, "/api/webhooks/"+strconv.FormatInt(h.Webhook.ID, 10)+"/deliveries", nil, http.StatusOK, &log)
	first := log.Deliveries[len(log.Deliveries)-1]
	if first.Type != webhook.AddressOnline || first.Status != "delivered" || first.Attempts != 2 {
		t.Fatalf("first delivery = %+v", first)
	}
	var list struct{ Webhooks []platform.Webhook }
	ada.do(http.MethodGet, "/api/webhooks", nil, http.StatusOK, &list)
	if len(list.Webhooks) != 2 || list.Webhooks[0].Delivered < 6 {
		t.Fatalf("webhooks = %+v", list.Webhooks)
	}

	time.Sleep(300 * time.Millisecond)
	if got := quiet.types(); len(got) != 0 {
		t.Fatalf("the run.failed hook got %v", got)
	}
	if got := other.types(); len(got) != 0 {
		t.Fatalf("the other workspace's hook got %v", got)
	}

	// Paused hooks get nothing; deleted ones are gone.
	ada.do(http.MethodPut, "/api/webhooks/"+strconv.FormatInt(h.Webhook.ID, 10), map[string]any{"active": false}, http.StatusNoContent, nil)
	n := len(rc.types())
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "bob", "text": "Lunch?"}, http.StatusOK, nil)
	time.Sleep(400 * time.Millisecond)
	if len(rc.types()) != n {
		t.Fatalf("a paused hook got %v", rc.types()[n:])
	}
	ada.do(http.MethodDelete, "/api/webhooks/"+strconv.FormatInt(h.Webhook.ID, 10), struct{}{}, http.StatusNoContent, nil)
	ada.do(http.MethodGet, "/api/webhooks/"+strconv.FormatInt(h.Webhook.ID, 10)+"/deliveries", nil, http.StatusNotFound, nil)
}

func TestWebhookSignature(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	sig := webhook.Sign("whsec_x", time.Now(), body)
	if err := webhook.Verify("whsec_x", sig, body, 0); err != nil {
		t.Fatal(err)
	}
	if webhook.Verify("whsec_y", sig, body, 0) == nil {
		t.Fatal("a wrong secret verified")
	}
	if webhook.Verify("whsec_x", sig, []byte(`{"id":"evt_2"}`), 0) == nil {
		t.Fatal("a changed body verified")
	}
	if webhook.Verify("whsec_x", webhook.Sign("whsec_x", time.Now().Add(-time.Hour), body), body, 0) == nil {
		t.Fatal("an old signature verified")
	}
}

func TestWebhookSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)
	if _, err := webhook.NewClient(false).Get(srv.URL); err == nil {
		t.Fatal("the guarded client reached a loopback address")
	}
	resp, err := webhook.NewClient(true).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
