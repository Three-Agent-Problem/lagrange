package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"

	"github.com/three-agent-problem/lagrange/acp"
	"github.com/three-agent-problem/lagrange/code"
)

// acpOptions configures a coding agent served with tunnel --acp.
type acpOptions struct {
	argv []string
	dir  string
	name string
	// settings apply to every session, e.g. {"model": "sonnet"} (--model, --set).
	settings map[string]any
	// callerSettings are the setting IDs (patterns) a sender may change per message
	// through {"acpSettings": {...}} in its metadata (--caller-settings).
	callerSettings []string
	allow, deny    []string          // tool kinds answered without asking (--allow, --deny)
	mcp            []json.RawMessage // MCP servers of every session (--mcp)
	perContext     string            // "", "dir" or "worktree" (--per-context)
	thoughts       bool              // stream the agent's thinking too (--thoughts)
	sessionsDir    string            // where per-context folders go; defaults to the cache directory
}

// acpFlags registers tunnel's --acp flags (except --acp, --dir and --name).
func acpFlags(fs *flag.FlagSet, o *acpOptions) {
	o.settings = map[string]any{}
	fs.Func("model", `With --acp: the model of every session, e.g. "haiku", "sonnet" or "opus" for claude-agent-acp.`, func(v string) error {
		o.settings["model"] = v
		return nil
	})
	fs.Func("set", `With --acp: a session setting, "id=value" (repeatable), e.g. --set mode=plan. A value that is JSON (true, 3, {...}) is sent as JSON.`, func(v string) error {
		id, val, ok := strings.Cut(v, "=")
		if !ok || strings.TrimSpace(id) == "" {
			return fmt.Errorf(`want "id=value", got %q`, v)
		}
		o.settings[strings.TrimSpace(id)] = settingValue(val)
		return nil
	})
	fs.Func("caller-settings", `With --acp: settings a sender may change per message with {"acpSettings": {...}} in the message metadata, comma separated, * matches anything (e.g. "model").`, func(v string) error {
		o.callerSettings = append(o.callerSettings, splitList(v)...)
		return nil
	})
	fs.Func("allow", `With --acp: tool kinds approved without asking, comma separated or repeated: read, edit, delete, move, search, execute, think, fetch, other, or * for all.`, func(v string) error {
		o.allow = append(o.allow, splitList(v)...)
		return nil
	})
	fs.Func("deny", `With --acp: tool kinds rejected without asking (wins over --allow).`, func(v string) error {
		o.deny = append(o.deny, splitList(v)...)
		return nil
	})
	fs.Func("mcp", `With --acp: an MCP server for every session (repeatable): "name=https://host/mcp" (HTTP), "name=command arg..." (stdio), or the ACP McpServer JSON object.`, func(v string) error {
		raw, err := mcpServer(v)
		if err != nil {
			return err
		}
		o.mcp = append(o.mcp, raw)
		return nil
	})
	fs.BoolVar(&o.thoughts, "thoughts", false, `With --acp: stream the agent's thinking into a "thoughts" artifact, so every client can follow it (each chunk is an activity entry).`)
	fs.Func("per-context", `With --acp: give each conversation (A2A context) its own working directory: "dir" (an empty folder) or "worktree" (a git worktree of --dir on its own branch, lagrange/<context>).`, func(v string) error {
		if v != "dir" && v != "worktree" && v != "none" {
			return fmt.Errorf(`want "dir" or "worktree", got %q`, v)
		}
		if v == "none" {
			v = ""
		}
		o.perContext = v
		return nil
	})
}

// settingValue reads a --set value: JSON if it parses as a JSON value other than a
// string, else the text itself.
func settingValue(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		if _, isString := out.(string); !isString {
			return out
		}
	}
	return v
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// mcpServer turns a --mcp value into the ACP McpServer JSON form.
func mcpServer(v string) (json.RawMessage, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, fmt.Errorf("--mcp: %w", err)
		}
		if name, _ := m["name"].(string); name == "" {
			return nil, errors.New(`--mcp: the JSON needs a "name"`)
		}
		return json.RawMessage(v), nil
	}
	name, target, ok := strings.Cut(v, "=")
	name, target = strings.TrimSpace(name), strings.TrimSpace(target)
	if !ok || name == "" || target == "" {
		return nil, fmt.Errorf(`--mcp: want "name=https://..." or "name=command args", got %q`, v)
	}
	var server map[string]any
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		server = map[string]any{"type": "http", "name": name, "url": target, "headers": []any{}}
	} else {
		f := strings.Fields(target)
		args := []string{}
		args = append(args, f[1:]...)
		server = map[string]any{"name": name, "command": f[0], "args": args, "env": []any{}}
	}
	raw, err := json.Marshal(server)
	return raw, err
}

