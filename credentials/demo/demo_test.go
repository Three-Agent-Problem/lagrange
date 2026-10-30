package demo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/credentials"
)

func TestAllowFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := New("http://console.example/demo-auth")

	if _, err := b.Lookup(ctx, "u1", "google-calendar"); !errors.Is(err, credentials.ErrNoGrant) {
		t.Fatalf("Lookup() before consent error = %v, want ErrNoGrant", err)
	}
	c, err := b.Begin(ctx, "u1", "google-calendar")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.URL, "http://console.example/demo-auth/connect/") {
		t.Errorf("Begin() URL = %s", c.URL)
	}

	// The consent page is served by Handler; POSTing "allow" is what the Allow button does.
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.PostForm(srv.URL+"/connect/"+c.ID, url.Values{"decision": {"allow"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST allow = %s, want 303", resp.Status)
	}

	g, err := b.Await(ctx, c)
	if err != nil {
		t.Fatalf("Await() error = %v", err)
	}
	var out struct {
		ID string `json:"id"`
	}
	event := map[string]any{"summary": "Hoodies delivery", "start": map[string]string{"date": "2026-11-20"}, "end": map[string]string{"date": "2026-11-21"}}
	if err := g.Call(ctx, http.MethodPost, CalendarEventsPath, event, &out); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if events := b.Events(); len(events) != 1 || events[0].Summary != "Hoodies delivery" || events[0].User != "u1" || out.ID != events[0].ID {
		t.Errorf("Events() = %+v, Call id = %s", events, out.ID)
	}
	if _, err := b.Lookup(ctx, "u1", "google-calendar"); err != nil {
		t.Errorf("Lookup() after consent error = %v, want the stored grant", err)
	}
	if err := g.Call(ctx, http.MethodGet, "/gmail/v1/users/me/messages", nil, nil); err == nil {
		t.Error("Call() on an unfaked API error = nil, want an error")
	}
}

func TestDenyFlow(t *testing.T) {
	b := New("http://console.example/demo-auth")
	c, err := b.Begin(context.Background(), "u1", "google-calendar")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Decide(c.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Await(context.Background(), c); !errors.Is(err, credentials.ErrDenied) {
		t.Fatalf("Await() error = %v, want ErrDenied", err)
	}
	if err := b.Decide(c.ID, true); err == nil {
		t.Error("Decide() twice error = nil, want an error")
	}
}
