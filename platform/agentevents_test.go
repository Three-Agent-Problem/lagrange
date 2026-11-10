package platform_test

import (
	"context"
	"iter"
	"math"
	"net/http"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/activity"
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
	var created, running, finished *activity.Entry
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
		}
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
			ID       string  `json:"id"`
			Cost     float64 `json:"cost"`
			Currency string  `json:"currency"`
		} `json:"runs"`
	}
	ada.do(http.MethodGet, "/api/runs?status=done", nil, http.StatusOK, &runs)
	costs := map[string]float64{}
	for _, r := range runs.Runs {
		costs[r.ID] = r.Cost
		if r.Currency != "USD" {
			t.Fatalf("run %s currency %q", r.ID, r.Currency)
		}
	}
	if math.Abs(costs[string(first.ID)]-0.5) > 1e-9 || math.Abs(costs[string(second.ID)]-0.5) > 1e-9 {
		t.Fatalf("run costs = %v (the session spent %.2f in all)", costs, spent)
	}
}