// config builds the executor's configuration from the options.
func (o *acpOptions) config() (*a2aacp.Config, error) {
	// Tool call starts, outputs and usage reach the activity log (lagrange/acp).
	cfg := &a2aacp.Config{Converter: acp.Converter(nil, acp.Options{Thoughts: o.thoughts})}
	if len(o.allow) > 0 || len(o.deny) > 0 {
		cfg.AutoAnswer = a2aacp.ToolRules{Allow: o.allow, Deny: o.deny}
	}
	if len(o.settings) > 0 || len(o.callerSettings) > 0 {
		fixed := maps.Clone(o.settings)
		var fromCaller func(context.Context, *a2asrv.ExecutorContext) (map[string]any, error)
		if len(o.callerSettings) > 0 {
			fromCaller = a2aacp.MetadataSettings(o.callerSettings...)
		}
		cfg.Settings = func(ctx context.Context, ec *a2asrv.ExecutorContext) (map[string]any, error) {
			out := maps.Clone(fixed)
			if fromCaller != nil {
				m, err := fromCaller(ctx, ec)
				if err != nil {
					return nil, err
				}
				maps.Copy(out, m)
			}
			return out, nil
		}
	}
	if len(o.mcp) > 0 || o.perContext != "" {
		base := o.sessionsDir
		if base == "" && o.perContext != "" {
			dir, err := os.UserCacheDir()
			if err != nil {
				return nil, err
			}
			abs, err := filepath.Abs(o.dir)
			if err != nil {
				return nil, err
			}
			base = filepath.Join(dir, "lagrange", "contexts", filepath.Base(abs))
		}
		o.sessionsDir = base
		var mu sync.Mutex // one worktree at a time: git locks the repository
		cfg.Session = func(ctx context.Context, ec *a2asrv.ExecutorContext) (a2aacp.SessionParams, error) {
			p := a2aacp.SessionParams{MCPServers: o.mcp}
			if o.perContext == "" {
				return p, nil
			}
			mu.Lock()
			defer mu.Unlock()
			cwd, err := contextDir(ctx, o.perContext, o.dir, base, ec.ContextID)
			if err != nil {
				return a2aacp.SessionParams{}, err
			}
			p.Cwd = cwd
			return p, nil
		}
	}
	return cfg, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// contextDir returns the working directory of a context, creating it if needed:
// a folder, or a git worktree of repo on the branch lagrange/<context>.
func contextDir(ctx context.Context, mode, repo, base, contextID string) (string, error) {
	name := unsafeName.ReplaceAllString(contextID, "_")
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("context ID %q can't name a directory", contextID)
	}
	dir := filepath.Join(base, name)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil // a session loaded again after a restart
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if mode == "dir" {
		return dir, os.Mkdir(dir, 0o755)
	}
	branch := "lagrange/" + name
	args := []string{"-C", repo, "worktree", "add", "-b", branch, dir, "HEAD"}
	if exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
		args = []string{"-C", repo, "worktree", "add", dir, branch} // the branch outlived its worktree
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree for context %s: %v: %s", contextID, err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// describe sums up the options for the agent's card.
func (o *acpOptions) describe() string {
	var b strings.Builder
	switch {
	case len(o.allow) == 1 && o.allow[0] == "*" && len(o.deny) == 0:
		b.WriteString(" It runs tools without asking.")
	case len(o.allow) > 0:
		b.WriteString(" It runs " + strings.Join(o.allow, ", ") + " tools without asking and asks before the rest.")
	default:
		b.WriteString(" It asks before running tools.")
	}
	if m, ok := o.settings["model"].(string); ok {
		b.WriteString(" Model: " + m + ".")
	}
	switch o.perContext {
	case "dir":
		b.WriteString(" Each conversation gets its own folder.")
	case "worktree":
		b.WriteString(" Each conversation gets its own git worktree and branch.")
	}
	return b.String()
}

// acpHandler serves a coding agent as an A2A agent, for the tunnel. Its permission
// prompts become structured input requests, which land in the sender's inbox.
func acpHandler(ctx context.Context, o *acpOptions, logger *slog.Logger) (http.Handler, func(), error) {
	if o.perContext == "worktree" {
		if out, err := exec.CommandContext(ctx, "git", "-C", o.dir, "rev-parse", "HEAD").CombinedOutput(); err != nil {
			return nil, nil, fmt.Errorf("--per-context worktree needs --dir to be a git repository with a commit: %s", strings.TrimSpace(string(out)))
		}
	}
	cfg, err := o.config()
	if err != nil {
		return nil, nil, err
	}
	exec, err := a2aacp.NewExecutor(ctx, a2aacp.Process{Argv: o.argv, Dir: o.dir, Stderr: os.Stderr}, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("starting %s: %w", o.argv[0], err)
	}
	var served a2asrv.AgentExecutor = exec
	if o.perContext == "worktree" {
		served = &commits{AgentExecutor: exec, repo: o.dir, base: o.sessionsDir, seen: map[string]string{}, logger: logger}
	}
	name := o.name
	if name == "" {
		name = o.argv[0]
	}
	card := &a2a.AgentCard{
		Name:        name,
		Description: "A coding agent (" + strings.Join(o.argv, " ") + ") connected through a Lagrange tunnel." + o.describe(),
		Version:     "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC),
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:  true,
			Extensions: []a2a.AgentExtension{structuredinput.NewExtension("")},
		},
		DefaultInputModes:  []string{"text/plain", "image/png", "image/jpeg"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID: "code", Name: "Code", Description: "Reads, writes and runs code in its workspace.",
			Tags: []string{"coding", "acp"},
		}},
	}
	h := a2asrv.NewHandler(served)
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(h))
	return mux, func() {
		if err := exec.Close(); err != nil {
			logger.Warn("stopping the agent", "error", err)
		}
	}, nil
}

