package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/jsonschema-go/jsonschema"
)

// The wrappers turn things that don't speak A2A into A2A agents behind the tunnel:
// --exec runs a command per message, --http posts each message to a URL (an n8n, Zapier
// or Make webhook, a FastAPI route...). Both get the same input and give the same kind
// of reply, so a script and a workflow can be swapped.
//
// Input: the message as JSON (wrapInput). --http posts it; --exec gets the text on stdin
// and the rest in LAGRANGE_* environment variables.
//
// Reply: plain text completes the task with that text. A JSON object completes it with
// the object as data, and its "text", "reply", "output" or "answer" string as the text.
// An object with "ask" asks the sender a question instead (INPUT_REQUIRED, with
// "schema" as a JSON Schema form when given); their answer comes back as a new call
// with "answer": true and the same taskId.

// wrapInput is what a wrapped program or URL receives for each message.
type wrapInput struct {
	Text      string `json:"text"`
	Data      any    `json:"data,omitempty"`
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	Answer    bool   `json:"answer,omitempty"`
}

// wrapReply is a wrapped program's reply, parsed.
type wrapReply struct {
	Text   string
	Data   any
	Ask    string
	Schema *jsonschema.Schema
}

// wrapFunc runs one message through the wrapped thing.
type wrapFunc func(ctx context.Context, in wrapInput) (wrapReply, error)

func inputOf(ec *a2asrv.ExecutorContext) wrapInput {
	in := wrapInput{TaskID: string(ec.TaskID), ContextID: ec.ContextID, Answer: ec.StoredTask != nil}
	var texts []string
	for _, p := range ec.Message.Parts {
		switch {
		case p.Text() != "":
			texts = append(texts, p.Text())
		case p.Data() != nil && in.Data == nil:
			in.Data = p.Data()
		}
	}
	in.Text = strings.Join(texts, "\n")
	return in
}

// parseReply reads a reply body: JSON when it is an object, text otherwise.
func parseReply(body []byte) (wrapReply, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return wrapReply{Text: string(trimmed)}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return wrapReply{Text: string(trimmed)}, nil
	}
	if ask, ok := obj["ask"].(string); ok && ask != "" {
		r := wrapReply{Ask: ask}
		if s, ok := obj["schema"]; ok && s != nil {
			raw, _ := json.Marshal(s)
			r.Schema = new(jsonschema.Schema)
			if err := json.Unmarshal(raw, r.Schema); err != nil {
				return wrapReply{}, fmt.Errorf("the reply's schema is not a JSON Schema: %w", err)
			}
		}
		return r, nil
	}
	r := wrapReply{Data: obj}
	for _, k := range []string{"text", "reply", "output", "answer"} {
		if s, ok := obj[k].(string); ok && s != "" {
			r.Text = s
			break
		}
	}
	return r, nil
}

// wrapHandler serves fn as an A2A agent with card, at /rpc.
func wrapHandler(card *a2a.AgentCard, fn wrapFunc) http.Handler {
	run := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if ec.StoredTask == nil && !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			if !yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), nil) {
				return
			}
			r, err := fn(ctx, inputOf(ec))
			if err != nil {
				msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(err.Error()))
				yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, msg), nil)
				return
			}
			if r.Ask != "" {
				if r.Schema != nil {
					ev, err := structuredinput.NewRequestEvent(ec, &structuredinput.Request{Schema: r.Schema}, a2a.NewTextPart(r.Ask))
					if err != nil {
						msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(err.Error()))
						yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, msg), nil)
						return
					}
					yield(ev, nil)
					return
				}
				msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(r.Ask))
				yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateInputRequired, msg), nil)
				return
			}
			var parts []*a2a.Part
			if r.Text != "" {
				parts = append(parts, a2a.NewTextPart(r.Text))
			}
			if r.Data != nil {
				parts = append(parts, a2a.NewDataPart(r.Data))
			}
			if len(parts) == 0 {
				parts = append(parts, a2a.NewTextPart("(no output)"))
			}
			art := a2a.NewArtifactEvent(ec, parts...)
			art.Artifact.Name = "answer"
			if !yield(art, nil) {
				return
			}
			var reply *a2a.Message
			if r.Text != "" {
				reply = a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(r.Text))
			}
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, reply), nil)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(run)))
	return mux
}

