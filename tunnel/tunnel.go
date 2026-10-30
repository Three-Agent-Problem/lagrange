// Package tunnel connects an agent that runs anywhere (a laptop, a home server, a GPU
// box) to a Lagrange server, like ngrok but for agents: the agent dials out, so it
// needs no public address, open port or deploy.
//
// The agent side (Run) opens a WebSocket to the server's /tunnel endpoint with an
// address token and serves HTTP on a yamux session over it. The server side (Server)
// turns each connection into a Session whose Transport sends HTTP requests to the
// agent through the session. One connection carries any number of concurrent
// requests, streaming (SSE) responses included. Several connections for the same
// address are several workers of one agent.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Path is where the server accepts tunnels.
const Path = "/tunnel"

// DefaultCapacity is how many tasks a session runs at once unless it says otherwise.
const DefaultCapacity = 4

// MaxCapacity caps what a session may ask for.
const MaxCapacity = 64

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.StreamOpenTimeout = 15 * time.Second
	return cfg
}

// Session is a connected agent on the server side.
type Session struct {
	id        string
	handle    string
	capacity  int
	agent     string
	expose    bool
	ys        *yamux.Session
	transport *http.Transport
	connected time.Time
}

// ID implements mailbox.Session.
func (s *Session) ID() string { return s.id }

// Handle is the address the session serves.
func (s *Session) Handle() string { return s.handle }

// Capacity implements mailbox.Session.
func (s *Session) Capacity() int { return s.capacity }

// Agent is the user agent string the client sent (its name and version).
func (s *Session) Agent() string { return s.agent }

// Expose reports whether the agent lets the server forward raw HTTP (webhooks) to it.
func (s *Session) Expose() bool { return s.expose }

// Connected is when the session started.
func (s *Session) Connected() time.Time { return s.connected }

// Transport implements mailbox.Session. Requests go to the agent whatever their host
// and scheme: the tunnel is the secure channel, so https URLs from the agent's card are
// sent as plain HTTP through it.
func (s *Session) Transport() http.RoundTripper { return plainHTTP{s.transport} }

type plainHTTP struct{ rt http.RoundTripper }

func (p plainHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "http" {
		r = r.Clone(r.Context())
		r.URL.Scheme = "http"
	}
	return p.rt.RoundTrip(r)
}

// Done implements mailbox.Session.
func (s *Session) Done() <-chan struct{} { return s.ys.CloseChan() }

// Close ends the session.
func (s *Session) Close() error { return s.ys.Close() }

// Server accepts tunnels.
type Server struct {
	// Authenticate maps a bearer token to the address it may serve.
	Authenticate func(ctx context.Context, token string) (handle string, err error)
	// OnSession is called for each new session, in its own goroutine, and should
	// register it (with a mailbox.Mailbox). The session is closed when the
	// connection drops.
	OnSession func(s *Session)
	// Logger defaults to slog.Default().
	Logger *slog.Logger

	seq atomic.Int64
}

// ServeHTTP implements http.Handler for Path.
func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger
	if log == nil {
		log = slog.Default()
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "a bearer token for the address is required", http.StatusUnauthorized)
		return
	}
	handle, err := srv.Authenticate(r.Context(), token)
	if err != nil {
		http.Error(w, "unknown token", http.StatusUnauthorized)
		return
	}
	capacity := DefaultCapacity
	if v := r.URL.Query().Get("capacity"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "capacity must be a positive number", http.StatusBadRequest)
			return
		}
		capacity = min(n, MaxCapacity)
	}

	// Tunnels authenticate with a bearer token, never cookies, so there is no
	// cross-site risk in accepting any origin.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(-1)
	ctx := context.WithoutCancel(r.Context())
	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	ys, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		conn.Close()
		return
	}
	s := &Session{
		id:        fmt.Sprintf("%s-%d", handle, srv.seq.Add(1)),
		handle:    handle,
		capacity:  capacity,
		agent:     r.Header.Get("User-Agent"),
		expose:    r.URL.Query().Get("expose") == "1",
		ys:        ys,
		connected: time.Now(),
	}
	s.transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return ys.Open()
		},
		MaxIdleConnsPerHost:   capacity * 2,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0, // agents may take long; ctx bounds each call
	}
	log.Info("tunnel connected", "address", handle, "session", s.id, "capacity", capacity, "agent", s.agent)
	if srv.OnSession != nil {
		go srv.OnSession(s)
	}
	<-ys.CloseChan()
	s.transport.CloseIdleConnections()
	conn.Close()
	log.Info("tunnel closed", "address", handle, "session", s.id)
}

// ErrUnauthorized means the server rejected the token; retrying won't help.
var ErrUnauthorized = errors.New("tunnel: the server rejected the token")

// ClientConfig configures Run.
type ClientConfig struct {
	// Server is the Lagrange server's base URL, for example https://lagrange.example.com.
	Server string
	// Token is the address token.
	Token string
	// Capacity is how many tasks this copy of the agent runs at once.
	Capacity int
	// Handler serves the requests that come through the tunnel: the agent itself, or a
	// reverse proxy to it (see Proxy).
	Handler http.Handler
	// Expose lets the server forward raw HTTP requests for /t/<handle>/... to Handler,
	// for example push notifications the agent subscribed to.
	Expose bool
	// UserAgent describes the client, for the server's logs and UI.
	UserAgent string
	// OnConnect is called after each successful connection.
	OnConnect func()
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Run keeps a tunnel open until ctx ends, reconnecting with backoff when it drops. It
// returns ErrUnauthorized if the server rejects the token.
func Run(ctx context.Context, cfg ClientConfig) error {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	wsURL, err := tunnelURL(cfg.Server, cfg.Capacity, cfg.Expose)
	if err != nil {
		return err
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := serveOnce(ctx, wsURL, cfg)
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		log.Warn("tunnel dropped, reconnecting", "error", err, "in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return ctx.Err()
}

func tunnelURL(server string, capacity int, expose bool) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("tunnel: invalid server URL %q", server)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("tunnel: unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + Path
	q := url.Values{}
	if capacity > 0 {
		q.Set("capacity", strconv.Itoa(capacity))
	}
	if expose {
		q.Set("expose", "1")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func serveOnce(ctx context.Context, wsURL string, cfg ClientConfig) error {
	header := http.Header{"Authorization": {"Bearer " + cfg.Token}}
	if cfg.UserAgent != "" {
		header.Set("User-Agent", cfg.UserAgent)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		return err
	}
	c.SetReadLimit(-1)
	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	ys, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		conn.Close()
		return err
	}
	if cfg.OnConnect != nil {
		cfg.OnConnect()
	}
	srv := &http.Server{Handler: cfg.Handler, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		select {
		case <-ctx.Done():
		case <-ys.CloseChan():
		}
		srv.Close()
		ys.Close()
	}()
	err = srv.Serve(ys)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Proxy returns a handler that forwards every request to target (for example
// http://localhost:9001), streaming responses as they come.
func Proxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		FlushInterval: -1,
	}
}