// commits publishes the commits an agent makes in its worktree, as lagrange/code
// commit artifacts of the task that made them, just before the task ends or waits.
type commits struct {
	a2asrv.AgentExecutor
	repo, base string
	logger     *slog.Logger

	mu   sync.Mutex
	seen map[string]string // context ID -> the last commit published (or the start)
}

func (c *commits) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		name := unsafeName.ReplaceAllString(ec.ContextID, "_")
		dir := filepath.Join(c.base, name)
		c.mu.Lock()
		if c.seen[ec.ContextID] == "" {
			// Before the first task the worktree doesn't exist: it starts at the repo's HEAD.
			from := dir
			if _, err := os.Stat(dir); err != nil {
				from = c.repo
			}
			c.seen[ec.ContextID] = revParse(ctx, from, "HEAD")
		}
		c.mu.Unlock()
		for ev, err := range c.AgentExecutor.Execute(ctx, ec) {
			if st, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && err == nil && (st.Status.State.Terminal() || st.Status.State == a2a.TaskStateInputRequired || st.Status.State == a2a.TaskStateAuthRequired) {
				if !c.publish(ctx, ec, dir, "lagrange/"+name, yield) {
					return
				}
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}

// publish yields an artifact for each commit since the last one published.
func (c *commits) publish(ctx context.Context, ec *a2asrv.ExecutorContext, dir, branch string, yield func(a2a.Event, error) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	from := c.seen[ec.ContextID]
	if from == "" {
		return true
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--reverse", from+"..HEAD").Output()
	if err != nil {
		c.logger.Warn("listing the agent's commits", "dir", dir, "error", err)
		return true
	}
	for _, sha := range strings.Fields(string(out)) {
		cm, patch, err := code.FromGit(ctx, dir, sha)
		if err != nil {
			c.logger.Warn("reading a commit", "sha", sha, "error", err)
			continue
		}
		cm.Branch = branch
		if !yield(code.Event(ec, cm, patch), nil) {
			return false
		}
		c.seen[ec.ContextID] = sha
	}
	return true
}

func revParse(ctx context.Context, dir, rev string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", rev).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