// wrapCard is the card of a wrapped agent.
func wrapCard(name, description, skill string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:                name,
		Description:         description,
		Version:             "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities: a2a.AgentCapabilities{
			Streaming:  true,
			Extensions: []a2a.AgentExtension{structuredinput.NewExtension("")},
		},
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"text/plain", "application/json"},
		Skills: []a2a.AgentSkill{{
			ID: "run", Name: firstOf(skill, name), Description: description, Tags: []string{"wrapped"},
		}},
	}
}

// execFunc runs command with sh -c (cmd /C on Windows) for each message: the text on
// stdin, the rest in LAGRANGE_TASK_ID, LAGRANGE_CONTEXT_ID, LAGRANGE_DATA (JSON) and
// LAGRANGE_ANSWER (1 when the message answers the command's question). A non-zero exit
// fails the task with the end of stderr.
func execFunc(command string, timeout time.Duration) wrapFunc {
	return func(ctx context.Context, in wrapInput) (wrapReply, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", command)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", command)
		}
		data := ""
		if in.Data != nil {
			raw, _ := json.Marshal(in.Data)
			data = string(raw)
		}
		answer := ""
		if in.Answer {
			answer = "1"
		}
		cmd.Env = append(os.Environ(),
			"LAGRANGE_TASK_ID="+in.TaskID, "LAGRANGE_CONTEXT_ID="+in.ContextID,
			"LAGRANGE_DATA="+data, "LAGRANGE_ANSWER="+answer)
		cmd.Stdin = strings.NewReader(in.Text)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &limitedBuffer{max: 4096, buf: &stderr}
		if err := cmd.Run(); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return wrapReply{}, fmt.Errorf("the command took longer than %s", timeout)
			}
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return wrapReply{}, fmt.Errorf("the command failed (%v): %s", err, msg)
			}
			return wrapReply{}, fmt.Errorf("the command failed: %v", err)
		}
		return parseReply(stdout.Bytes())
	}
}

// limitedBuffer keeps the last max bytes written to it.
type limitedBuffer struct {
	max int
	buf *bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.buf.Write(p)
	if over := b.buf.Len() - b.max; over > 0 {
		b.buf.Next(over)
	}
	return len(p), nil
}

// httpFunc posts each message to target as JSON (wrapInput) and reads the response as
// a reply. A non-2xx response fails the task with its status and the start of its body.
func httpFunc(target string, header http.Header, timeout time.Duration) wrapFunc {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, in wrapInput) (wrapReply, error) {
		body, _ := json.Marshal(in)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return wrapReply{}, err
		}
		if header != nil {
			req.Header = header.Clone()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/plain")
		resp, err := client.Do(req)
		if err != nil {
			return wrapReply{}, fmt.Errorf("calling the workflow: %v", err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return wrapReply{}, fmt.Errorf("reading the workflow's reply: %v", err)
		}
		if resp.StatusCode/100 != 2 {
			snippet := strings.TrimSpace(string(out))
			if len(snippet) > 300 {
				snippet = snippet[:300] + "…"
			}
			return wrapReply{}, fmt.Errorf("the workflow answered %s: %s", resp.Status, snippet)
		}
		return parseReply(out)
	}
}

// headerFlag collects repeated -header "Name: value" flags.
type headerFlag http.Header

func (h headerFlag) String() string { return "" }

func (h headerFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("want \"Name: value\", got %q", v)
	}
	http.Header(h).Add(strings.TrimSpace(name), strings.TrimSpace(value))
	return nil
}
