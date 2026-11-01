// Package platform assembles a Lagrange server: Postgres, the directory, the relay
// executor in a2a-go cluster mode, the tunnel endpoint, the API and the web app, all on
// one HTTP handler.
//
// Routes:
//
//	/a/{handle}/.well-known/agent-card.json  the address's agent card (also at /a/{handle})
//	/a/{handle}/rpc                          A2A JSON-RPC for the address
//	/a/{handle}/...                          A2A HTTP+JSON (REST) for the address
//	/t/{handle}/...                          raw HTTP to an agent whose tunnel exposes it
//	/tunnel                                  agents connect here (WebSocket)
//	/registry/agents                         registry-compatible search of addresses
//	/api/...                                 the web app's API (session cookie)
//	/healthz                                 liveness
//	/                                        the web app
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/mailbox"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/relay"
	"github.com/three-agent-problem/lagrange/tunnel"
)

// Config configures a server.
type Config struct {
	// DatabaseURL is the Postgres connection URL.
	DatabaseURL string
	// PublicURL is how clients reach this server, for example https://lagrange.example.com.
	// Agent cards and tunnel commands use it.
	PublicURL string
	// Admin creates the first user on start, unless a user with this email exists.
	AdminEmail, AdminPassword, AdminName, AdminHandle string
	// WorkerID names this replica in the work queue (default: hostname).
	WorkerID string
	// Web is the built web app (index.html at the root). Nil serves a placeholder page.
	Web fs.FS
	// AllowPrivatePush lets push notifications go to private and loopback addresses.
	// Only for local development and tests.
	AllowPrivatePush bool
	// Dev enables development-only endpoints, such as POST /api/dev/reset.
	Dev bool
	// DevSeed is a command (split on spaces, no shell) that seeds a dev server: it runs
	// from start to Close, and "reset to seed" restarts it with LAGRANGE_SEED=1 after
	// emptying the server. It gets LAGRANGE_SERVER, LAGRANGE_ADMIN_EMAIL and
	// LAGRANGE_ADMIN_PASSWORD. Needs Dev.
	DevSeed string
	// Queue tunes the work queue; zero values are the defaults.
	Queue pg.WorkQueueConfig
	// WaitTick is how often the team's wait policy (reminders, escalations, cancels) is
	// applied (default 30s).
	WaitTick time.Duration
	// EventPoll is how often subscribers poll for task events (default 100ms).
	EventPoll time.Duration
	// Email sends notifications by email (notify.ParseSMTP). Nil: no email; Slack still works.
	// Without email, signups aren't verified and login links and password resets are off.
	Email notify.Email
	// Signup is SignupInvite (default: only invited people join) or SignupOpen.
	Signup string
	// OAuth providers people can log in with (GitHubProvider, GoogleProvider).
	OAuth []*OAuthProvider
	// TrustProxy: the server runs behind a reverse proxy, so the client address is the
	// last X-Forwarded-For entry (for rate limits).
	TrustProxy bool
	// AuthRateLimit is how many login, signup and link requests a client may make per
	// minute (default 30; negative: no limit).
	AuthRateLimit int
	Logger        *slog.Logger
}

// Platform is a running server.
type Platform struct {
	cfg       Config
	log       *slog.Logger
	DB        *pg.DB
	Directory *directory.Directory
	Links     *relay.Links
	Activity  *activity.Log
	Mailbox   *mailbox.Mailbox
	Tasks     *pg.TaskStore
	Relay     *relay.Relay
	Notify    *notify.Notifier
	// A2A is the a2a-go request handler that serves every address.
	A2A     a2asrv.RequestHandler
	Tunnels *tunnel.Server

	sessions *sessionRegistry
	limits   *limits
	mail     sync.WaitGroup
	seed     *seeder
	queue    *pg.WorkQueue
	handler  http.Handler
	stop     context.CancelFunc
	closed   sync.Once
}

