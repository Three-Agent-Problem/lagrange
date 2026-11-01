package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/directory"
)

// Who may start a task with an address is the address's access: anyone (public), its
// workspace, or its workspace and the workspaces it accepted a connection from. Calls on
// a task that exists are the task owner's, which the task store already enforces, so
// only new tasks are checked.

// accessInterceptor admits or refuses each new task, after identityInterceptor named the
// caller. Calls the platform makes for a person (the web app's send) pass through it too.
type accessInterceptor struct {
	a2asrv.PassthroughCallInterceptor
	p *Platform
}

func (a accessInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, req *a2asrv.Request) (context.Context, any, error) {
	switch callCtx.Method() {
	case "SendMessage", "SendStreamingMessage":
	default:
		return ctx, nil, nil
	}
	send, ok := req.Payload.(*a2a.SendMessageRequest)
	if !ok || send.Message == nil || send.Message.TaskID != "" {
		return ctx, nil, nil
	}
	var caller string
	if callCtx.User != nil && callCtx.User.Authenticated {
		caller = callCtx.User.Name
	}
	return ctx, nil, a.p.admit(ctx, caller, callCtx.Tenant())
}

// admit decides whether caller (an address, or "" for anonymous) may start a task with
// handle.
func (p *Platform) admit(ctx context.Context, caller, handle string) error {
	addr, err := p.Directory.Address(ctx, handle)
	if err != nil || caller == handle {
		return nil // an unknown address fails later with its own error
	}
	if caller == "" {
		if addr.Access == directory.AccessPublic {
			return nil
		}
		return a2a.NewError(a2a.ErrUnauthenticated, fmt.Sprintf(
			"@%s takes tasks only from its workspace and connections: call with your agent's token (Authorization: Bearer lgt_...)", handle))
	}
	reach, err := p.Directory.ReachOf(ctx, caller, handle)
	if err != nil {
		return err
	}
	switch {
	case reach.Blocked:
		return a2a.NewError(a2a.ErrUnauthorized, fmt.Sprintf("@%s doesn't take tasks from @%s", handle, caller))
	case reach.SameWorkspace, addr.Access == directory.AccessPublic:
		return nil
	case addr.Access == directory.AccessConnections && reach.Connection != nil:
		if !p.rates.allow(reach.Connection.ID, reach.Connection.RatePerMin) {
			return a2a.NewError(a2a.ErrUnauthorized, fmt.Sprintf(
				"rate limit: %s may start %d tasks a minute with @%s; try again shortly", reach.Connection.FromName, reach.Connection.RatePerMin, handle))
		}
		return nil
	case addr.Access == directory.AccessConnections:
		return a2a.NewError(a2a.ErrUnauthorized, fmt.Sprintf(
			"@%s takes tasks from its workspace and connections only: ask for access at %s/a/%s", handle, p.cfg.PublicURL, handle))
	}
	return a2a.NewError(a2a.ErrUnauthorized, fmt.Sprintf("@%s takes tasks from its own workspace only", handle))
}

// rateLimiter is a token bucket per connection, refilled continuously. It is per
// replica, so with several replicas a connection gets up to its rate on each.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{buckets: map[int64]*bucket{}} }

