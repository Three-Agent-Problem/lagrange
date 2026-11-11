package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/tunnel"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// transcript runs CLI commands in-process and records what they print, with task IDs
// numbered in order of appearance so the output is stable.
type transcript struct {
	t   *testing.T
	out bytes.Buffer
	ids map[string]string
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func (tr *transcript) norm(s string) string {
	return uuidRE.ReplaceAllStringFunc(s, func(id string) string {
		if n, ok := tr.ids[id]; ok {
			return n
		}
		n := fmt.Sprintf("<id%d>", len(tr.ids)+1)
		tr.ids[id] = n
		return n
	})
}

// run runs one command as the person whose key is given, and returns its stdout.
func (tr *transcript) run(as, key string, args ...string) string {
	tr.t.Helper()
	tr.t.Setenv("LAGRANGE_KEY", key)
	var o, e bytes.Buffer
	stdout, stderr = &o, &e
	defer func() { stdout, stderr = os.Stdout, os.Stderr }()
	cmds := map[string]func(context.Context, []string) error{"send": send, "task": task, "answer": answer, "waiting": waiting, "runs": runs, "run": run}
	err := cmds[args[0]](tr.t.Context(), args[1:])
	code := 0
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		code = ee.code
		if ee.msg != "" {
			fmt.Fprintln(&e, "lagrange:", ee.msg)
		}
	case err != nil:
		code = 1
		fmt.Fprintln(&e, "lagrange:", err)
	}
	fmt.Fprintf(&tr.out, "$ %s: lagrange %s\n", as, tr.norm(strings.Join(args, " ")))
	tr.out.WriteString(tr.norm(o.String()))
	for _, l := range strings.Split(strings.TrimRight(tr.norm(e.String()), "\n"), "\n") {
		if l != "" {
			fmt.Fprintf(&tr.out, "! %s\n", l)
		}
	}
	fmt.Fprintf(&tr.out, "[exit %d]\n\n", code)
	return strings.TrimSpace(o.String())
}

func TestCLIVerbs(t *testing.T) {
	p, server := testServer(t)
	useConfig(t)
	t.Setenv("LAGRANGE_SERVER", server)
	pollEvery = 20 * time.Millisecond
	ctx := t.Context()
	if _, err := p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	ada, bob := keyFor(t, p, "ada", directory.RoleMember), keyFor(t, p, "bob", directory.RoleMember)
	tok, err := client.New(server, ada).CreateAgent(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(tctx, tunnel.ClientConfig{Server: server, Token: tok, Handler: echoHandler("Echo")})
	}()
	for deadline := time.Now().Add(15 * time.Second); p.Mailbox.Stats("echo").Sessions == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the echo tunnel didn't connect")
		}
	}
	tr := &transcript{t: t, ids: map[string]string{}}

	// An agent answers.
	tr.run("ada", ada, "send", "@echo", "hello", "from", "CI", "--wait")

	// A person approves while the sender waits.
	hoodies := tr.run("ada", ada, "send", "@bob", "Approve 200 hoodies for 2,800 EUR?")
	untilState(t, server, ada, hoodies, client.Waiting)
	tr.run("bob", bob, "waiting", "--mine")
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(time.Second) // after task --wait has seen it waiting
		if err := client.New(server, bob).Answer(ctx, hoodies, &client.Answer{Text: "Go ahead.", Decision: "approve"}); err != nil {
			t.Error(err)
		}
	}()
	tr.run("ada", ada, "task", hoodies, "--wait")
	<-done

	// A rejection fails the wait.
	ship := tr.run("ada", ada, "send", "@bob", "Ship the release?")
	untilState(t, server, ada, ship, client.Waiting)
	tr.run("bob", bob, "answer", ship, "--reject", "Not on a Friday.")
	untilState(t, server, ada, ship, a2a.TaskState.Terminal)
	tr.run("ada", ada, "task", ship, "--wait")

	// Timing out leaves the task waiting.
	tr.run("ada", ada, "send", "@bob", "Lunch?", "--wait", "--timeout", "1s")

	// A task that asks you ends the wait.
	tr.run("ada", ada, "send", "@ada", "Remember the milk", "--wait")

	// Structured content goes along.
	tr.run("ada", ada, "send", "@echo", "--data", `{"order":"PO-1","qty":200}`, "--wait", "--json")

	// Errors.
	tr.run("ada", ada, "send", "@nobody", "hi")
	tr.run("bob", bob, "answer", hoodies, "--approve")

	tr.run("ada", ada, "runs", "--limit", "3")
	tr.run("ada", ada, "runs", "-q", "hoodies")
	tr.run("ada", ada, "run", hoodies)
	tr.run("ada", ada, "run", hoodies, "--overlap")
	tr.run("ada", ada, "waiting")

	golden := filepath.Join("testdata", "verbs.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, tr.out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got := tr.out.String(); got != string(want) {
		t.Errorf("the output changed (run with -update if that's intended):\n%s", got)
	}
}

// untilState waits until a task's state satisfies ok, so the transcript doesn't depend on
// timing.
func untilState(t *testing.T, server, key, id string, ok func(a2a.TaskState) bool) {
	t.Helper()
	_, done, err := client.New(server, key).Follow(t.Context(), id, 15*time.Second, 20*time.Millisecond, func(tk *client.Task) bool { return ok(tk.Task.Status.State) })
	if err != nil || !done {
		t.Fatalf("task %s didn't get there: %v", id, err)
	}
}

func TestParseAll(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	wait := fs.Bool("wait", false, "")
	data := fs.String("data", "", "")
	pos := parseAll(fs, []string{"@x", "--wait", "hello", "--data", "{}", "world", "--", "--not-a-flag"})
	if !*wait || *data != "{}" || strings.Join(pos, "|") != "@x|hello|world|--not-a-flag" {
		t.Fatalf("wait=%v data=%q pos=%q", *wait, *data, pos)
	}
}
