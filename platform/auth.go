package platform

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/tunnel"
)

// SessionCookie holds a person's web session.
const SessionCookie = "lagrange_session"

type identityKey struct{}

// identity is who is calling: an agent with its bearer token, or a person with their
// session cookie. The handle becomes the a2asrv user name, so tasks are owned by their
// sender and the graph can draw who called whom.
type identity struct {
	handle string
	kind   string
	user   *directory.User
}

// identify resolves the caller of an A2A request. Requests without credentials are
// anonymous; a wrong token is rejected rather than silently treated as anonymous.
func (p *Platform) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := p.identityOf(r)
		if err != nil {
			http.Error(w, "unknown token", http.StatusUnauthorized)
			return
		}
		if id != nil {
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Platform) identityOf(r *http.Request) (*identity, error) {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
		handle, err := p.Directory.TokenHandle(r.Context(), token)
		if err != nil {
			return nil, err
		}
		return &identity{handle: handle, kind: directory.KindAgent}, nil
	}
	if u := p.sessionUser(r); u != nil {
		return &identity{handle: u.Handle, kind: directory.KindHuman, user: u}, nil
	}
	return nil, nil
}

func (p *Platform) sessionUser(r *http.Request) *directory.User {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := p.Directory.Session(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return u
}

// identityInterceptor copies the HTTP caller's identity into the a2asrv CallContext.
// Calls the platform makes itself (the inbox) set the CallContext user directly and
// carry no identity, so they are left alone.
type identityInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

func (identityInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	if id, ok := ctx.Value(identityKey{}).(*identity); ok {
		callCtx.User = a2asrv.NewAuthenticatedUser(id.handle, map[string]any{"kind": id.kind})
	}
	return ctx, nil, nil
}

// sessionRegistry tracks live tunnel sessions for the API and raw passthrough.
type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[*tunnel.Session]struct{}
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: map[*tunnel.Session]struct{}{}}
}

func (r *sessionRegistry) add(s *tunnel.Session) {
	r.mu.Lock()
	r.sessions[s] = struct{}{}
	r.mu.Unlock()
}

func (r *sessionRegistry) remove(s *tunnel.Session) {
	r.mu.Lock()
	delete(r.sessions, s)
	r.mu.Unlock()
}

func (r *sessionRegistry) of(handle string) []*tunnel.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*tunnel.Session
	for s := range r.sessions {
		if s.Handle() == handle {
			out = append(out, s)
		}
	}
	return out
}

func (r *sessionRegistry) exposed(handle string) *tunnel.Session {
	for _, s := range r.of(handle) {
		if s.Expose() {
			return s
		}
	}
	return nil
}

func (r *sessionRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for s := range r.sessions {
		_ = s.Close()
	}
}
