package pg_test

import (
	"context"
	"errors"
	"iter"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/a2aproject/a2a-go/v2/a2asrv/workqueue"

	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// as returns ctx with an a2asrv CallContext for user on tenant, the way a transport
// sets it up.
func as(ctx context.Context, user, tenant string) context.Context {
	ctx, callCtx := a2asrv.NewCallContext(ctx, nil)
	callCtx.User = a2asrv.NewAuthenticatedUser(user, nil)
	return withTenant(ctx, tenant)
}

// withTenant runs ctx through a tiny a2a-go handler call so the CallContext carries the
// tenant (a2asrv sets it only on its own path).
func withTenant(ctx context.Context, tenant string) context.Context {
	var got context.Context
	h := a2asrv.NewHandler(a2asrv.AgentExecutorFunc(nil), a2asrv.WithCallInterceptors(interceptor(func(c context.Context) { got = c })))
	_, _ = h.GetTask(ctx, &a2a.GetTaskRequest{ID: "probe", Tenant: tenant})
	return got
}

type interceptor func(context.Context)

func (f interceptor) Before(ctx context.Context, _ *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	f(ctx)
	return ctx, nil, errors.New("probe")
}

func (f interceptor) After(context.Context, *a2asrv.CallContext, *a2asrv.Response) error { return nil }

func newTask(id string) *a2a.Task {
	return &a2a.Task{ID: a2a.TaskID(id), ContextID: "ctx-" + id, Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted}}
}

func TestTaskStore(t *testing.T) {
	ctx := context.Background()
	store := pg.NewTaskStore(pgtest.Open(t))
	alice := as(ctx, "alice", "shop")

	v1, err := store.Create(alice, newTask("t1"))
	if err != nil || v1 != 1 {
		t.Fatalf("Create = %d, %v", v1, err)
	}
	if _, err := store.Create(alice, newTask("t1")); !errors.Is(err, taskstore.ErrTaskAlreadyExists) {
		t.Fatalf("second Create: %v, want ErrTaskAlreadyExists", err)
	}

	got, err := store.Get(alice, "t1")
	if err != nil || got.User != "alice" || got.Task.ContextID != "ctx-t1" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	for name, other := range map[string]context.Context{
		"other user":   as(ctx, "bob", "shop"),
		"other tenant": as(ctx, "alice", "bank"),
	} {
		if _, err := store.Get(other, "t1"); !errors.Is(err, a2a.ErrTaskNotFound) {
			t.Errorf("Get by %s: %v, want ErrTaskNotFound", name, err)
		}
	}

	working := newTask("t1")
	working.Status.State = a2a.TaskStateWorking
	v2, err := store.Update(alice, &taskstore.UpdateRequest{Task: working, PrevVersion: v1, Event: working})
	if err != nil || v2 != 2 {
		t.Fatalf("Update = %d, %v", v2, err)
	}
	if _, err := store.Update(alice, &taskstore.UpdateRequest{Task: working, PrevVersion: v1, Event: working}); !errors.Is(err, taskstore.ErrConcurrentModification) {
		t.Fatalf("stale Update: %v, want ErrConcurrentModification", err)
	}
	if _, err := store.Update(as(ctx, "bob", "shop"), &taskstore.UpdateRequest{Task: working, Event: working}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("Update by another user: %v, want ErrTaskNotFound", err)
	}
	loaded, tenant, err := store.Load(ctx, "t1")
	if err != nil || tenant != "shop" || loaded.Task.Status.State != a2a.TaskStateWorking || loaded.Version != 2 {
		t.Fatalf("Load = %+v %q, %v", loaded, tenant, err)
	}
}

