package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

// TokensMeta is the _meta key of the usage_update that [ReportTokens] adds after
// each turn: {"input", "output", "cacheRead", "cacheWrite"} (see [Tokens]).
const TokensMeta = "https://threeagents.dev/ext/tokens"

// ReportTokens wraps an ACP agent so that each turn's tokens reach the converter
// and from there the activity log. An ACP agent returns them in its session/prompt
// response (usage), which a2aacp doesn't pass on, so the wrapper sends a
// usage_update carrying them in _meta[TokensMeta] just before that response. The
// executor handles the update before the response ends the turn. An agent that
// returns no usage is unchanged.
func ReportTokens(a a2aacp.Agent) a2aacp.Agent {
	return a2aacp.AgentFunc(func(ctx context.Context, key string) (io.ReadWriteCloser, error) {
		rwc, err := a.Start(ctx, key)
		if err != nil {
			return nil, err
		}
		return &tokenTap{rwc: rwc, r: bufio.NewReader(rwc), prompts: map[string]string{}}, nil
	})
}

// tokenTap sits on an agent's stdio, which carries newline-delimited JSON-RPC.
// Writes are the client's requests; reads are the agent's messages.
type tokenTap struct {
	rwc io.ReadWriteCloser

	mu      sync.Mutex
	prompts map[string]string // the JSON-RPC ID of a session/prompt request in flight → its session
	wbuf    []byte            // the start of a request not yet written in full

	r   *bufio.Reader
	out []byte // what Read returns next
}

var (
	promptMethod = []byte(`"session/prompt"`)
	methodKey    = []byte(`"method"`)
)

// Write notes session/prompt requests, then writes p unchanged.
func (t *tokenTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.wbuf = append(t.wbuf, p...)
	for {
		i := bytes.IndexByte(t.wbuf, '\n')
		if i < 0 {
			break
		}
		t.notePrompt(t.wbuf[:i])
		t.wbuf = t.wbuf[i+1:]
	}
	if len(t.wbuf) == 0 {
		t.wbuf = nil // don't keep a large prompt's buffer
	}
	t.mu.Unlock()
	return t.rwc.Write(p)
}

func (t *tokenTap) notePrompt(line []byte) {
	if !bytes.Contains(line, promptMethod) {
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &req) == nil && req.Method == "session/prompt" && len(req.ID) > 0 {
		t.prompts[string(bytes.TrimSpace(req.ID))] = req.Params.SessionID
	}
}

// Read returns the agent's messages, with a usage_update before each
// session/prompt response that reports usage.
func (t *tokenTap) Read(p []byte) (int, error) {
	for len(t.out) == 0 {
		line, err := t.r.ReadBytes('\n')
		if len(line) > 0 {
			t.out = t.inspect(line)
			break
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(p, t.out)
	t.out = t.out[n:]
	return n, nil
}

// inspect returns line, preceded by a usage_update if it answers a session/prompt
// request with usage.
func (t *tokenTap) inspect(line []byte) []byte {
	t.mu.Lock()
	pending := len(t.prompts) > 0
	t.mu.Unlock()
	// Requests and notifications have a method; only responses can end a prompt.
	if !pending || bytes.Contains(line, methodKey) {
		return line
	}
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Result *struct {
			Usage *struct {
				Input      int64 `json:"inputTokens"`
				Output     int64 `json:"outputTokens"`
				CacheRead  int64 `json:"cachedReadTokens"`
				CacheWrite int64 `json:"cachedWriteTokens"`
			} `json:"usage"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &resp) != nil || len(resp.ID) == 0 {
		return line
	}
	id := string(bytes.TrimSpace(resp.ID))
	t.mu.Lock()
	session, ok := t.prompts[id]
	delete(t.prompts, id)
	t.mu.Unlock()
	if !ok || resp.Result == nil || resp.Result.Usage == nil {
		return line
	}
	u := resp.Result.Usage
	tk := Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
	if tk.IsZero() {
		return line
	}
	note, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": session,
			"update":    map[string]any{"sessionUpdate": "usage_update", "_meta": map[string]any{TokensMeta: tk.Map()}},
		},
	})
	if err != nil {
		return line
	}
	return append(append(note, '\n'), line...)
}

// Close stops the agent.
func (t *tokenTap) Close() error { return t.rwc.Close() }
