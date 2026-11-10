package platform_test

import (
	"context"
	"fmt"
	"iter"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/code"
	"github.com/three-agent-problem/lagrange/relay"
)

// coder acts like a coding agent served through lagrange/acp: a tool call starts and
// finishes, and it reports usage, cumulative over its session (the context).
func coder(cost *float64) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			status := func(parts ...*a2a.Part) bool {
				return yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, parts...)), nil)
			}
			usage := func() bool {
				*cost += 0.25
				return status(a2a.NewDataPart(map[string]any{"usage": map[string]any{
					"used": 1000, "size": 200000, "cost": map[string]any{"amount": *cost, "currency": "USD"}}}))
			}
			tool := func(verb, state string, extra map[string]any) bool {
				tc := map[string]any{"id": "call-1", "title": "go test ./...", "kind": "execute", "status": state}
				for k, v := range extra {
					tc[k] = v
				}
				return status(a2a.NewTextPart(verb+" go test ./..."), a2a.NewDataPart(map[string]any{"toolCall": tc}))
			}
			_ = yield(a2a.NewSubmittedTask(ec, ec.Message), nil) &&
				tool("Running", "in_progress", map[string]any{"input": map[string]any{"command": "go test ./..."}}) &&
				tool("Finished", "completed", map[string]any{"output": "ok  ./..."}) &&
				usage() && usage() &&
				yield(code.Event(ec, code.Commit{SHA: "3f2a1c9e00", Branch: "lagrange/x", Subject: "fix the tests", Insertions: 4, Deletions: 1,
					Files: []code.File{{Path: "a.go", Insertions: 4, Deletions: 1}}}, "--- a/a.go\n+++ b/a.go\n"), nil) &&
				yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart("done")), nil) &&
				yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
		}
	})
}

func TestAgentEvents(t *testing.T) {
	e := newEnv(t, "")
	ctx := t.Context()
	var spent float64
	e.connect(t, "coder", e.agent(t, "coder"), agentServer("coder", coder(&spent)), 2)
	c := e.client(t, "coder", e.agent(t, "lead"), "")

	send := func(contextID string) *a2a.Task {
		msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("run the tests"))
		msg.ContextID = contextID
		msg.Metadata = map[string]any{relay.TagsKey: map[string]any{"subtask": "S3", "run": "r1"}}
		task := asTask(t)(c.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg}))
		if task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("task: %s", task.Status.State)
		}
		return task
	}
	first := send("")
	entries, err := e.p.Activity.Task(ctx, string(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	var created, running, finished, commit *activity.Entry
	var usage []*activity.Entry
	for _, x := range entries {
		tc, _ := x.Data["toolCall"].(map[string]any)
		switch {
		case x.Kind == activity.KindTask && created == nil:
			created = x
		case tc != nil && tc["status"] == "in_progress":
			running = x
		case tc != nil && tc["status"] == "completed":
			finished = x
		case x.Kind == activity.KindUsage:
			usage = append(usage, x)
		case x.Kind == activity.KindArtifact && x.Data["commit"] != nil:
			commit = x
		}
	}
	if commit == nil || commit.Summary != "commit 3f2a1c9 fix the tests (+4 −1)" || commit.Data["content"] != nil {
		t.Fatalf("commit entry = %+v", commit)
	}
	if c, patch, ok := code.Find(first.Artifacts[0].Parts); !ok || c.Branch != "lagrange/x" || !strings.Contains(patch, "+++ b/a.go") {
		t.Fatalf("the task's commit artifact: %+v %q", c, patch)
	}
	if tags, _ := created.Data["tags"].(map[string]any); tags["subtask"] != "S3" || tags["run"] != "r1" {
		t.Fatalf("creation entry data = %v", created.Data)
	}
	if running == nil || running.Summary != "Running go test ./..." {
		t.Fatalf("no start entry: %+v", entries)
	}
	if in, _ := running.Data["toolCall"].(map[string]any)["input"].(map[string]any); in["command"] != "go test ./..." {
		t.Fatalf("start input = %v", running.Data)
	}
	if finished == nil || finished.Data["toolCall"].(map[string]any)["output"] != "ok  ./..." {
		t.Fatalf("finish entry = %+v", finished)
	}
	if len(usage) != 2 || usage[1].Summary != "usage: $0.50 · 0% of context" || usage[0].State != "" {
		t.Fatalf("usage entries = %+v", usage)
	}
	if d0, d1 := usage[0].Data["costDelta"], usage[1].Data["costDelta"]; d0 != 0.25 || d1 != 0.25 {
		t.Fatalf("deltas = %v, %v", d0, d1)
	}

	// The same session in a second run: its cost counts what it spent since.
	second := send(first.ContextID)
	ada := e.login(t, adminEmail, adminPassword)
	var runs struct {
		Runs []struct {
			ID        string  `json:"id"`
			ContextID string  `json:"contextId"`
			Cost      float64 `json:"cost"`
			Currency  string  `json:"currency"`
		} `json:"runs"`
	}
	ada.do(http.MethodGet, "/api/runs?status=done", nil, http.StatusOK, &runs)
	costs := map[string]float64{}
	for _, r := range runs.Runs {
		costs[r.ID] = r.Cost
		if r.Currency != "USD" || r.ContextID != first.ContextID {
			t.Fatalf("run %s: currency %q, context %q (want %q)", r.ID, r.Currency, r.ContextID, first.ContextID)
		}
	}
	if math.Abs(costs[string(first.ID)]-0.5) > 1e-9 || math.Abs(costs[string(second.ID)]-0.5) > 1e-9 {
		t.Fatalf("run costs = %v (the session spent %.2f in all)", costs, spent)
	}
}

// inspector answers with what it got: each part, and the message's metadata.
func inspector() a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			var got []string
			for _, p := range ec.Message.Parts {
				switch {
				case p.Text() != "":
					got = append(got, "text "+p.Text())
				case p.Raw() != nil:
					got = append(got, fmt.Sprintf("raw %s %s %d", p.Filename, p.MediaType, len(p.Raw())))
				case p.URL() != "":
					got = append(got, fmt.Sprintf("url %s %s %s", p.Filename, p.MediaType, p.URL()))
				}
			}
			if s, ok := ec.Message.Metadata["acpSettings"].(map[string]any); ok {
				got = append(got, fmt.Sprintf("model %v", s["model"]))
			}
			_ = yield(a2a.NewSubmittedTask(ec, ec.Message), nil) &&
				yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart(strings.Join(got, "; "))), nil) &&
				yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
		}
	})
}

