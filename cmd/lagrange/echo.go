package main

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// echoHandler is a built-in test agent for `lagrange tunnel --echo`: it answers every
// message with what it was sent, so a new address can be tried before any agent code
// exists. Each message becomes a task with an "echo" artifact.
func echoHandler(name string) http.Handler {
	if name == "" {
		name = "Echo"
	}
	exec := a2asrv.AgentExecutorFunc(func(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if ec.StoredTask == nil && !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			said := echoText(ec.Message)
			art := a2a.NewArtifactEvent(ec, a2a.NewTextPart(said))
			art.Artifact.Name = "echo"
			if !yield(art, nil) {
				return
			}
			reply := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("You said: "+said))
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, reply), nil)
		}
	})
	card := &a2a.AgentCard{
		Name:                name,
		Description:         "A test agent built into lagrange: it answers every message with what it was sent.",
		Version:             "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:   []string{"text/plain", "application/json"},
		DefaultOutputModes:  []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID: "echo", Name: "Echo", Description: "Repeats the message back.", Tags: []string{"test", "echo"},
			Examples: []string{"Hello!"},
		}},
	}
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec)))
	return mux
}

// echoText is a message's text, or its data as JSON when it has no text.
func echoText(m *a2a.Message) string {
	var out []string
	for _, p := range m.Parts {
		switch {
		case p.Text() != "":
			out = append(out, p.Text())
		case p.Data() != nil:
			raw, _ := json.Marshal(p.Data())
			out = append(out, string(raw))
		}
	}
	if len(out) == 0 {
		return "(nothing)"
	}
	return strings.Join(out, " ")
}
