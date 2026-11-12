package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

type pipeRWC struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeRWC) Close() error                { p.w.Close(); return p.r.Close() }

// fakeAgent is an ACP agent over pipes that answers each prompt with a reply chunk,
// a cost report (as claude-agent-acp sends it), and a response with usage when
// usage is set.
func fakeAgent(t *testing.T, usage map[string]any) a2aacp.Agent {
	return a2aacp.AgentFunc(func(context.Context, string) (io.ReadWriteCloser, error) {
		clientR, agentW := io.Pipe()
		agentR, clientW := io.Pipe()
		go func() {
			enc := json.NewEncoder(agentW)
			sc := bufio.NewScanner(agentR)
			sc.Buffer(nil, 1<<20)
			for sc.Scan() {
				var m struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						SessionID string `json:"sessionId"`
					} `json:"params"`
				}
				if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
					t.Errorf("agent got %q: %v", sc.Bytes(), err)
					return
				}
				if m.ID == nil {
					continue
				}
				var result any = map[string]any{}
				switch m.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
				case "session/new":
					result = map[string]any{"sessionId": "s1"}
				case "session/prompt":
					update := func(u map[string]any) {
						_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": m.Params.SessionID, "update": u}})
					}
					update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "done"}})
					update(map[string]any{"sessionUpdate": "usage_update", "used": 30000, "size": 200000, "cost": map[string]any{"amount": 0.05, "currency": "USD"}})
					res := map[string]any{"stopReason": "end_turn"}
					if usage != nil {
						res["usage"] = usage
					}
					result = res
				}
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			}
		}()
		return pipeRWC{r: clientR, w: clientW}, nil
	})
}

// usages runs one task on agent and returns the usage reports it yielded.
func usages(t *testing.T, agent a2aacp.Agent) []Usage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exec, err := a2aacp.NewExecutor(ctx, agent, &a2aacp.Config{Converter: Converter(nil, Options{})})
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Close()
	ec := &a2asrv.ExecutorContext{TaskID: a2a.NewTaskID(), ContextID: a2a.NewContextID(), Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))}
	var out []Usage
	ended := false
	for ev, err := range exec.Execute(ctx, ec) {
		if err != nil {
			t.Fatal(err)
		}
		if ended {
			t.Fatalf("an event after the task ended: %#v", ev)
		}
		if su, ok := ev.(*a2a.TaskStatusUpdateEvent); ok {
			if su.Status.State.Terminal() {
				ended = true
			}
			if su.Status.Message != nil {
				if u, ok := ParseUsage(Find(su.Status.Message.Parts, UsageKey)); ok {
					out = append(out, u)
				}
			}
		}
	}
	if !ended {
		t.Fatal("the task didn't end")
	}
	return out
}

func TestReportTokens(t *testing.T) {
	usage := map[string]any{"inputTokens": 12, "outputTokens": 340, "cachedReadTokens": 28000, "cachedWriteTokens": 1500, "totalTokens": 29852}
	got := usages(t, ReportTokens(fakeAgent(t, usage)))
	if len(got) != 2 {
		t.Fatalf("usage reports = %+v, want the cost report, then the tokens", got)
	}
	if got[0].Cost == nil || got[0].Tokens != nil {
		t.Fatalf("first report = %+v", got[0])
	}
	want := Tokens{Input: 12, Output: 340, CacheRead: 28000, CacheWrite: 1500}
	if got[1].Tokens == nil || *got[1].Tokens != want || got[1].Cost != nil {
		t.Fatalf("tokens report = %+v, want %+v", got[1], want)
	}
}

func TestReportTokensWithoutUsage(t *testing.T) {
	got := usages(t, ReportTokens(fakeAgent(t, nil)))
	if len(got) != 1 || got[0].Tokens != nil {
		t.Fatalf("usage reports = %+v, want only the cost report", got)
	}
}

func TestTokenTapSplitWrites(t *testing.T) {
	agentR, clientW := io.Pipe()
	clientR, agentW := io.Pipe()
	tap := &tokenTap{rwc: pipeRWC{r: clientR, w: clientW}, r: bufio.NewReader(clientR), prompts: map[string]string{}}
	go func() { _, _ = io.Copy(io.Discard, agentR) }()
	req := `{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"s9","prompt":[]}}` + "\n"
	for _, part := range []string{req[:20], req[20:50], req[50:]} {
		if _, err := tap.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		_, _ = io.WriteString(agentW, `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn","usage":{"outputTokens":5}}}`+"\n")
		agentW.Close()
	}()
	b, err := io.ReadAll(iotest(tap))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"sessionId":"s9"`) || !strings.Contains(lines[0], `"output":5`) || !strings.Contains(lines[1], `"id":7`) {
		t.Fatalf("read %q", lines)
	}
	if len(tap.prompts) != 0 {
		t.Fatalf("prompts left: %v", tap.prompts)
	}
}

// iotest reads r a few bytes at a time, as a decoder with a small buffer would.
func iotest(r io.Reader) io.Reader { return smallReader{r} }

type smallReader struct{ r io.Reader }

func (s smallReader) Read(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return s.r.Read(p)
}
