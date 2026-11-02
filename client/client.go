// Package client calls a Lagrange server's HTTP API with a personal API key (lgk_…).
// The CLI and the MCP server use it; the API itself is described at /api/openapi.yaml.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Client calls one server as one key.
type Client struct {
	// Server is the base URL, e.g. https://lagrange.example.com.
	Server string
	// Key is a personal API key.
	Key string
	// HTTP is the client to use; nil means one with a 30-second timeout.
	HTTP *http.Client
}

// New returns a client for server with key.
func New(server, key string) *Client {
	return &Client{Server: strings.TrimSuffix(server, "/"), Key: key}
}

// Error is an error answer from the server.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the server answered %d", e.Status)
	}
	return e.Message
}

// StatusOf returns the HTTP status of an API error, or 0.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

// Do calls method path with body as JSON and decodes the answer into out (if not nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	} else if method != http.MethodGet {
		r = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, r)
	if err != nil {
		return err
	}
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	h := c.HTTP
	if h == nil {
		h = defaultHTTP
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Me is who a key acts as.
type Me struct {
	User struct {
		ID     int64  `json:"id"`
		Email  string `json:"email"`
		Name   string `json:"name"`
		Handle string `json:"handle"`
	} `json:"user"`
	Key struct {
		Workspace string `json:"workspace"`
		Role      string `json:"role"`
	} `json:"key"`
}

// Me returns who the key acts as.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	return &m, c.Do(ctx, http.MethodGet, "/api/me", nil, &m)
}

// CreateAgent creates an agent address and returns its first tunnel token.
func (c *Client) CreateAgent(ctx context.Context, handle string) (string, error) {
	var out struct{ Token string }
	err := c.Do(ctx, http.MethodPost, "/api/addresses", map[string]string{"handle": handle}, &out)
	return out.Token, err
}

// MintToken mints a tunnel token for an agent address.
func (c *Client) MintToken(ctx context.Context, handle, label string) (string, error) {
	var out struct{ Token string }
	err := c.Do(ctx, http.MethodPost, "/api/addresses/"+url.PathEscape(handle)+"/tokens", map[string]string{"label": label}, &out)
	return out.Token, err
}

// ClaimLogin trades a `lagrange login` code and its PKCE verifier for a key. It needs
// no key.
func ClaimLogin(ctx context.Context, server, code, verifier string) (*Login, error) {
	var l Login
	err := New(server, "").Do(ctx, http.MethodPost, "/api/cli/token", map[string]string{"code": code, "verifier": verifier}, &l)
	return &l, err
}

// Login is a key from `lagrange login`.
type Login struct {
	Key       string `json:"key"`
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	Server    string `json:"server"`
}

// Address is an agent or a person.
type Address struct {
	Handle      string          `json:"handle"`
	Kind        string          `json:"kind"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	Online      bool            `json:"online"`
	Card        json.RawMessage `json:"card,omitempty"`
}

// Addresses lists the addresses the workspace can reach.
func (c *Client) Addresses(ctx context.Context) ([]*Address, error) {
	var out struct{ Addresses []*Address }
	err := c.Do(ctx, http.MethodGet, "/api/addresses", nil, &out)
	return out.Addresses, err
}

// Task is a task as the API shows it, with the full A2A task.
type Task struct {
	ID           string    `json:"id"`
	ContextID    string    `json:"contextId"`
	State        string    `json:"state"`
	Sender       string    `json:"sender"`
	Recipient    string    `json:"recipient"`
	ParentTaskID string    `json:"parentTaskId,omitempty"`
	Summary      string    `json:"summary"`
	Status       string    `json:"status,omitempty"`
	Awaiting     string    `json:"awaiting,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Task         *a2a.Task `json:"task,omitempty"`
}

// Task returns one task.
func (c *Client) Task(ctx context.Context, id string) (*Task, error) {
	var out struct{ Task *Task }
	if err := c.Do(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	if out.Task == nil {
		return nil, &Error{Status: http.StatusNotFound, Message: "no such task"}
	}
	return out.Task, nil
}

// Send is a message to an address.
type Send struct {
	To        string         `json:"to"`
	Text      string         `json:"text,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
}

// SendResult is what an address answered: a task to follow, or a plain message.
type SendResult struct {
	Task    *a2a.Task
	Message *a2a.Message
}

// Send sends a message and returns the task it started (or continued), or the
// address's direct answer.
func (c *Client) Send(ctx context.Context, m *Send) (*SendResult, error) {
	var out struct{ Result json.RawMessage }
	if err := c.Do(ctx, http.MethodPost, "/api/send", m, &out); err != nil {
		return nil, err
	}
	var probe struct {
		Status    *json.RawMessage `json:"status"`
		MessageID string           `json:"messageId"`
	}
	_ = json.Unmarshal(out.Result, &probe)
	if probe.Status != nil {
		var t a2a.Task
		if err := json.Unmarshal(out.Result, &t); err != nil {
			return nil, err
		}
		return &SendResult{Task: &t}, nil
	}
	var msg a2a.Message
	if err := json.Unmarshal(out.Result, &msg); err != nil {
		return nil, err
	}
	return &SendResult{Message: &msg}, nil
}

// Answer is the answer to a task waiting for input.
type Answer struct {
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Decision string         `json:"decision,omitempty"`
}

// Answer answers a task waiting for input.
func (c *Client) Answer(ctx context.Context, id string, a *Answer) error {
	return c.Do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/answer", a, nil)
}

// Wait is a task waiting for an answer.
type Wait struct {
	Task        *Task     `json:"task"`
	RunID       string    `json:"runId"`
	Who         string    `json:"who"`
	Asker       string    `json:"asker"`
	Person      bool      `json:"person"`
	Since       time.Time `json:"since"`
	Reminders   int       `json:"reminders"`
	EscalatedTo string    `json:"escalatedTo,omitempty"`
}

// Waiting lists the workspace's tasks waiting for an answer, longest wait first.
func (c *Client) Waiting(ctx context.Context) ([]*Wait, error) {
	var out struct{ Waiting []*Wait }
	err := c.Do(ctx, http.MethodGet, "/api/waiting", nil, &out)
	return out.Waiting, err
}

// Run is a root task and everything it caused.
type Run struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Outcome      string    `json:"outcome,omitempty"`
	Starter      string    `json:"starter"`
	Recipient    string    `json:"recipient"`
	State        string    `json:"state"`
	Waiting      []string  `json:"waiting"`
	Participants []string  `json:"participants"`
	Tasks        int       `json:"tasks"`
	StartedAt    time.Time `json:"startedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// RunFilter narrows Runs. Empty fields don't filter.
type RunFilter struct {
	Q, Status, Participant, WaitingOn string
	Limit                             int
}

// Runs lists runs, newest first.
func (c *Client) Runs(ctx context.Context, f RunFilter) ([]*Run, error) {
	q := url.Values{}
	for k, v := range map[string]string{"q": f.Q, "status": f.Status, "participant": f.Participant, "waitingOn": f.WaitingOn} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprint(f.Limit))
	}
	path := "/api/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct{ Runs []*Run }
	err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return out.Runs, err
}

// RunDetail is a run with its tasks.
type RunDetail struct {
	Run   *Run    `json:"run"`
	Tasks []*Task `json:"tasks"`
}

// Run returns one run with its tasks.
func (c *Client) Run(ctx context.Context, id string) (*RunDetail, error) {
	var out RunDetail
	err := c.Do(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(id), nil, &out)
	return &out, err
}
