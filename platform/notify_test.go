package platform_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/relay"
)

type sentMail struct{ to, subject, body string }

// outbox is a notify.Email that keeps what it sends.
type outbox struct {
	mu   sync.Mutex
	sent []sentMail
}

func (o *outbox) Send(_ context.Context, to, subject, body string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, sentMail{to, subject, body})
	return nil
}

// next waits for the n-th email (from 1).
func (o *outbox) next(t *testing.T, n int) sentMail {
	t.Helper()
	waitFor(t, "an email", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return len(o.sent) >= n
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sent[n-1]
}

var linkPattern = regexp.MustCompile(`/d/(lgd_[0-9a-f]+)`)

func linkToken(t *testing.T, body string) string {
	t.Helper()
	m := linkPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no decision link in %q", body)
	}
	return m[1]
}

// anon calls the API without a session, as someone who only has the link.
func anon(t *testing.T, e *env, method, path, contentType, body string, want int, out any) {
	t.Helper()
	req, _ := http.NewRequest(method, e.url+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
}

// An agent asks a person: they get an email with the question (data as plain lines, not
// JSON) and a link that answers it once, without logging in.
func TestDecisionLinkEmail(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	ctx := t.Context()
	c := e.client(t, "ada", e.agent(t, "buyer"), "")

	req := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser,
		a2a.NewTextPart("Approve 200 hoodies?"), a2a.NewDataPart(map[string]any{"total": 5416, "supplier": "Northwind"}))}
	task := asTask(t)(c.SendMessage(ctx, req))

	mail := out.next(t, 1)
	if mail.to != adminEmail || mail.subject != "@buyer needs your answer" {
		t.Fatalf("mail = %+v", mail)
	}
	for _, want := range []string{"Approve 200 hoodies?", "supplier: Northwind", "total: 5416", e.url + "/d/lgd_"} {
		if !strings.Contains(mail.body, want) {
			t.Fatalf("mail body lacks %q:\n%s", want, mail.body)
		}
	}
	if strings.Contains(mail.body, "{") {
		t.Fatalf("mail body has JSON:\n%s", mail.body)
	}
	token := linkToken(t, mail.body)

	var view struct {
		Task struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"task"`
		For struct{ Handle string } `json:"for"`
	}
	anon(t, e, http.MethodGet, "/api/decisions/"+token, "", "", http.StatusOK, &view)
	if view.Task.ID != string(task.ID) || view.Task.State != string(a2a.TaskStateInputRequired) || view.For.Handle != "ada" {
		t.Fatalf("decision = %+v", view)
	}
	anon(t, e, http.MethodGet, "/api/decisions/lgd_nope", "", "", http.StatusGone, nil)
	// Only JSON, and a failed answer leaves the link working.
	anon(t, e, http.MethodPost, "/api/decisions/"+token, "text/plain", `{"text":"yes"}`, http.StatusUnsupportedMediaType, nil)
	anon(t, e, http.MethodPost, "/api/decisions/"+token, "application/json", `{}`, http.StatusBadRequest, nil)

	anon(t, e, http.MethodPost, "/api/decisions/"+token, "application/json", `{"text":"Approved."}`, http.StatusAccepted, nil)
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := done.Status.Message.Metadata[relay.AnsweredByKey]; got != "ada" || relay.Text(done.Status.Message) != "Approved." {
		t.Fatalf("answer = %q by %v", relay.Text(done.Status.Message), got)
	}
	// Used up.
	anon(t, e, http.MethodPost, "/api/decisions/"+token, "application/json", `{"text":"again"}`, http.StatusGone, nil)
	anon(t, e, http.MethodGet, "/api/decisions/"+token, "", "", http.StatusGone, nil)
}

// A person's own task: the agent asks its sender back, and the link answers the agent.
func TestDecisionLinkSender(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	e.connect(t, "shop", e.agent(t, "shop"), agentServer("shop", asker()), 1)
	ada := e.login(t, adminEmail, adminPassword)

	var sent struct {
		Result struct{ ID string } `json:"result"`
	}
	ada.do(http.MethodPost, "/api/send", map[string]string{"to": "@shop", "text": "One hoodie please"}, http.StatusOK, &sent)
	mail := out.next(t, 1)
	if mail.subject != "@shop needs your answer" || !strings.Contains(mail.body, "Which size?") {
		t.Fatalf("mail = %+v", mail)
	}
	anon(t, e, http.MethodPost, "/api/decisions/"+linkToken(t, mail.body), "application/json", `{"text":"L"}`, http.StatusAccepted, nil)
	var full struct {
		Task struct {
			State string    `json:"state"`
			Task  *a2a.Task `json:"task"`
		} `json:"task"`
	}
	waitFor(t, "the order", func() bool {
		ada.do(http.MethodGet, "/api/tasks/"+sent.Result.ID, nil, http.StatusOK, &full)
		return full.Task.State == string(a2a.TaskStateCompleted)
	})
	if got := relay.Text(full.Task.Task.Status.Message); got != "Ordering size L" {
		t.Fatalf("reply = %q", got)
	}
}

// Settings: Slack only, no email; webhooks must be Slack's; the test button works.
func TestNotificationSettings(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	ctx := t.Context()
	bobUser, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false)
	if err != nil {
		t.Fatal(err)
	}
	bob := e.login(t, "bob@example.com", "hunter2hunter2")

	var got struct {
		Settings       directory.NotificationSettings `json:"settings"`
		EmailAvailable bool                           `json:"emailAvailable"`
	}
	bob.do(http.MethodGet, "/api/settings/notifications", nil, http.StatusOK, &got)
	if !got.Settings.Email || got.Settings.SlackWebhook != "" || !got.EmailAvailable {
		t.Fatalf("defaults = %+v", got)
	}
	bob.do(http.MethodPut, "/api/settings/notifications", map[string]any{"email": false, "slackWebhook": "http://169.254.169.254/"}, http.StatusBadRequest, nil)
	bob.do(http.MethodPut, "/api/settings/notifications", map[string]any{"email": false, "slackWebhook": "https://hooks.slack.com/services/T/B/x"}, http.StatusOK, &got)
	if got.Settings.Email || got.Settings.SlackWebhook == "" {
		t.Fatalf("saved = %+v", got)
	}

	// Point Bob's Slack at a fake (the API only accepts hooks.slack.com).
	posts := make(chan string, 4)
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		posts <- body.Text
	}))
	t.Cleanup(slack.Close)
	if err := e.p.Directory.SetNotificationSettings(ctx, bobUser.ID, &directory.NotificationSettings{SlackWebhook: slack.URL}); err != nil {
		t.Fatal(err)
	}
	bob.do(http.MethodPost, "/api/settings/notifications/test", map[string]any{}, http.StatusNoContent, nil)
	if msg := <-posts; !strings.Contains(msg, "notifications work") {
		t.Fatalf("test post = %q", msg)
	}

	c := e.client(t, "bob", e.agent(t, "buyer"), "")
	task := asTask(t)(c.SendMessage(ctx, text("Sign off the Q3 budget?")))
	msg := <-posts
	if !strings.Contains(msg, "@buyer needs your answer") || !strings.Contains(msg, "> Sign off the Q3 budget?") || !strings.Contains(msg, "|Answer now>") {
		t.Fatalf("slack post = %q", msg)
	}
	anon(t, e, http.MethodPost, "/api/decisions/"+linkToken(t, msg), "application/json", `{"text":"No.","decision":"reject"}`, http.StatusAccepted, nil)
	waitState(t, c, task.ID, a2a.TaskStateRejected)
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.sent) != 0 {
		t.Fatalf("email off, but sent %+v", out.sent)
	}
}
