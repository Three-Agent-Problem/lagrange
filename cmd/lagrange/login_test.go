package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/three-agent-problem/lagrange/pg/pgtest"
	"github.com/three-agent-problem/lagrange/platform"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// testServer starts a platform with admin ada@example.com.
func testServer(t *testing.T) (*platform.Platform, string) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, err := platform.New(t.Context(), platform.Config{
		DatabaseURL: pgtest.URL(t), PublicURL: srv.URL, WorkerID: t.Name(),
		AdminEmail: "ada@example.com", AdminPassword: "correct horse battery staple", AdminName: "Ada", AdminHandle: "ada",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	mux.Handle("/", p.Handler())
	return p, srv.URL
}

// browserAs logs a browser in and returns a fake openBrowser that approves the CLI's
// authorize link the way the web page does, then follows the redirect.
func browserAs(t *testing.T, server, role string) func(string) error {
	jar, _ := cookiejar.New(nil)
	b := &http.Client{Jar: jar}
	post := func(path string, body any) *http.Response {
		raw, _ := json.Marshal(body)
		resp, err := b.Post(server+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return nil
		}
		return resp
	}
	if resp := post("/api/login", map[string]string{"email": "ada@example.com", "password": "correct horse battery staple"}); resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatal("browser login failed")
	}
	return func(link string) error {
		u, err := url.Parse(link)
		if err != nil || u.Path != "/cli/authorize" {
			t.Errorf("authorize link = %q", link)
			return err
		}
		q := u.Query()
		port, _ := strconv.Atoi(q.Get("port"))
		go func() {
			resp := post("/api/cli/authorize", map[string]any{"challenge": q.Get("challenge"), "name": q.Get("name"), "workspace": "default", "role": role, "port": port})
			if resp == nil {
				return
			}
			var out struct{ Redirect string }
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if r, err := b.Get(out.Redirect); err == nil {
				r.Body.Close()
			} else {
				t.Error(err)
			}
		}()
		return nil
	}
}

func useConfig(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("LAGRANGE_CONFIG", p)
	t.Setenv("LAGRANGE_SERVER", "")
	t.Setenv("LAGRANGE_KEY", "")
	t.Setenv("LAGRANGE_TOKEN", "")
	return p
}

func TestLoginThenTunnelByAddress(t *testing.T) {
	p, server := testServer(t)
	path := useConfig(t)
	openBrowser = browserAs(t, server, "member")
	t.Cleanup(func() { openBrowser = nil })

	if err := login(t.Context(), []string{"--server", server}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config %v, mode %v", err, info.Mode())
	}
	cfg, _ := loadConfig()
	if cfg.Server != server || !strings.HasPrefix(cfg.Key, "lgk_") || cfg.Workspace != "default" || cfg.Role != "member" {
		t.Fatalf("config = %+v", cfg)
	}
	if err := whoami(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	// The tunnel creates the address and its token with the login: nothing to copy.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runTunnel(ctx, []string{"--address", "@echo-test", "--create", "--echo"}) }()
	deadline := time.Now().Add(15 * time.Second)
	for p.Mailbox.Stats("echo-test").Sessions == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the tunnel didn't connect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	cfg, _ = loadConfig()
	if tok := cfg.Tokens[tokenKey(server, "echo-test")]; !strings.HasPrefix(tok, "lgr_") {
		t.Fatalf("saved tokens = %v", cfg.Tokens)
	}

	if err := logout(nil); err != nil {
		t.Fatal(err)
	}
	if err := whoami(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("whoami after logout = %v", err)
	}
}

func TestLoginPaste(t *testing.T) {
	_, server := testServer(t)
	useConfig(t)
	// The person opens the printed link elsewhere; the page shows the code, they paste it.
	jar, _ := cookiejar.New(nil)
	b := &http.Client{Jar: jar}
	raw, _ := json.Marshal(map[string]string{"email": "ada@example.com", "password": "correct horse battery staple"})
	if resp, err := b.Post(server+"/api/login", "application/json", bytes.NewReader(raw)); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatal("browser login failed")
	}
	r, w := io.Pipe()
	stdin = r
	t.Cleanup(func() { stdin = os.Stdin })
	// pasteCode prints the link; approve with the challenge the CLI made, read from the
	// link it printed, by capturing stdout.
	old := os.Stdout
	pr, pw, _ := os.Pipe()
	os.Stdout = pw
	t.Cleanup(func() { os.Stdout = old })
	go func() {
		buf := make([]byte, 4096)
		n, _ := pr.Read(buf)
		i := strings.Index(string(buf[:n]), server+"/cli/authorize?")
		u, _ := url.Parse(strings.Fields(string(buf[i:n]))[0])
		body, _ := json.Marshal(map[string]any{"challenge": u.Query().Get("challenge"), "workspace": "default", "role": "viewer"})
		resp, err := b.Post(server+"/api/cli/authorize", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return
		}
		var out struct{ Code string }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_, _ = io.WriteString(w, out.Code+"\n")
		go func() { _, _ = io.Copy(io.Discard, pr) }()
	}()
	err := login(t.Context(), []string{"--server", server, "--no-browser"})
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := loadConfig(); cfg.Role != "viewer" {
		t.Fatalf("config = %+v", cfg)
	}
}
