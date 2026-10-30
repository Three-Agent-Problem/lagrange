// Package nango is a credentials.Broker backed by Nango (https://nango.dev).
//
// Nango runs the OAuth flow (Connect UI), stores and refreshes the tokens, and proxies
// API calls, so the agent only ever handles a connection ID. Only Nango's HTTP API is
// used, which works both against Nango Cloud and a self-hosted Nango (whose free tier
// includes Auth, Proxy and Connect UI). Docs:
//
//   - connect sessions: https://nango.dev/docs/guides/auth/share-connect-link
//   - list connections: https://nango.dev/docs/reference/backend/http-api/connections/list
//   - proxy:            https://nango.dev/docs/reference/backend/http-api/proxy/post
//   - auth webhooks:    https://nango.dev/docs/guides/platform/webhooks-from-nango
package nango

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

// DefaultBaseURL is Nango Cloud's API.
const DefaultBaseURL = "https://api.nango.dev"

// EndUserTag is the connection tag that ties a Nango connection to the agent's user.
const EndUserTag = "end_user_id"

// Client calls Nango's HTTP API with a secret key.
type Client struct {
	baseURL   string
	secretKey string
	http      *http.Client
}

// NewClient creates a client. baseURL defaults to DefaultBaseURL and httpClient to a
// client with a 30 second timeout.
func NewClient(secretKey, baseURL string, httpClient *http.Client) (*Client, error) {
	if secretKey == "" {
		return nil, errors.New("nango: a secret key is required (Environment Settings > API keys)")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), secretKey: secretKey, http: httpClient}, nil
}

// ConnectSession is a short-lived session for Nango Connect (30 minutes).
type ConnectSession struct {
	Token       string    `json:"token"`
	ConnectLink string    `json:"connect_link"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// CreateConnectSession creates a session that lets a user connect one of the allowed
// integrations. The tags are copied onto the connection the user creates.
func (c *Client) CreateConnectSession(ctx context.Context, tags map[string]string, allowedIntegrations []string) (*ConnectSession, error) {
	body := map[string]any{"tags": tags, "allowed_integrations": allowedIntegrations}
	var resp struct {
		Data ConnectSession `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/connect/sessions", nil, body, &resp); err != nil {
		return nil, fmt.Errorf("nango: create connect session: %w", err)
	}
	if resp.Data.ConnectLink == "" {
		return nil, errors.New("nango: create connect session: the response has no connect_link")
	}
	return &resp.Data, nil
}

// Connection is a connection as listed by Nango (without credentials).
type Connection struct {
	ID                int               `json:"id"`
	ConnectionID      string            `json:"connection_id"`
	Provider          string            `json:"provider"`
	ProviderConfigKey string            `json:"provider_config_key"`
	Created           time.Time         `json:"created"`
	Tags              map[string]string `json:"tags"`
	Errors            []struct {
		Type  string `json:"type"`
		LogID string `json:"log_id"`
	} `json:"errors"`
}

// ListConnections lists the connections that carry all the given tags.
func (c *Client) ListConnections(ctx context.Context, tags map[string]string) ([]Connection, error) {
	query := url.Values{}
	for k, v := range tags {
		query.Set("tags["+k+"]", v)
	}
	var resp struct {
		Connections []Connection `json:"connections"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/connections", query, nil, &resp); err != nil {
		return nil, fmt.Errorf("nango: list connections: %w", err)
	}
	return resp.Connections, nil
}

// Proxy sends a request to the provider's API through Nango, which adds the
// connection's credentials. path is the provider API path, for example
// "/calendar/v3/calendars/primary/events".
func (c *Client) Proxy(ctx context.Context, method, integration, connectionID, path string, body io.Reader) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/proxy"+path, body)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	req.Header.Set("Provider-Config-Key", integration)
	req.Header.Set("Connection-Id", connectionID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	c.authorize(req)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse(resp, out)
}

func decodeResponse(resp *http.Response, out any) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