func TestTaskStoreList(t *testing.T) {
	ctx := context.Background()
	store := pg.NewTaskStore(pgtest.Open(t))
	alice := as(ctx, "alice", "shop")
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if _, err := store.Create(alice, newTask(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(as(ctx, "bob", "shop"), newTask("bobs")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.List(as(ctx, "", "shop"), &a2a.ListTasksRequest{}); !errors.Is(err, a2a.ErrUnauthenticated) {
		t.Fatalf("anonymous List: %v, want ErrUnauthenticated", err)
	}
	var seen []a2a.TaskID
	req := &a2a.ListTasksRequest{PageSize: 2}
	for pages := 0; ; pages++ {
		resp, err := store.List(alice, req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.TotalSize != 5 {
			t.Fatalf("TotalSize = %d, want 5", resp.TotalSize)
		}
		for _, task := range resp.Tasks {
			seen = append(seen, task.ID)
		}
		if resp.NextPageToken == "" {
			break
		}
		if pages > 5 {
			t.Fatal("pagination does not end")
		}
		req.PageToken = resp.NextPageToken
	}
	if len(seen) != 5 || seen[0] != "e" || seen[4] != "a" {
		t.Fatalf("listed %v, want e..a", seen)
	}
}

func TestPushStore(t *testing.T) {
	ctx := context.Background()
	store := pg.NewPushStore(pgtest.Open(t))
	saved, err := store.Save(ctx, "t1", &a2a.PushConfig{URL: "https://example.com/hook", Token: "x"})
	if err != nil || saved.ID == "" || saved.TaskID != "t1" {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	if _, err := store.Save(ctx, "t1", &a2a.PushConfig{URL: "not a url"}); !errors.Is(err, a2a.ErrInvalidParams) {
		t.Fatalf("Save bad URL: %v", err)
	}
	if got, err := store.Get(ctx, "t1", saved.ID); err != nil || got.Token != "x" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if list, err := store.List(ctx, "t1"); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if err := store.DeleteAll(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "t1", saved.ID); !errors.Is(err, push.ErrPushConfigNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}

func TestWorkQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := pgtest.Open(t)
	q := pg.NewWorkQueueBackend(db, "w1", pg.WorkQueueConfig{Lease: time.Second, MaxAttempts: 2, Poll: 50 * time.Millisecond})

	payload := &workqueue.Payload{Type: workqueue.PayloadTypeExecute, TaskID: "t1"}
	if _, err := q.Write(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Write(ctx, payload); err == nil {
		t.Fatal("a second execution for the same task was accepted")
	}

	msg, err := q.Read(ctx)
	if err != nil || msg.Payload().TaskID != "t1" {
		t.Fatalf("Read = %v, %v", msg, err)
	}
	// A worker that stops heartbeating loses the item when the lease runs out.
	again, err := q.Read(ctx)
	if err != nil || again.Payload().TaskID != "t1" {
		t.Fatalf("reclaim Read = %v, %v", again, err)
	}
	// Two attempts are used up, so returning it fails the item for good.
	if err := again.Return(ctx, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM work_items WHERE task_id = 't1'").Scan(&state); err != nil || state != "failed" {
		t.Fatalf("state = %q, %v, want failed", state, err)
	}

	// Once nothing is pending for the task, it can be executed again.
	if _, err := q.Write(ctx, payload); err != nil {
		t.Fatal(err)
	}
	msg, err = q.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := msg.Complete(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestClusterMode runs a real a2a-go handler in cluster mode on these stores: a
// blocking send, a streaming send and an INPUT_REQUIRED round trip.
func TestClusterMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := pgtest.Open(t)

	exec := a2asrv.AgentExecutorFunc(func(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if execCtx.StoredTask == nil {
				if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
					return
				}
			}
			text := ""
			if len(execCtx.Message.Parts) > 0 {
				text = execCtx.Message.Parts[0].Text()
			}
			if text == "ask" {
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateInputRequired,
					a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart("which one?"))), nil)
				return
			}
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted,
				a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart("done: "+text))), nil)
		}
	})
	handler := a2asrv.NewHandler(exec, a2asrv.WithClusterMode(a2asrv.ClusterConfig{
		TaskStore:    pg.NewTaskStore(db),
		QueueManager: pg.NewEventQueueManager(db, 20*time.Millisecond),
		WorkQueue:    pg.NewWorkQueue(db, "w1", pg.WorkQueueConfig{Poll: 50 * time.Millisecond}),
	}))
	srv := httptest.NewServer(a2asrv.NewJSONRPCHandler(handler))
	defer srv.Close()
	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{a2a.NewAgentInterface(srv.URL, a2a.TransportProtocolJSONRPC)})
	if err != nil {
		t.Fatal(err)
	}

	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := res.(*a2a.Task)
	if !ok || task.Status.State != a2a.TaskStateCompleted || task.Status.Message.Parts[0].Text() != "done: hello" {
		t.Fatalf("SendMessage = %#v", res)
	}

	var states []a2a.TaskState
	for ev, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("ask"))}) {
		if err != nil {
			t.Fatal(err)
		}
		switch ev := ev.(type) {
		case *a2a.Task:
			task = ev
			states = append(states, ev.Status.State)
		case *a2a.TaskStatusUpdateEvent:
			states = append(states, ev.Status.State)
		}
	}
	if len(states) == 0 || states[len(states)-1] != a2a.TaskStateInputRequired {
		t.Fatalf("streamed states %v, want ... INPUT_REQUIRED", states)
	}

	reply := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("the blue one"))
	reply.TaskID = task.ID
	res, err = client.SendMessage(ctx, &a2a.SendMessageRequest{Message: reply})
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := res.(*a2a.Task); !ok || done.Status.State != a2a.TaskStateCompleted || done.Status.Message.Parts[0].Text() != "done: the blue one" {
		t.Fatalf("resume = %#v", res)
	}
}
