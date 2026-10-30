package nango

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/credentials"
)

const testKey = "test-secret"

// fakeNango implements the slice of Nango's HTTP API the broker uses.
type fakeNango struct {
	t *testing.T
	// connectionsAfter is the number of GET /connections calls that return nothing
	// before the user's connection "appears".
	connectionsAfter int32
	listCalls        atomic.Int32

	mu            sync.Mutex
	lastProxy     http.Header
	lastProxyBody []byte
}

func (f *fakeNango) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
		f.t.Errorf("%s %s: Authorization = %q", r.Method, r.URL.Path, got)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/connect/sessions":
		var body struct {
			Tags                map[string]string `json:"tags"`
			AllowedIntegrations []string          `json:"allowed_integrations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("connect session body: %v", err)
			return
		}
		if body.Tags[EndUserTag] != "u1" || len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "google-calendar" {
			f.t.Errorf("connect session body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"data":{"token":"nango_connect_session_1","connect_link":"https://connect.nango.dev/?session_token=nango_connect_session_1","expires_at":"2099-01-01T00:00:00.000Z"}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/connections":
		if got := r.URL.Query().Get("tags[" + EndUserTag + "]"); got != "u1" {
			f.t.Errorf("list connections tag filter = %q, want u1", got)
		}
		if f.listCalls.Add(1) <= f.connectionsAfter {
			_, _ = w.Write([]byte(`{"connections":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"connections":[
			{"id":1,"connection_id":"slack-conn","provider":"slack","provider_config_key":"slack","created":"2026-10-01T10:00:00.000Z","tags":{"end_user_id":"u1"},"errors":[]},
			{"id":2,"connection_id":"old-cal","provider":"google-calendar","provider_config_key":"google-calendar","created":"2026-10-01T10:00:00.000Z","tags":{"end_user_id":"u1"},"errors":[]},
			{"id":3,"connection_id":"new-cal","provider":"google-calendar","provider_config_key":"google-calendar","created":"2026-10-02T10:00:00.000Z","tags":{"end_user_id":"u1"},"errors":[]},
			{"id":4,"connection_id":"broken-cal","provider":"google-calendar","provider_config_key":"google-calendar","created":"2026-10-03T10:00:00.000Z","tags":{"end_user_id":"u1"},"errors":[{"type":"auth","log_id":"x"}]}
		]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/proxy/calendar/v3/calendars/primary/events":
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastProxy, f.lastProxyBody = r.Header.Clone(), body
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"evt1","status":"confirmed"}`))
	default:
		http.NotFound(w, r)
	}
}

func newTestBroker(t *testing.T, fake *fakeNango, cfg BrokerConfig) *Broker {
	t.Helper()
	fake.t = t
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := NewClient(testKey, srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewBroker(client, cfg)
}

func TestNewClientNeedsKey(t *testing.T) {
	if _, err := NewClient("", "", nil); err == nil {
		t.Fatal("NewClient() error = nil, want an error without a secret key")
	}
}

func TestBeginCreatesConnectSession(t *testing.T) {
	b := newTestBroker(t, &fakeNango{}, BrokerConfig{})
	c, err := b.Begin(context.Background(), "u1", "google-calendar")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if c.URL != "https://connect.nango.dev/?session_token=nango_connect_session_1" || c.ID != "nango_connect_session_1" {
		t.Errorf("Begin() = %+v", c)
	}
	if c.ExpiresAt.Year() != 2099 {
		t.Errorf("Begin() ExpiresAt = %v", c.ExpiresAt)
	}
}

func TestLookupPicksNewestHealthyConnection(t *testing.T) {
	b := newTestBroker(t, &fakeNango{}, BrokerConfig{})
	g, err := b.Lookup(context.Background(), "u1", "google-calendar")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if g.ConnectionID() != "new-cal" {
		t.Errorf("Lookup() connection = %s, want new-cal (newest without errors)", g.ConnectionID())
	}
}

func TestLookupNoGrant(t *testing.T) {
	b := newTestBroker(t, &fakeNango{connectionsAfter: 1}, BrokerConfig{})
	if _, err := b.Lookup(context.Background(), "u1", "google-calendar"); !errors.Is(err, credentials.ErrNoGrant) {
		t.Fatalf("Lookup() error = %v, want ErrNoGrant", err)
	}
}

func TestAwaitByPolling(t *testing.T) {
	fake := &fakeNango{connectionsAfter: 2}
	b := newTestBroker(t, fake, BrokerConfig{PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := b.Begin(ctx, "u1", "google-calendar")
	if err != nil {
		t.Fatal(err)
	}
	g, err := b.Await(ctx, c)
	if err != nil {
		t.Fatalf("Await() error = %v", err)
	}
	if g.ConnectionID() != "new-cal" || fake.listCalls.Load() < 3 {
		t.Errorf("Await() connection = %s after %d polls", g.ConnectionID(), fake.listCalls.Load())
	}
}

func TestAwaitByWebhook(t *testing.T) {
	const signingKey = "signing-key"
	// Polling is off (one check an hour), so only a webhook can make Await look.
	b := newTestBroker(t, &fakeNango{}, BrokerConfig{PollInterval: time.Hour, WebhookSigningKey: signingKey})
	hooks := httptest.NewServer(b.WebhookHandler())
	defer hooks.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &credentials.Challenge{ID: "s1", User: "u1", Integration: "google-calendar"}
	type result struct {
		g   credentials.Grant
		err error
	}
	done := make(chan result, 1)
	go func() {
		g, err := b.Await(ctx, c)
		done <- result{g, err}
	}()

	// The body names a connection that is not the user's; the broker must not use it.
	body := []byte(`{"type":"auth","operation":"creation","success":true,"connectionId":"someone-elses","providerConfigKey":"google-calendar","tags":{"end_user_id":"u1"}}`)
	if status := postHook(t, hooks.URL, body, "not-a-signature"); status != http.StatusUnauthorized {
		t.Fatalf("webhook with a bad signature = %d, want 401", status)
	}
	// The waiter registers asynchronously, so resend until Await returns.
	for {
		if status := postHook(t, hooks.URL, body, sign(signingKey, body)); status != http.StatusOK {
			t.Fatalf("webhook = %d, want 200", status)
		}
		select {
		case r := <-done:
			if r.err != nil || r.g.ConnectionID() != "new-cal" {
				t.Fatalf("Await() = %v, %v; want new-cal, the user's connection as listed by Nango", r.g, r.err)
			}
			return
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Await() did not return after the webhook")
		}
	}
}

func TestWebhookNeedsSigningKey(t *testing.T) {
	b := newTestBroker(t, &fakeNango{}, BrokerConfig{})
	hooks := httptest.NewServer(b.WebhookHandler())
	defer hooks.Close()
	body := []byte(`{"type":"auth","operation":"creation","success":true,"connectionId":"x","providerConfigKey":"google-calendar","tags":{"end_user_id":"u1"}}`)
	if status := postHook(t, hooks.URL, body, ""); status != http.StatusForbidden {
		t.Fatalf("webhook without a configured signing key = %d, want 403", status)
	}
}

func TestAwaitExpires(t *testing.T) {
	b := newTestBroker(t, &fakeNango{connectionsAfter: 1 << 30}, BrokerConfig{PollInterval: time.Hour})
	c := &credentials.Challenge{User: "u1", Integration: "google-calendar", ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	if _, err := b.Await(context.Background(), c); !errors.Is(err, credentials.ErrExpired) {
		t.Fatalf("Await() error = %v, want ErrExpired", err)
	}
}

func TestGrantCallUsesProxy(t *testing.T) {
	fake := &fakeNango{}
	b := newTestBroker(t, fake, BrokerConfig{})
	g, err := b.Lookup(context.Background(), "u1", "google-calendar")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID string `json:"id"`
	}
	event := map[string]any{"summary": "Hoodies delivery", "start": map[string]string{"date": "2026-11-20"}}
	if err := g.Call(context.Background(), http.MethodPost, "/calendar/v3/calendars/primary/events", event, &out); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out.ID != "evt1" {
		t.Errorf("Call() decoded id = %q", out.ID)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if h := fake.lastProxy; h.Get("Provider-Config-Key") != "google-calendar" || h.Get("Connection-Id") != "new-cal" {
		t.Errorf("proxy headers = %v", h)
	}
	if !bytes.Contains(fake.lastProxyBody, []byte(`"Hoodies delivery"`)) {
		t.Errorf("proxy body = %s", fake.lastProxyBody)
	}
}

func postHook(t *testing.T, url string, body []byte, signature string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(SignatureHeader, signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func sign(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
