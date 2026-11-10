package main

import (
	"context"
	"io"
	"iter"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/code"
)

// committer commits one file per task in its context's worktree, as a coding agent would.
type committer struct{ base string }

func (c committer) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		dir, err := contextDir(ctx, "worktree", "", c.base, ec.ContextID)
		if err != nil {
			yield(nil, err)
			return
		}
		name := ec.Message.Parts[0].Text()
		_ = os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644)
		for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "add " + name}} {
			if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
				yield(nil, err)
				return
			}
		}
		_ = yield(a2a.NewSubmittedTask(ec, ec.Message), nil) &&
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
	}
}

func (committer) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(func(a2a.Event, error) bool) {}
}

func TestCommitsPublished(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo, base := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "start"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	// contextDir needs the repo for the first worktree: point the fake at it.
	inner := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		if _, err := contextDir(ctx, "worktree", repo, base, ec.ContextID); err != nil {
			return func(yield func(a2a.Event, error) bool) { yield(nil, err) }
		}
		return committer{base}.Execute(ctx, ec)
	})
	c := &commits{AgentExecutor: inner, repo: repo, base: base, seen: map[string]string{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	run := func(text string) []code.Commit {
		ec := &a2asrv.ExecutorContext{TaskID: a2a.NewTaskID(), ContextID: "ctx-1", Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))}
		var got []code.Commit
		var last a2a.Event
		for ev, err := range c.Execute(t.Context(), ec) {
			if err != nil {
				t.Fatal(err)
			}
			if a, ok := ev.(*a2a.TaskArtifactUpdateEvent); ok {
				cm, _, _ := code.Find(a.Artifact.Parts)
				got = append(got, cm)
			}
			last = ev
		}
		if st, ok := last.(*a2a.TaskStatusUpdateEvent); !ok || st.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("last event %T, want the completion after the commits", last)
		}
		return got
	}
	first := run("a.txt")
	if len(first) != 1 || first[0].Subject != "add a.txt" || first[0].Branch != "lagrange/ctx-1" {
		t.Fatalf("first task's commits = %+v", first)
	}
	second := run("b.txt") // only the new one
	if len(second) != 1 || second[0].Subject != "add b.txt" || second[0].Parents[0] != first[0].SHA {
		t.Fatalf("second task's commits = %+v", second)
	}
}
