package nango

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/three-agent-problem/lagrange/credentials"
)

// SignatureHeader carries the HMAC-SHA256 of a webhook body, keyed with the
// environment's webhook signing key (which is not the API secret key).
const SignatureHeader = "X-Nango-Hmac-Sha256"

// BrokerConfig configures a Broker.
type BrokerConfig struct {
	// PollInterval is how often Await checks for the new connection. Default 2s.
	// Polling is what makes the flow work locally, where Nango can't reach a webhook.
	PollInterval time.Duration
	// WebhookSigningKey verifies webhooks (Nango environment settings, not the API key).
	// Without it WebhookHandler refuses every webhook and Await relies on polling.
	WebhookSigningKey string
	Logger            *slog.Logger
}

// Broker implements credentials.Broker with Nango Connect and the Nango proxy.
// Connections are tagged with EndUserTag = the agent's user.
type Broker struct {
	client     *Client
	poll       time.Duration
	signingKey string
	logger     *slog.Logger

	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

var _ credentials.Broker = (*Broker)(nil)

// NewBroker creates a broker on top of client.
func NewBroker(client *Client, cfg BrokerConfig) *Broker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Broker{
		client:     client,
		poll:       cfg.PollInterval,
		signingKey: cfg.WebhookSigningKey,
		logger:     cfg.Logger,
		waiters:    make(map[string][]chan struct{}),
	}
}

// Lookup implements credentials.Broker. It returns the newest healthy connection the
// user has for the integration.
func (b *Broker) Lookup(ctx context.Context, user, integration string) (credentials.Grant, error) {
	conns, err := b.client.ListConnections(ctx, map[string]string{EndUserTag: user})
	if err != nil {
		return nil, err
	}
	// Filtering by integration happens here rather than in the query, because the
	// documented HTTP filter is tags, while integration filtering is an SDK option.
	conns = slices.DeleteFunc(conns, func(c Connection) bool {
		return c.ProviderConfigKey != integration || len(c.Errors) > 0
	})
	if len(conns) == 0 {
		return nil, credentials.ErrNoGrant
	}
	newest := slices.MaxFunc(conns, func(a, b Connection) int { return a.Created.Compare(b.Created) })
	return &grant{client: b.client, integration: integration, connectionID: newest.ConnectionID}, nil
}

// Begin implements credentials.Broker by creating a Nango connect session.
func (b *Broker) Begin(ctx context.Context, user, integration string) (*credentials.Challenge, error) {
	session, err := b.client.CreateConnectSession(ctx, map[string]string{EndUserTag: user}, []string{integration})
	if err != nil {
		return nil, err
	}
	return &credentials.Challenge{
		ID:          session.Token,
		User:        user,
		Integration: integration,
		URL:         session.ConnectLink,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

// Await implements credentials.Broker. It polls Nango for the user's connection, and
// checks at once when WebhookHandler reports one. Either way the grant comes from
// Lookup, never from the webhook body: a webhook only says when to look.
func (b *Broker) Await(ctx context.Context, c *credentials.Challenge) (credentials.Grant, error) {
	k := waiterKey(c.User, c.Integration)
	notified := make(chan struct{}, 1)
	b.mu.Lock()
	b.waiters[k] = append(b.waiters[k], notified)
	b.mu.Unlock()
	defer b.removeWaiter(k, notified)

	ticker := time.NewTicker(b.poll)
	defer ticker.Stop()
	var expired <-chan time.Time
	if !c.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(c.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-expired:
			return nil, credentials.ErrExpired
		case <-notified:
		case <-ticker.C:
		}
		g, err := b.Lookup(ctx, c.User, c.Integration)
		if err == nil {
			return g, nil
		}
		if !errors.Is(err, credentials.ErrNoGrant) {
			b.logger.Warn("nango connection check failed", "error", err)
		}
	}
}

func (b *Broker) removeWaiter(k string, ch chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.waiters[k] = slices.DeleteFunc(b.waiters[k], func(c chan struct{}) bool { return c == ch })
	if len(b.waiters[k]) == 0 {
		delete(b.waiters, k)
	}
}

// webhook is the part of Nango's auth webhook the broker needs.
type webhook struct {
	Type              string            `json:"type"`
	Operation         string            `json:"operation"`
	Success           bool              `json:"success"`
	ConnectionID      string            `json:"connectionId"`
	ProviderConfigKey string            `json:"providerConfigKey"`
	Tags              map[string]string `json:"tags"`
}

// WebhookHandler receives Nango's webhooks. A signed auth/creation webhook makes the
// matching Await check for the connection at once instead of at the next poll. Point
// the environment's webhook URL at it when Nango can reach this server. Without a
// signing key it answers 403 to everything.
func (b *Broker) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if b.signingKey == "" {
			http.Error(w, "webhooks are disabled: no signing key is configured", http.StatusForbidden)
			return
		}
		if !validSignature(b.signingKey, body, r.Header.Get(SignatureHeader)) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		var hook webhook
		if err := json.Unmarshal(body, &hook); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if hook.Type == "auth" && hook.Operation == "creation" && hook.Success {
			b.notify(waiterKey(hook.Tags[EndUserTag], hook.ProviderConfigKey))
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (b *Broker) notify(k string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.waiters[k] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func validSignature(key string, body []byte, got string) bool {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(got))
}

func waiterKey(user, integration string) string { return user + "\x00" + integration }

type grant struct {
	client       *Client
	integration  string
	connectionID string
}

// ConnectionID implements credentials.Grant.
func (g *grant) ConnectionID() string { return g.connectionID }

// Call implements credentials.Grant through the Nango proxy.
func (g *grant) Call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	resp, err := g.client.Proxy(ctx, method, g.integration, g.connectionID, path, reader)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return decodeResponse(resp, out)
}