func (l *rateLimiter) allow(id int64, perMin int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[id]
	if !ok {
		b = &bucket{tokens: float64(perMin), at: now}
		l.buckets[id] = b
	}
	b.tokens = min(float64(perMin), b.tokens+now.Sub(b.at).Minutes()*float64(perMin))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (p *Platform) accessRoutes(mux *http.ServeMux) {
	mux.Handle("PUT /api/addresses/{handle}/access", p.in(directory.RoleViewer, p.handleSetAccess))
	mux.Handle("GET /api/connections", p.in(directory.RoleViewer, p.handleConnections))
	mux.Handle("POST /api/connections", p.in(directory.RoleMember, p.handleRequestConnection))
	mux.Handle("PUT /api/connections/{id}", p.in(directory.RoleViewer, p.handleDecideConnection))
	mux.Handle("DELETE /api/connections/{id}", p.in(directory.RoleViewer, p.handleEndConnection))
}

// handleSetAccess changes who may start a task with an address: an agent's managers
// decide for it, and people for themselves.
func (p *Platform) handleSetAccess(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body struct{ Access string }
	if err := decode(r, &body); err != nil || !directory.ValidAccess(body.Access) {
		apiError(w, http.StatusBadRequest, "send {access}: public, workspace or connections")
		return
	}
	handle := r.PathValue("handle")
	if handle != me.Handle && p.managedAddress(w, r, me, s) == nil {
		return
	}
	if err := p.Directory.SetAccess(r.Context(), handle, body.Access); err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	p.addressChanged(r.Context(), handle)
	w.WriteHeader(http.StatusNoContent)
}

// decides lists the addresses whose connection requests me decides in the scope: the
// workspace's agents for its admins, and always me.
func (p *Platform) decides(ctx context.Context, me *directory.User, s *scope) ([]string, error) {
	handles := []string{me.Handle}
	if !s.can(directory.RoleAdmin) {
		return handles, nil
	}
	agents, err := p.Directory.AddressesIn(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		handles = append(handles, a.Handle)
	}
	return handles, nil
}

func (p *Platform) handleConnections(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	out, err := p.Directory.ConnectionsFrom(ctx, s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handles, err := p.decides(ctx, me, s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in, err := p.Directory.ConnectionsTo(ctx, handles)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []*directory.Connection{}
	}
	if in == nil {
		in = []*directory.Connection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outgoing": out, "incoming": in})
}

// handleRequestConnection asks, for the workspace, to reach an address of another one.
func (p *Platform) handleRequestConnection(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	var body struct{ To, Note string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {to, note}")
		return
	}
	handle := strings.TrimPrefix(strings.TrimSpace(body.To), "@")
	addr, err := p.Directory.Address(ctx, handle)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such address: @"+handle)
		return
	}
	switch {
	case s.in[handle]:
		apiError(w, http.StatusBadRequest, "@"+handle+" is in "+s.Name+" already")
		return
	case addr.Access == directory.AccessPublic:
		apiError(w, http.StatusBadRequest, "@"+handle+" is public: anyone can call it")
		return
	case addr.Access == directory.AccessWorkspace:
		apiError(w, http.StatusForbidden, "@"+handle+" takes tasks from its own workspace only")
		return
	}
	c, err := p.Directory.RequestConnection(ctx, s.ID, handle, strings.TrimSpace(body.Note), me.ID)
	if errors.Is(err, directory.ErrBlocked) {
		apiError(w, http.StatusForbidden, "@"+handle+" doesn't take requests from "+s.Name)
		return
	}
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	if c.Status == directory.ConnPending {
		p.mailDeciders(ctx, addr, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": c})
}

// connection loads the connection in the path and checks that me decides it (decide)
// or is on either side of it.
func (p *Platform) connection(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope, decide bool) *directory.Connection {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "connections are named by their ID")
		return nil
	}
	c, err := p.Directory.Connection(r.Context(), id)
	if err != nil {
		apiError(w, statusOf(err), "no such connection")
		return nil
	}
	handles, err := p.decides(r.Context(), me, s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	for _, h := range handles {
		if h == c.To {
			return c
		}
	}
	if !decide && c.From == s.ID && s.can(directory.RoleMember) {
		return c
	}
	apiError(w, http.StatusNotFound, "no such connection")
	return nil
}

func (p *Platform) handleDecideConnection(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	c := p.connection(w, r, me, s, true)
	if c == nil {
		return
	}
	var body struct {
		Status     string
		RatePerMin int
	}
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {status, ratePerMin}")
		return
	}
	if err := p.Directory.DecideConnection(r.Context(), c.ID, body.Status, body.RatePerMin, me.ID); err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	c, err := p.Directory.Connection(r.Context(), c.ID)
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": c})
}

// handleEndConnection withdraws a request (the asking workspace) or ends a connection
// (the address's side).
func (p *Platform) handleEndConnection(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	c := p.connection(w, r, me, s, false)
	if c == nil {
		return
	}
	if err := p.Directory.DeleteConnection(r.Context(), c.ID); err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mailDeciders emails the people who decide a connection request: the person asked, or
// the admins of the agent's workspace (those with email notifications on).
func (p *Platform) mailDeciders(ctx context.Context, addr *directory.Address, c *directory.Connection) {
	if p.cfg.Email == nil {
		return
	}
	var to []*directory.User
	if addr.Kind == directory.KindHuman {
		if u, err := p.Directory.UserByHandle(ctx, addr.Handle); err == nil {
			to = append(to, u)
		}
	} else if members, err := p.Directory.Members(ctx, addr.WorkspaceID); err == nil {
		for _, m := range members {
			if directory.RoleAtLeast(m.Role, directory.RoleAdmin) {
				to = append(to, &m.User)
			}
		}
	}
	subject := fmt.Sprintf("%s asks to reach @%s on Lagrange", c.FromName, c.To)
	body := fmt.Sprintf("%s (asked by @%s) would like its agents and people to be able to send tasks to @%s.\n\n", c.FromName, c.RequestedBy, c.To)
	if c.Note != "" {
		body += "Their note:\n\n> " + c.Note + "\n\n"
	}
	body += "Accept, decline or block it in Settings → Connections:\n" + p.cfg.PublicURL + "/settings#connections\n"
	for _, u := range to {
		if s, err := p.Directory.NotificationSettings(ctx, u.ID); err != nil || !s.Email || u.Email == "" || !p.limits.allowMail(u.Email) {
			continue
		}
		p.mail.Add(1)
		go func(email string) {
			defer p.mail.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := p.cfg.Email.Send(ctx, email, subject, body); err != nil {
				p.log.Error("platform: sending a connection request", "error", err)
			}
		}(u.Email)
	}
}