// New connects to Postgres, applies migrations and builds the server.
func New(ctx context.Context, cfg Config) (*Platform, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.PublicURL == "" {
		return nil, errors.New("platform: PublicURL is required")
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	for _, o := range cfg.OAuth {
		o.Config.RedirectURL = cfg.PublicURL + "/api/auth/" + o.Name + "/callback"
	}
	if cfg.AuthRateLimit == 0 {
		cfg.AuthRateLimit = 30
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID, _ = os.Hostname()
	}
	db, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	p := &Platform{
		cfg:       cfg,
		log:       log,
		DB:        db,
		Directory: directory.New(db),
		Links:     relay.NewLinks(db),
		Activity:  activity.New(db),
		Mailbox:   mailbox.New(),
		Tasks:     pg.NewTaskStore(db),
		sessions:  newSessionRegistry(),
		limits:    &limits{perMinute: cfg.AuthRateLimit},
	}
	bg, stop := context.WithCancel(context.Background())
	p.stop = stop
	p.Mailbox.OnChange = func(handle string, s mailbox.Stats) {
		_ = p.Activity.Add(bg, &activity.Entry{Kind: activity.KindPresence, Recipient: handle, Data: map[string]any{
			"sessions": s.Sessions, "capacity": s.Capacity, "inFlight": s.InFlight, "waiting": s.Waiting,
		}})
	}
	p.queue = pg.NewWorkQueueBackend(db, cfg.WorkerID, cfg.Queue)
	p.Notify = &notify.Notifier{Directory: p.Directory, PublicURL: cfg.PublicURL, Email: cfg.Email, Logger: log}
	p.Relay = &relay.Relay{Directory: p.Directory, Links: p.Links, Activity: p.Activity, Mailbox: p.Mailbox, OnWaiting: p.onWaiting, Logger: log}
	handler := a2asrv.NewHandler(p.Relay,
		a2asrv.WithClusterMode(a2asrv.ClusterConfig{
			TaskStore:    p.Tasks,
			QueueManager: pg.NewEventQueueManager(db, cfg.EventPoll),
			WorkQueue:    p.queue.Queue(),
		}),
		a2asrv.WithPushNotifications(pg.NewPushStore(db), push.NewHTTPPushSender(&push.HTTPSenderConfig{
			Timeout:              10 * time.Second,
			AllowPrivateNetworks: cfg.AllowPrivatePush,
		})),
		a2asrv.WithCallInterceptors(identityInterceptor{}),
	)
	p.A2A = settlingCancel{handler}
	p.Tunnels = &tunnel.Server{
		Authenticate: func(ctx context.Context, token string) (string, error) {
			handle, err := p.Directory.TokenHandle(ctx, token)
			if err != nil {
				return "", err
			}
			addr, err := p.Directory.Address(ctx, handle)
			if err != nil || addr.Kind != directory.KindAgent {
				return "", errors.New("only agent addresses can open tunnels")
			}
			return handle, nil
		},
		OnSession: func(s *tunnel.Session) { p.onSession(bg, s) },
		Logger:    log,
	}
	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		handle := cfg.AdminHandle
		if handle == "" {
			handle = "admin"
		}
		name := cfg.AdminName
		if name == "" {
			name = handle
		}
		if _, err := p.Directory.EnsureAdmin(ctx, cfg.AdminEmail, name, handle, cfg.AdminPassword); err != nil {
			db.Close()
			return nil, fmt.Errorf("platform: creating the admin user: %w", err)
		}
	}
	p.handler = p.routes()
	if cfg.WaitTick <= 0 {
		cfg.WaitTick = 30 * time.Second
	}
	go p.runWaitTimers(bg, cfg.WaitTick)
	if p.seed = newSeeder(cfg, log); p.seed != nil {
		// A server with no agents yet gets the scripted requests too.
		var empty bool
		_ = db.Pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM addresses WHERE kind = 'agent')`).Scan(&empty)
		if err := p.seed.start(empty); err != nil {
			log.Warn("dev seed command", "err", err)
		}
	}
	return p, nil
}

// Handler is the server's HTTP handler.
func (p *Platform) Handler() http.Handler { return p.handler }

// PublicURL is the server's public base URL.
func (p *Platform) PublicURL() string { return p.cfg.PublicURL }

// Close stops taking work, hands running tasks back to the queue for another replica
// (or the next start), and closes the database.
func (p *Platform) Close() {
	p.closed.Do(func() {
		p.seed.stop()
		p.queue.Close(5 * time.Second)
		p.Notify.Wait()
		p.mail.Wait()
		p.stop()
		p.sessions.closeAll()
		p.DB.Close()
	})
}

func (p *Platform) onSession(ctx context.Context, s *tunnel.Session) {
	p.sessions.add(s)
	go func() {
		<-s.Done()
		p.sessions.remove(s)
	}()
	// Store the card before the session takes work, so the first task doesn't fetch it.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if card, err := relay.FetchCard(cctx, &http.Client{Transport: s.Transport()}, ""); err != nil {
		p.log.Warn("platform: could not fetch the agent card through the tunnel", "address", s.Handle(), "error", err)
	} else if err := p.Directory.SetCard(cctx, s.Handle(), card); err != nil {
		p.log.Warn("platform: storing the agent card failed", "address", s.Handle(), "error", err)
	}
	p.Mailbox.Add(s.Handle(), s)
}

func (p *Platform) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := p.DB.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	rest := a2asrv.NewTenantRESTHandler("/a/{*}", p.A2A)
	rpc := a2asrv.NewJSONRPCHandler(p.A2A)
	mux.HandleFunc("GET /a/{handle}/.well-known/agent-card.json", p.handleCard)
	// Clients given an address URL (https://host/a/ada) fetch it as the card URL.
	mux.HandleFunc("GET /a/{handle}", p.handleCard)
	mux.Handle("POST /a/{handle}/rpc", p.addressOnly(p.identify(p.recordIn(tenantRPC(rpc)))))
	mux.Handle("/a/{handle}/", p.addressOnly(p.identify(p.recordIn(rest))))
	mux.HandleFunc("/t/{handle}/{path...}", p.handlePassthrough)
	mux.Handle(tunnel.Path, p.Tunnels)
	mux.HandleFunc("GET /registry/agents", p.handleRegistrySearch)
	p.api(mux)
	mux.Handle("/", webHandler(p.cfg.Web))
	return mux
}

// addressOnly answers 404 for handles that don't exist.
func (p *Platform) addressOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := p.Directory.Address(r.Context(), r.PathValue("handle")); err != nil {
			http.Error(w, "no such address", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recordIn records every A2A call to an address: who called, the request and the
// response (a whole stream, for streaming calls).
func (p *Platform) recordIn(next http.Handler) http.Handler {
	return p.Activity.Handler(next, func(r *http.Request) activity.Exchange {
		x := activity.Exchange{Direction: activity.DirIn, Recipient: r.PathValue("handle")}
		if id, _ := r.Context().Value(identityKey{}).(*identity); id != nil {
			x.Sender = id.handle
		}
		if parent := r.Header.Get(relay.HeaderTask); parent != "" && p.Links.ValidParent(r.Context(), a2a.TaskID(parent), x.Sender) {
			x.ParentTaskID = parent
		}
		return x
	})
}

// tenantRPC puts the address from the path into JSON-RPC params, so clients that ignore
// the card's tenant field still reach the right address.
func tenantRPC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle := r.PathValue("handle")
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "reading the request failed", http.StatusBadRequest)
			return
		}
		var req map[string]json.RawMessage
		if json.Unmarshal(body, &req) == nil {
			var params map[string]json.RawMessage
			if raw, ok := req["params"]; ok && json.Unmarshal(raw, &params) == nil && params != nil {
				params["tenant"], _ = json.Marshal(handle)
				req["params"], _ = json.Marshal(params)
				body, _ = json.Marshal(req)
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

// Card returns the platform's card for an address: the agent's own card with the
// platform's interfaces, or a card describing a person.
func (p *Platform) Card(addr *directory.Address) *a2a.AgentCard {
	base := p.cfg.PublicURL + "/a/" + addr.Handle
	var card a2a.AgentCard
	switch {
	case addr.Kind == directory.KindHuman:
		card = a2a.AgentCard{
			Name:        addr.Name(),
			Description: firstNonEmpty(addr.Description, addr.Name()+" is a person on Lagrange. Send a question or a request; they answer from their inbox, and the task completes with their answer."),
			Version:     "1.0.0",
			Skills: []a2a.AgentSkill{{
				ID: "ask", Name: "Ask " + addr.Name(),
				Description: "A question, a request for approval, or a task for a person.",
				Tags:        []string{"human", "approval", "question"},
			}},
		}
	case addr.Card != nil:
		card = *addr.Card
		card.Signatures = nil // the platform rewrites the card, so the agent's signatures no longer hold
		if addr.DisplayName != "" {
			card.Name = addr.DisplayName
		}
	default:
		card = a2a.AgentCard{Name: addr.Name(), Description: firstNonEmpty(addr.Description, "An agent on Lagrange."), Version: "0.0.0", Skills: []a2a.AgentSkill{}}
	}
	card.SupportedInterfaces = []*a2a.AgentInterface{
		a2a.NewAgentInterface(base+"/rpc", a2a.TransportProtocolJSONRPC),
		a2a.NewAgentInterface(base, a2a.TransportProtocolHTTPJSON),
	}
	// The platform streams and pushes for every address, whatever the agent supports.
	card.Capabilities.Streaming = true
	card.Capabilities.PushNotifications = true
	if len(card.DefaultInputModes) == 0 {
		card.DefaultInputModes = []string{"text/plain", "application/json"}
	}
	if len(card.DefaultOutputModes) == 0 {
		card.DefaultOutputModes = []string{"text/plain", "application/json"}
	}
	if card.Skills == nil {
		card.Skills = []a2a.AgentSkill{}
	}
	return &card
}

func (p *Platform) handleCard(w http.ResponseWriter, r *http.Request) {
	addr, err := p.Directory.Address(r.Context(), r.PathValue("handle"))
	if err != nil {
		http.Error(w, "no such address", http.StatusNotFound)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, p.Card(addr))
}

// handlePassthrough forwards raw HTTP to an agent's tunnel when the tunnel was opened with
// expose, so agents can receive webhooks (push notifications) without a public address.
func (p *Platform) handlePassthrough(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	s := p.sessions.exposed(handle)
	if s == nil {
		http.Error(w, "this agent is offline or doesn't expose HTTP", http.StatusBadGateway)
		return
	}
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host = "http", "agent"
	out.URL.Path = "/" + r.PathValue("path")
	out.URL.RawPath = ""
	out.RequestURI = ""
	out.Host = "agent"
	// Pushes come from the platform's own push sender, on behalf of the task they report.
	resp, err := p.Activity.Transport(s.Transport(), activity.Exchange{Direction: activity.DirPush, Recipient: handle}).RoundTrip(out)
	if err != nil {
		http.Error(w, "the agent did not answer: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// settlingCancel works around a race in a2a-go's cluster-mode cancel: it subscribes
// to the task and takes the first final event as the outcome, and the snapshot it
// starts with already counts as final when the task waits for input. The cancel work
// item still runs, so wait for it to settle the task.
type settlingCancel struct{ a2asrv.RequestHandler }

func (h settlingCancel) CancelTask(ctx context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	task, err := h.RequestHandler.CancelTask(ctx, req)
	if !errors.Is(err, a2a.ErrTaskNotCancelable) {
		return task, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		got, gerr := h.GetTask(ctx, &a2a.GetTaskRequest{Tenant: req.Tenant, ID: req.ID})
		switch {
		case gerr != nil:
			return nil, err
		case got.Status.State == a2a.TaskStateCanceled:
			return got, nil
		case got.Status.State.Terminal():
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
}
