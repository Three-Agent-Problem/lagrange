package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

func parseACP(t *testing.T, args ...string) *acpOptions {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var o acpOptions
	acpFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestACPFlagsNone(t *testing.T) {
	cfg, err := parseACP(t).config()
	if err != nil || cfg != nil {
		t.Fatalf("no flags: got %v, %v; want the executor's defaults (nil)", cfg, err)
	}
}

func TestACPSettings(t *testing.T) {
	o := parseACP(t, "--model", "haiku", "--set", "mode=plan", "--set", "budget=3", "--caller-settings", "model")
	cfg, err := o.config()
	if err != nil {
		t.Fatal(err)
	}
	ec := &a2asrv.ExecutorContext{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))}
	got, err := cfg.Settings(context.Background(), ec)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"model": "haiku", "mode": "plan", "budget": float64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %v, want %v", got, want)
	}
	// The sender may pick the model, and only the model.
	ec.Message.Metadata = map[string]any{a2aacp.SettingsMetadataKey: map[string]any{"model": "opus"}}
	if got, err = cfg.Settings(context.Background(), ec); err != nil || got["model"] != "opus" || got["mode"] != "plan" {
		t.Fatalf("caller model: %v, %v", got, err)
	}
	ec.Message.Metadata = map[string]any{a2aacp.SettingsMetadataKey: map[string]any{"mode": "bypassPermissions"}}
	if _, err = cfg.Settings(context.Background(), ec); err == nil {
		t.Fatal("a sender changed a setting outside --caller-settings")
	}
	// The fixed settings aren't shared state between messages.
	got["model"] = "changed"
	ec.Message.Metadata = nil
	if again, _ := cfg.Settings(context.Background(), ec); again["model"] != "haiku" {
		t.Fatalf("settings leaked between calls: %v", again)
	}
}

func TestACPToolRules(t *testing.T) {
	cfg, err := parseACP(t, "--allow", "read,search", "--allow", "execute", "--deny", "delete").config()
	if err != nil {
		t.Fatal(err)
	}
	rules, ok := cfg.AutoAnswer.(a2aacp.ToolRules)
	if !ok || !reflect.DeepEqual(rules.Allow, []string{"read", "search", "execute"}) || !reflect.DeepEqual(rules.Deny, []string{"delete"}) {
		t.Fatalf("rules = %#v", cfg.AutoAnswer)
	}
}

func TestMCPServer(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"api=https://example.com/mcp", `{"headers":[],"name":"api","type":"http","url":"https://example.com/mcp"}`},
		{"fs = mcp-fs --root /tmp", `{"args":["--root","/tmp"],"command":"mcp-fs","env":[],"name":"fs"}`},
		{"git=mcp-git", `{"args":[],"command":"mcp-git","env":[],"name":"git"}`},
		{`{"type":"sse","name":"x","url":"http://h"}`, `{"type":"sse","name":"x","url":"http://h"}`},
	} {
		got, err := mcpServer(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "noequals", "=https://x", "name=", `{"url":"x"}`, `{nope`} {
		if _, err := mcpServer(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestACPSessionMCP(t *testing.T) {
	cfg, err := parseACP(t, "--mcp", "api=https://example.com/mcp").config()
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "c1"})
	if err != nil || p.Cwd != "" || len(p.MCPServers) != 1 {
		t.Fatalf("session = %+v, %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(p.MCPServers[0], &m); err != nil || m["url"] != "https://example.com/mcp" {
		t.Fatalf("mcp = %s", p.MCPServers[0])
	}
}

func TestPerContextDir(t *testing.T) {
	o := parseACP(t, "--per-context", "dir")
	o.dir, o.sessionsDir = t.TempDir(), t.TempDir()
	cfg, err := o.config()
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "ctx/../a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Cwd == b.Cwd || filepath.Dir(a.Cwd) != o.sessionsDir || filepath.Dir(b.Cwd) != o.sessionsDir {
		t.Fatalf("cwds %q %q, want two folders in %s", a.Cwd, b.Cwd, o.sessionsDir)
	}
	if st, err := os.Stat(a.Cwd); err != nil || !st.IsDir() {
		t.Fatalf("%s: %v", a.Cwd, err)
	}
	// Loading the session again (after a restart) reuses the folder.
	again, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "b"})
	if err != nil || again.Cwd != b.Cwd {
		t.Fatalf("again: %q, %v", again.Cwd, err)
	}
}

func TestPerContextWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "a")

	o := parseACP(t, "--per-context", "worktree")
	o.dir, o.sessionsDir = repo, t.TempDir()
	cfg, err := o.config()
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "c-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Cwd, "a.txt")); err != nil {
		t.Fatalf("the worktree has no checkout: %v", err)
	}
	if b := git("-C", p.Cwd, "branch", "--show-current"); b != "lagrange/c-1" {
		t.Fatalf("branch = %q", b)
	}
	// The worktree is gone but its branch stays: it is checked out again.
	git("worktree", "remove", "--force", p.Cwd)
	if p2, err := cfg.Session(context.Background(), &a2asrv.ExecutorContext{ContextID: "c-1"}); err != nil || p2.Cwd != p.Cwd {
		t.Fatalf("again: %+v, %v", p2, err)
	}
}

func TestACPDescribe(t *testing.T) {
	o := parseACP(t, "--allow", "*", "--model", "sonnet", "--per-context", "worktree")
	if d := o.describe(); !strings.Contains(d, "without asking") || !strings.Contains(d, "sonnet") || !strings.Contains(d, "worktree") {
		t.Fatalf("describe = %q", d)
	}
	if d := parseACP(t).describe(); !strings.Contains(d, "asks before") {
		t.Fatalf("describe = %q", d)
	}
}
