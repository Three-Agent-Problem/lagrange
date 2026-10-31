package main

import (
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

func TestEchoAgent(t *testing.T) {
	srv := httptest.NewServer(echoHandler(""))
	t.Cleanup(srv.Close)
	c, err := a2aclient.NewFromEndpoints(t.Context(), []*a2a.AgentInterface{a2a.NewAgentInterface(srv.URL+"/rpc", a2a.TransportProtocolJSONRPC)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		parts []*a2a.Part
		want  string
	}{
		{[]*a2a.Part{a2a.NewTextPart("Hello!")}, "Hello!"},
		{[]*a2a.Part{a2a.NewDataPart(map[string]any{"n": 1})}, `{"n":1}`},
	} {
		res, err := c.SendMessage(t.Context(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, tc.parts...)})
		if err != nil {
			t.Fatal(err)
		}
		task, ok := res.(*a2a.Task)
		if !ok || task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("result = %#v", res)
		}
		if got := task.Status.Message.Parts[0].Text(); got != "You said: "+tc.want {
			t.Errorf("reply = %q", got)
		}
		if len(task.Artifacts) != 1 || task.Artifacts[0].Name != "echo" || task.Artifacts[0].Parts[0].Text() != tc.want {
			t.Errorf("artifacts = %+v", task.Artifacts)
		}
	}
}
