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
