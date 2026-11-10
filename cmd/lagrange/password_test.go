package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/pg/pgtest"
	"github.com/three-agent-problem/lagrange/platform"
)

func TestLoginWithPassword(t *testing.T) {
	_, server := testServer(t)
	useConfig(t)
	t.Setenv("LAGRANGE_PASSWORD", "correct horse battery staple")
	if err := login(t.Context(), []string{"--server", server, "--email", "ada@example.com", "--name", "ci box"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadConfig()
	if cfg.Server != server || !strings.HasPrefix(cfg.Key, "lgk_") || cfg.Workspace != "default" || cfg.Role != "owner" {
		t.Fatalf("config = %+v", cfg)
	}
	me, err := client.New(server, cfg.Key).Me(t.Context())
	if err != nil || me.User.Handle != "ada" {
		t.Fatalf("me = %+v, %v", me, err)
	}

	// From stdin, and a wrong password fails.
	t.Setenv("LAGRANGE_PASSWORD", "")
	stdin = strings.NewReader("wrong\n")
	t.Cleanup(func() { stdin = os.Stdin })
	if err := login(t.Context(), []string{"--server", server, "--email", "ada@example.com"}); err == nil || !strings.Contains(err.Error(), "wrong email or password") {
		t.Fatalf("wrong password: %v", err)
	}
	stdin = strings.NewReader("")
	if err := login(t.Context(), []string{"--server", server, "--email", "ada@example.com"}); err == nil || !strings.Contains(err.Error(), "no password") {
		t.Fatalf("no password: %v", err)
	}
	if _, err := client.PasswordLogin(t.Context(), server, "ada@example.com", "correct horse battery staple", "elsewhere", "x"); err == nil {
		t.Fatal("a key in a workspace ada isn't in")
	}
}

func TestTokenAPIKey(t *testing.T) {
	dbURL := pgtest.URL(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, err := platform.New(t.Context(), platform.Config{
		DatabaseURL: dbURL, PublicURL: srv.URL, WorkerID: t.Name(),
		AdminEmail: "ada@example.com", AdminPassword: "correct horse battery staple", AdminName: "Ada", AdminHandle: "ada",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	mux.Handle("/", p.Handler())

	out := captureStdout(t, func() {
		if err := token(t.Context(), []string{"--database-url", dbURL, "--api-key", "--email", "ada@example.com", "--role", "member"}); err != nil {
			t.Fatal(err)
		}
	})
	key := strings.TrimSpace(out)
	me, err := client.New(srv.URL, key).Me(t.Context())
	if err != nil || me.User.Handle != "ada" || me.Key.Role != "member" || me.Key.Workspace != "default" {
		t.Fatalf("key %q: %+v, %v", key, me, err)
	}
	for _, args := range [][]string{
		{"--api-key"},
		{"--api-key", "--email", "nobody@example.com"},
		{"--api-key", "--email", "ada@example.com", "--workspace", "elsewhere"},
	} {
		if err := token(t.Context(), append([]string{"--database-url", dbURL}, args...)); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}
