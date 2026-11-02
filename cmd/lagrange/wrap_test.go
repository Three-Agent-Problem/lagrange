package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
)

func wrapClient(t *testing.T, h http.Handler) *a2aclient.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := a2aclient.NewFromEndpoints(t.Context(), []*a2a.AgentInterface{a2a.NewAgentInterface(srv.URL+"/rpc", a2a.TransportProtocolJSONRPC)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sendMsg(t *testing.T, c *a2aclient.Client, msg *a2a.Message) *a2a.Task {
	t.Helper()
	res, err := c.SendMessage(t.Context(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("result = %#v", res)
	}
	return task
}

func TestExecWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	card := wrapCard("Shout", "Shouts.", "")
	t.Run("text", func(t *testing.T) {
		c := wrapClient(t, wrapHandler(card, execFunc("tr a-z A-Z", time.Minute)))
		task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")))
		if task.Status.State != a2a.TaskStateCompleted || task.Artifacts[0].Parts[0].Text() != "HELLO" {
			t.Fatalf("task = %s %+v", task.Status.State, task.Artifacts)
		}
	})
	t.Run("json and env", func(t *testing.T) {
		c := wrapClient(t, wrapHandler(card, execFunc(`printf '{"output":"got %s","n":%s}' "$(cat)" "$LAGRANGE_DATA"`, time.Minute)))
		task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("x"), a2a.NewDataPart(map[string]any{"a": 1})))
		parts := task.Artifacts[0].Parts
		if parts[0].Text() != "got x" || parts[1].Data().(map[string]any)["n"].(map[string]any)["a"] != float64(1) {
			t.Fatalf("parts = %+v", parts)
		}
	})
	t.Run("ask then answer", func(t *testing.T) {
		script := `if [ "$LAGRANGE_ANSWER" = 1 ]; then echo "ok: $(cat)"; else echo '{"ask":"Approve?","schema":{"type":"object","properties":{"approved":{"type":"boolean"}}}}'; fi`
		c := wrapClient(t, wrapHandler(card, execFunc(script, time.Minute)))
		task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("pay 50")))
		if task.Status.State != a2a.TaskStateInputRequired {
			t.Fatalf("state = %s", task.Status.State)
		}
		if req, err := structuredinput.ParseRequest(task.Status.Message); err != nil || req == nil {
			t.Fatalf("no schema request: %v", err)
		}
		task = sendMsg(t, c, a2a.NewMessageForTask(a2a.MessageRoleUser, task, a2a.NewTextPart("yes")))
		if task.Status.State != a2a.TaskStateCompleted || task.Artifacts[len(task.Artifacts)-1].Parts[0].Text() != "ok: yes" {
			t.Fatalf("task = %s %+v", task.Status.State, task.Artifacts)
		}
	})
	t.Run("failure", func(t *testing.T) {
		c := wrapClient(t, wrapHandler(card, execFunc("echo broken >&2; exit 3", time.Minute)))
		task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("x")))
		if task.Status.State != a2a.TaskStateFailed || !strings.Contains(task.Status.Message.Parts[0].Text(), "broken") {
			t.Fatalf("task = %s %+v", task.Status.State, task.Status.Message)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		c := wrapClient(t, wrapHandler(card, execFunc("sleep 5", 100*time.Millisecond)))
		task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("x")))
		if task.Status.State != a2a.TaskStateFailed || !strings.Contains(task.Status.Message.Parts[0].Text(), "longer than") {
			t.Fatalf("task = %s %+v", task.Status.State, task.Status.Message)
		}
	})
}

func TestHTTPWrapper(t *testing.T) {
	var got []wrapInput
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Key") != "k1" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var in wrapInput
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		got = append(got, in)
		switch {
		case in.Text == "plain":
			_, _ = io.WriteString(w, "just text")
		case in.Answer:
			_ = json.NewEncoder(w).Encode(map[string]any{"output": "thanks, " + in.Text})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ask": "Which size?"})
		}
	}))
	t.Cleanup(upstream.Close)
	header := http.Header{"X-Key": {"k1"}}
	c := wrapClient(t, wrapHandler(wrapCard("Flow", "A flow.", ""), httpFunc(upstream.URL, header, time.Minute)))

	task := sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("plain")))
	if task.Status.State != a2a.TaskStateCompleted || task.Artifacts[0].Parts[0].Text() != "just text" {
		t.Fatalf("plain: %s %+v", task.Status.State, task.Artifacts)
	}

	task = sendMsg(t, c, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("order hoodies")))
	if task.Status.State != a2a.TaskStateInputRequired || task.Status.Message.Parts[0].Text() != "Which size?" {
		t.Fatalf("ask: %s %+v", task.Status.State, task.Status.Message)
	}
	task = sendMsg(t, c, a2a.NewMessageForTask(a2a.MessageRoleUser, task, a2a.NewTextPart("L")))
	if task.Status.State != a2a.TaskStateCompleted || task.Status.Message.Parts[0].Text() != "thanks, L" {
		t.Fatalf("answer: %s %+v", task.Status.State, task.Status.Message)
	}
	last := got[len(got)-1]
	if !last.Answer || last.TaskID != string(task.ID) || last.ContextID != task.ContextID {
		t.Errorf("answer input = %+v", last)
	}

	bad := wrapClient(t, wrapHandler(wrapCard("Flow", "A flow.", ""), httpFunc(upstream.URL, nil, time.Minute)))
	task = sendMsg(t, bad, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("x")))
	if task.Status.State != a2a.TaskStateFailed || !strings.Contains(task.Status.Message.Parts[0].Text(), "401") {
		t.Fatalf("401: %s %+v", task.Status.State, task.Status.Message)
	}
}