func TestSendFiles(t *testing.T) {
	e := newEnv(t, "")
	e.connect(t, "inspector", e.agent(t, "inspector"), agentServer("inspector", inspector()), 2)
	ada := e.login(t, adminEmail, adminPassword)
	png := []byte("\x89PNG\r\n\x1a\nrest")

	var sent struct {
		Result struct{ ID string } `json:"result"`
	}
	ada.do(http.MethodPost, "/api/send", map[string]any{
		"to": "@inspector", "text": "what's on screen?",
		"files": []map[string]any{
			{"name": "shot.png", "data": png},
			{"name": "../../etc/notes", "data": []byte("plain words"), "mediaType": "text/plain"},
			{"url": "https://example.com/spec.pdf"},
		},
		"tags":     map[string]string{"subtask": "S1"},
		"metadata": map[string]any{"acpSettings": map[string]any{"model": "opus"}},
	}, http.StatusOK, &sent)

	var task *a2a.Task
	waitFor(t, "the answer", func() bool {
		stored, _, err := e.p.Tasks.Load(t.Context(), a2a.TaskID(sent.Result.ID))
		task = stored.Task
		return err == nil && task.Status.State == a2a.TaskStateCompleted
	})
	want := "text what's on screen?; raw shot.png image/png 12; raw notes text/plain 11; url  application/pdf https://example.com/spec.pdf; model opus"
	if got := artifactText(t, task); got != want {
		t.Fatalf("the agent got:\n %s\nwant:\n %s", got, want)
	}
	entries, _ := e.p.Activity.Task(t.Context(), sent.Result.ID)
	if tags, _ := entries[0].Data["tags"].(map[string]any); tags["subtask"] != "S1" {
		t.Fatalf("first entry = %+v", entries[0])
	}

	for _, bad := range []map[string]any{
		{"to": "@inspector", "files": []map[string]any{{"name": "x", "data": png, "url": "https://example.com/x"}}},
		{"to": "@inspector", "files": []map[string]any{{"name": "x"}}},
		{"to": "@inspector", "files": []map[string]any{{"url": "file:///etc/passwd"}}},
	} {
		ada.do(http.MethodPost, "/api/send", bad, http.StatusBadRequest, nil)
	}
}
