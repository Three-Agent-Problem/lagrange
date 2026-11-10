package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/three-agent-problem/registry/lint"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/credentials"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/mailbox"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/relay"
)

func (p *Platform) api(mux *http.ServeMux) {
	mux.Handle("POST /api/login", p.public(p.handleLogin))
	mux.HandleFunc("POST /api/logout", p.handleLogout)
	mux.Handle("GET /api/me", p.session(p.handleMe, false, true))
	p.keyRoutes(mux)
	p.webhookRoutes(mux)
	p.accountRoutes(mux)
	p.workspaceRoutes(mux)
	p.accessRoutes(mux)
	p.profileRoutes(mux)
	mux.Handle("GET /api/users", p.in(directory.RoleViewer, p.handleUsers))
	mux.Handle("POST /api/users", p.in(directory.RoleAdmin, p.handleCreateUser))
	mux.Handle("GET /api/addresses", p.in(directory.RoleViewer, p.handleAddresses))
	mux.Handle("POST /api/addresses", p.in(directory.RoleMember, p.handleCreateAddress))
	mux.Handle("DELETE /api/addresses/{handle}", p.in(directory.RoleMember, p.handleDeleteAddress))
	mux.Handle("GET /api/addresses/{handle}/tokens", p.in(directory.RoleMember, p.handleTokens))
	mux.Handle("POST /api/addresses/{handle}/tokens", p.in(directory.RoleMember, p.handleMintToken))
	mux.Handle("GET /api/inbox", p.authed(p.handleInbox))
	mux.Handle("GET /api/tasks", p.in(directory.RoleViewer, p.handleTasks))
	mux.Handle("GET /api/tasks/{id}", p.in(directory.RoleViewer, p.handleTask))
	mux.Handle("POST /api/tasks/{id}/answer", p.in(directory.RoleViewer, p.handleAnswer))
	mux.Handle("POST /api/tasks/{id}/cancel", p.in(directory.RoleViewer, p.handleCancel))
	mux.Handle("POST /api/send", p.in(directory.RoleMember, p.handleSend))
	mux.Handle("GET /api/graph", p.in(directory.RoleViewer, p.handleGraph))
	mux.Handle("GET /api/runs", p.in(directory.RoleViewer, p.handleRuns))
	mux.Handle("GET /api/runs/{id}", p.in(directory.RoleViewer, p.handleRun))
	mux.Handle("POST /api/runs/{id}/archive", p.in(directory.RoleMember, p.handleArchiveRun))
	mux.Handle("DELETE /api/runs/{id}/archive", p.in(directory.RoleMember, p.handleArchiveRun))
	mux.Handle("GET /api/activity", p.in(directory.RoleViewer, p.handleActivity))
	mux.Handle("GET /api/settings/notifications", p.authed(p.handleNotificationSettings))
	mux.Handle("PUT /api/settings/notifications", p.authed(p.handleSetNotificationSettings))
	mux.Handle("POST /api/settings/notifications/test", p.authed(p.handleTestNotification))
	mux.Handle("POST /api/dev/reset", p.authed(p.handleDevReset))
	mux.Handle("GET /api/conversations", p.in(directory.RoleViewer, p.handleConversations))
	mux.Handle("GET /api/conversations/{a}/{b}", p.in(directory.RoleViewer, p.handleConversation))
	mux.Handle("GET /api/waiting", p.in(directory.RoleViewer, p.handleWaiting))
	mux.Handle("POST /api/waiting/{id}/remind", p.in(directory.RoleMember, p.handleRemind))
	mux.Handle("GET /api/settings/wait-policy", p.in(directory.RoleViewer, p.handleWaitPolicy))
	mux.Handle("PUT /api/settings/wait-policy", p.in(directory.RoleAdmin, p.handleSetWaitPolicy))
	// Decision links carry their own credential, so they need no session.
	mux.HandleFunc("GET /api/decisions/{token}", p.handleDecision)
	mux.HandleFunc("POST /api/decisions/{token}", p.handleDecisionAnswer)
	mux.Handle("GET /api/events", p.in(directory.RoleViewer, p.handleEvents))
}

type apiHandler func(w http.ResponseWriter, r *http.Request, me *directory.User)

// authed requires a session of someone who confirmed their email. State-changing
// requests must be JSON, which a cross-site form can't send without a CORS preflight;
// with the SameSite=Lax cookie that keeps the API safe from CSRF.
// API keys work only where a handler opts in (workspace routes, through in); everything
// else, such as managing keys, passwords and workspaces, needs a real session.
func (p *Platform) authed(h apiHandler) http.Handler { return p.session(h, true, false) }

// authedAny is authed for people who haven't confirmed their email yet.
func (p *Platform) authedAny(h apiHandler) http.Handler { return p.session(h, false, false) }

func (p *Platform) session(h apiHandler, verified, keys bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var me *directory.User
		if secret, ok := bearerKey(r); ok {
			if !keys {
				apiError(w, http.StatusForbidden, "API keys can't do this: log in to the web app")
				return
			}
			u, grant, err := p.Directory.APIKeyUser(r.Context(), secret)
			if err != nil {
				apiError(w, http.StatusUnauthorized, "unknown or revoked API key")
				return
			}
			if grant.Role == directory.RoleViewer && r.Method != http.MethodGet && r.Method != http.MethodHead {
				apiError(w, http.StatusForbidden, "this API key is read-only (viewer)")
				return
			}
			me = u
			r = r.WithContext(context.WithValue(r.Context(), keyGrantKey{}, grant))
		} else {
			me = p.sessionUser(r)
		}
		if me == nil {
			apiError(w, http.StatusUnauthorized, "log in first")
			return
		}
		if verified && !me.Verified {
			apiError(w, http.StatusForbidden, "confirm your email first: open the link we sent you")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				apiError(w, http.StatusUnsupportedMediaType, "send JSON")
				return
			}
		}
		h(w, r, me)
	})
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func (p *Platform) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Email, Password string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {email, password}")
		return
	}
	u, token, err := p.Directory.Login(r.Context(), body.Email, body.Password)
	if errors.Is(err, directory.ErrBadCredentials) {
		apiError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.setSession(w, token)
	writeJSON(w, http.StatusOK, u)
}

func (p *Platform) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = p.Directory.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (p *Platform) handleMe(w http.ResponseWriter, r *http.Request, me *directory.User) {
	identities, err := p.Directory.Identities(r.Context(), me.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	workspaces, err := p.Directory.Workspaces(r.Context(), me.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{
		"user": me, "identities": identities, "workspaces": workspaces, "auth": p.authMethods(),
		"publicUrl": p.cfg.PublicURL, "dev": p.cfg.Dev, "devSeed": p.seed != nil,
	}
	if g := keyGrantOf(r.Context()); g != nil {
		out["key"] = map[string]string{"workspace": g.Workspace, "role": g.Role}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUsers lists the workspace's members.
func (p *Platform) handleUsers(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	users, err := p.Directory.Members(r.Context(), s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleCreateUser creates a person with a password, as a member of the workspace (or
// with ?role). Invitations are the friendlier way; this one is for scripts.
func (p *Platform) handleCreateUser(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body struct{ Email, Name, Handle, Password, Role string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {email, name, handle, password, role}")
		return
	}
	role, ok := p.grantable(w, s, body.Role)
	if !ok {
		return
	}
	u, err := p.Directory.Register(r.Context(), directory.Registration{Email: body.Email, Name: body.Name, Handle: body.Handle,
		Password: body.Password, Verified: true, Workspace: s.ID, Role: role})
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	p.addressChanged(r.Context(), u.Handle)
	writeJSON(w, http.StatusCreated, u)
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, directory.ErrTaken):
		return http.StatusConflict
	case errors.Is(err, directory.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, directory.ErrInvalidHandle):
		return http.StatusBadRequest
	case errors.Is(err, a2a.ErrTaskNotFound):
		return http.StatusNotFound
	case errors.Is(err, a2a.ErrInvalidParams), errors.Is(err, a2a.ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, a2a.ErrUnauthorized):
		return http.StatusForbidden
	case errors.Is(err, a2a.ErrUnauthenticated):
		return http.StatusUnauthorized
	}
	if strings.HasPrefix(err.Error(), "directory: ") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (p *Platform) addressChanged(ctx context.Context, handle string) {
	_ = p.Activity.Add(ctx, &activity.Entry{Kind: activity.KindAddress, Recipient: handle})
}

// AddressView is an address as the web app sees it.
type AddressView struct {
	*directory.Address
	Card    *a2a.AgentCard `json:"card"`
	CardURL string         `json:"cardUrl"`
	Online  bool           `json:"online"`
	// External addresses belong to another workspace: they show because they talked to
	// this one.
	External bool          `json:"external,omitempty"`
	Stats    mailbox.Stats `json:"stats"`
	Sessions []SessionView `json:"sessions"`
}

// SessionView is a connected tunnel.
type SessionView struct {
	ID        string    `json:"id"`
	Capacity  int       `json:"capacity"`
	Agent     string    `json:"agent"`
	Expose    bool      `json:"expose"`
	Connected time.Time `json:"connected"`
}

func (p *Platform) addressView(a *directory.Address) *AddressView {
	v := &AddressView{Address: a, Card: p.Card(a), CardURL: p.cfg.PublicURL + "/a/" + a.Handle + a2asrv.WellKnownAgentCardPath, Sessions: []SessionView{}}
	v.Address.Card = nil // the platform card replaces the agent's own one in the view
	v.Stats = p.Mailbox.Stats(a.Handle)
	for _, s := range p.sessions.of(a.Handle) {
		v.Sessions = append(v.Sessions, SessionView{ID: s.ID(), Capacity: s.Capacity(), Agent: s.Agent(), Expose: s.Expose(), Connected: s.Connected()})
	}
	v.Online = a.Kind == directory.KindHuman || a.Endpoint != "" || v.Stats.Sessions > 0
	return v
}

// addressViews lists a workspace's addresses, or with s nil every address.
func (p *Platform) addressViews(ctx context.Context, s *scope) ([]*AddressView, error) {
	var addrs []*directory.Address
	var err error
	if s == nil {
		addrs, err = p.Directory.Addresses(ctx)
	} else {
		addrs, err = p.Directory.AddressesIn(ctx, s.ID)
	}
	if err != nil {
		return nil, err
	}
	views := make([]*AddressView, len(addrs))
	for i, a := range addrs {
		views[i] = p.addressView(a)
	}
	return views, nil
}

func (p *Platform) handleAddresses(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	views, err := p.addressViews(r.Context(), s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": views})
}

// TunnelCommand is the command that connects an agent to this server.
func (p *Platform) TunnelCommand(token string) string {
	return fmt.Sprintf("lagrange tunnel --server %s --token %s --to http://localhost:9001", p.cfg.PublicURL, token)
}

func (p *Platform) handleCreateAddress(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body struct{ Handle, DisplayName, Description, Endpoint, Access string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {handle, displayName, description, endpoint, access}")
		return
	}
	a := &directory.Address{Handle: strings.ToLower(strings.TrimSpace(body.Handle)), OwnerID: me.ID, WorkspaceID: s.ID,
		DisplayName: body.DisplayName, Description: body.Description, Endpoint: strings.TrimSuffix(body.Endpoint, "/"), Access: body.Access}
	if err := p.Directory.CreateAgent(r.Context(), a); err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	token, err := p.Directory.MintToken(r.Context(), a.Handle, "created with the address")
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.addressChanged(r.Context(), a.Handle)
	writeJSON(w, http.StatusCreated, map[string]any{"address": p.addressView(a), "token": token, "command": p.TunnelCommand(token)})
}

// mayManage reports whether me may change an agent of the workspace: its admins may,
// and so may the member who created it.
func mayManage(me *directory.User, s *scope, a *directory.Address) bool {
	return s.can(directory.RoleAdmin) || (a.OwnerID != 0 && a.OwnerID == me.ID)
}

func (p *Platform) managedAddress(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) *directory.Address {
	a, err := p.Directory.Address(r.Context(), r.PathValue("handle"))
	if err != nil || a.Kind == directory.KindAgent && a.WorkspaceID != s.ID {
		apiError(w, http.StatusNotFound, "no such address")
		return nil
	}
	if a.Kind != directory.KindAgent || !mayManage(me, s, a) {
		apiError(w, http.StatusForbidden, "you don't manage this agent")
		return nil
	}
	return a
}

func (p *Platform) handleDeleteAddress(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	a := p.managedAddress(w, r, me, s)
	if a == nil {
		return
	}
	if err := p.Directory.DeleteAddress(r.Context(), a.Handle); err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	for _, s := range p.sessions.of(a.Handle) {
		_ = s.Close()
	}
	p.addressChanged(r.Context(), a.Handle)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Platform) handleTokens(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	a := p.managedAddress(w, r, me, s)
	if a == nil {
		return
	}
	tokens, err := p.Directory.Tokens(r.Context(), a.Handle)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (p *Platform) handleMintToken(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	a := p.managedAddress(w, r, me, s)
	if a == nil {
		return
	}
	var body struct{ Label string }
	_ = decode(r, &body)
	token, err := p.Directory.MintToken(r.Context(), a.Handle, body.Label)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "command": p.TunnelCommand(token)})
}

// TaskView is a task as the web app sees it.
type TaskView struct {
	ID           a2a.TaskID `json:"id"`
	ContextID    string     `json:"contextId"`
	State        string     `json:"state"`
	Sender       string     `json:"sender"`
	Recipient    string     `json:"recipient"`
	ParentTaskID string     `json:"parentTaskId,omitempty"`
	Summary      string     `json:"summary"`
	// Status is what the task's latest status says, in words (empty while it waits on a person).
	Status    string                 `json:"status,omitempty"`
	Awaiting  string                 `json:"awaiting,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
	Task      *a2a.Task              `json:"task,omitempty"`
	Input     *InputView             `json:"input,omitempty"`
	Challenge *credentials.Challenge `json:"challenge,omitempty"`
}

// InputView is a structured input request, for rendering a form.
type InputView struct {
	ID     string          `json:"id,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

const taskSelect = `SELECT t.id, t.context_id, t.state, t.tenant, coalesce(l.sender, t.owner), coalesce(l.parent_task_id, ''), t.created_at, t.updated_at, t.task
	FROM tasks t LEFT JOIN task_links l ON l.task_id = t.id `

func (p *Platform) queryTasks(ctx context.Context, full bool, where string, args ...any) ([]*TaskView, error) {
	rows, err := p.DB.Pool.Query(ctx, taskSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskView
	for rows.Next() {
		v := &TaskView{}
		var raw []byte
		if err := rows.Scan(&v.ID, &v.ContextID, &v.State, &v.Recipient, &v.Sender, &v.ParentTaskID, &v.CreatedAt, &v.UpdatedAt, &raw); err != nil {
			return nil, err
		}
		var task a2a.Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, err
		}
		fillTaskView(v, &task)
		if full {
			v.Task = &task
		}
		out = append(out, v)
	}
	if out == nil {
		out = []*TaskView{}
	}
	return out, rows.Err()
}

func fillTaskView(v *TaskView, task *a2a.Task) {
	if len(task.History) > 0 {
		v.Summary = relay.Describe(task.History[0])
	}
	if msg := task.Status.Message; msg != nil {
		if h, ok := msg.Metadata[relay.AwaitingKey].(string); ok {
			v.Awaiting = h
		} else {
			v.Status = clip(relay.Describe(msg), 600)
		}
		if task.Status.State == a2a.TaskStateInputRequired {
			if req, err := structuredinput.ParseRequest(msg); err == nil && req != nil && req.Schema != nil {
				schema, _ := json.Marshal(req.Schema)
				v.Input = &InputView{ID: req.ID, Schema: schema}
			}
		}
		if ch, ok := credentials.ChallengeFromMessage(msg); ok {
			v.Challenge = ch
		}
	}
	if n := len(task.Artifacts); v.Status == "" && n > 0 && task.Status.State.Terminal() {
		// A task that ends with a result and no closing words: the result says it.
		v.Status = clip(relay.Describe(&a2a.Message{Parts: task.Artifacts[n-1].Parts}), 600)
	}
}

// handleInbox lists the interrupted tasks that wait for me: the ones addressed to me,
// and the ones I sent that need my answer.
func (p *Platform) handleInbox(w http.ResponseWriter, r *http.Request, me *directory.User) {
	tasks, err := p.queryTasks(r.Context(), true, `WHERE t.state IN ($2, $3) AND (t.tenant = $1 OR coalesce(l.sender, t.owner) = $1)
		ORDER BY t.updated_at DESC LIMIT 200`, me.Handle, string(a2a.TaskStateInputRequired), string(a2a.TaskStateAuthRequired))
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handleTasks lists the workspace's recent tasks; with ?mine=1 only the ones I sent or
// received.
func (p *Platform) handleTasks(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var tasks []*TaskView
	var err error
	if r.URL.Query().Get("mine") == "1" {
		tasks, err = p.queryTasks(r.Context(), true, `WHERE t.tenant = $1 OR coalesce(l.sender, t.owner) = $1 ORDER BY t.updated_at DESC LIMIT $2`, me.Handle, limit)
	} else {
		tasks, err = p.queryTasks(r.Context(), false, `WHERE `+visibleSQL(2)+` ORDER BY t.updated_at DESC LIMIT $1`, limit, s.handles)
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (p *Platform) handleTask(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	id := r.PathValue("id")
	tasks, err := p.queryTasks(r.Context(), true, `WHERE t.id = $1 AND `+visibleSQL(2), id, s.handles)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(tasks) == 0 {
		apiError(w, http.StatusNotFound, "no such task")
		return
	}
	entries, err := p.Activity.Task(r.Context(), id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	children, err := p.Links.Children(r.Context(), a2a.TaskID(id))
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []*activity.Entry{}
	}
	if children == nil {
		children = []*relay.Link{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": tasks[0], "activity": entries, "children": children})
}

// callAs returns ctx with an a2asrv CallContext for user, as the platform's own calls
// into the A2A handler use.
func callAs(ctx context.Context, user string, attrs map[string]any) context.Context {
	ctx, callCtx := a2asrv.NewCallContext(ctx, a2asrv.NewServiceParams(nil))
	callCtx.User = a2asrv.NewAuthenticatedUser(user, attrs)
	return ctx
}

type sendBody struct {
	To        string         `json:"to"`
	Text      string         `json:"text"`
	Data      map[string]any `json:"data"`
	Files     []sendFile     `json:"files"`
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	// Tags are recorded with the task (relay.TagsKey); Metadata is the message's
	// metadata, e.g. {"acpSettings": {"model": "opus"}} for a coding agent.
	Tags     map[string]string `json:"tags"`
	Metadata map[string]any    `json:"metadata"`
}

// sendFile is a file in a message: its bytes (base64 in JSON) or a URL.
type sendFile struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
	URL       string `json:"url"`
}

func (b *sendBody) parts() ([]*a2a.Part, error) {
	var parts []*a2a.Part
	if t := strings.TrimSpace(b.Text); t != "" {
		parts = append(parts, a2a.NewTextPart(t))
	}
	if b.Data != nil {
		parts = append(parts, a2a.NewDataPart(b.Data))
	}
	for i, f := range b.Files {
		var part *a2a.Part
		switch {
		case len(f.Data) > 0 && f.URL == "":
			part = a2a.NewRawPart(f.Data)
			part.MediaType = f.MediaType
			if part.MediaType == "" {
				part.MediaType = mime.TypeByExtension(path.Ext(f.Name))
			}
			if part.MediaType == "" {
				part.MediaType = http.DetectContentType(f.Data)
			}
		case f.URL != "" && len(f.Data) == 0:
			u, err := url.Parse(f.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return nil, fmt.Errorf("files[%d]: url must be http(s)", i)
			}
			mt := f.MediaType
			if mt == "" {
				mt = mime.TypeByExtension(path.Ext(u.Path))
			}
			part = a2a.NewFileURLPart(a2a.URL(f.URL), mt)
		default:
			return nil, fmt.Errorf("files[%d]: give data (base64) or url, not both", i)
		}
		if f.Name != "" {
			part.Filename = path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// handleSend sends a message from me to an address, as a new task or into one of my tasks.
func (p *Platform) handleSend(w http.ResponseWriter, r *http.Request, me *directory.User, _ *scope) {
	var body sendBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {to, text, data, files, taskId, contextId, tags, metadata} (at most 8 MB)")
		return
	}
	body.To = strings.TrimPrefix(strings.TrimSpace(body.To), "@")
	if _, err := p.Directory.Address(r.Context(), body.To); err != nil {
		apiError(w, http.StatusNotFound, "no such address: @"+body.To)
		return
	}
	parts, err := body.parts()
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(parts) == 0 {
		apiError(w, http.StatusBadRequest, "the message is empty")
		return
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, parts...)
	msg.TaskID = a2a.TaskID(body.TaskID)
	msg.ContextID = body.ContextID
	if len(body.Metadata) > 0 {
		msg.Metadata = body.Metadata
	}
	if len(body.Tags) > 0 {
		if msg.Metadata == nil {
			msg.Metadata = map[string]any{}
		}
		tags := map[string]any{}
		for k, v := range body.Tags {
			tags[k] = v
		}
		msg.Metadata[relay.TagsKey] = tags
	}
	res, err := p.A2A.SendMessage(callAs(r.Context(), me.Handle, map[string]any{"kind": directory.KindHuman}), &a2a.SendMessageRequest{
		Tenant: body.To, Message: msg, Config: &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

type answerBody struct {
	Text     string         `json:"text"`
	Data     map[string]any `json:"data"`
	Decision string         `json:"decision"`
}

// handleAnswer answers a task waiting for input: a person answering a task addressed to
// them, or the sender answering the agent's question. With a structured input request,
// data is the answer object.
func (p *Platform) handleAnswer(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body answerBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {text, data, decision}")
		return
	}
	if status, err := p.answer(r.Context(), me, s, r.PathValue("id"), &body); err != nil {
		apiError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// answer answers task id as me. With s, admins of the workspace may answer for the
// workspace's agents. On failure it returns the HTTP status to report.
func (p *Platform) answer(ctx context.Context, me *directory.User, s *scope, id string, body *answerBody) (int, error) {
	stored, tenant, err := p.Tasks.Load(ctx, a2a.TaskID(id))
	if err != nil {
		return statusOf(err), err
	}
	task := stored.Task
	if task.Status.State != a2a.TaskStateInputRequired {
		return http.StatusConflict, errors.New("the task isn't waiting for an answer")
	}
	attrs := map[string]any{"kind": directory.KindHuman}
	switch {
	case tenant == me.Handle:
		// A task for me: answer on the sender's behalf, as the person it waits for.
		attrs[relay.AttrAnsweredBy] = me.Handle
		if body.Decision == "reject" {
			attrs[relay.AttrDecision] = "reject"
		}
	case stored.User != me.Handle && p.escalatedTo(ctx, id) == me.Handle:
		// Escalated to me: answer on behalf of the person it waited for.
		attrs[relay.AttrAnsweredBy] = me.Handle
		attrs[relay.AttrAnswersFor] = tenant
		if body.Decision == "reject" {
			attrs[relay.AttrDecision] = "reject"
		}
	case stored.User == me.Handle, s != nil && s.can(directory.RoleAdmin) && s.in[stored.User]:
	default:
		return http.StatusForbidden, errors.New("this task doesn't wait for you")
	}

	var msg *a2a.Message
	req, err := structuredinput.ParseRequest(task.Status.Message)
	if err == nil && req != nil && body.Data == nil {
		// --approve from the CLI, MCP or the API: fill in an approval form's one boolean.
		body.Data = decisionData(req.Schema, body.Decision)
	}
	if err == nil && req != nil && body.Data != nil {
		var extra []*a2a.Part
		if t := strings.TrimSpace(body.Text); t != "" {
			extra = append(extra, a2a.NewTextPart(t))
		}
		if msg, err = structuredinput.NewResponse(task, req, body.Data, extra...); err != nil {
			return http.StatusBadRequest, err
		}
	} else {
		parts, _ := (&sendBody{Text: body.Text, Data: body.Data}).parts() // no files: no error
		if len(parts) == 0 {
			return http.StatusBadRequest, errors.New("the answer is empty")
		}
		msg = a2a.NewMessageForTask(a2a.MessageRoleUser, task, parts...)
	}
	msg.SetMeta(relay.AnsweredByKey, me.Handle)
	if _, err := p.A2A.SendMessage(callAs(ctx, stored.User, attrs), &a2a.SendMessageRequest{
		Tenant: tenant, Message: msg, Config: &a2a.SendMessageConfig{ReturnImmediately: true},
	}); err != nil {
		return statusOf(err), err
	}
	return 0, nil
}

// decisionData is the answer an approve or reject decision gives to a form whose only
// field is a boolean ({"approved": true}), or nil when the form asks for more.
func decisionData(schema *jsonschema.Schema, decision string) map[string]any {
	if schema == nil || (decision != "approve" && decision != "reject") || len(schema.Properties) != 1 {
		return nil
	}
	for name, prop := range schema.Properties {
		if prop != nil && prop.Type == "boolean" {
			return map[string]any{name: decision == "approve"}
		}
	}
	return nil
}

const linkGone = "This link was already used or has expired. Open your inbox to see the task."

// handleDecision returns the task a decision link answers, and who it is for. The link
// is the credential and reaches only this one task.
func (p *Platform) handleDecision(w http.ResponseWriter, r *http.Request) {
	link, err := p.Directory.DecisionLink(r.Context(), r.PathValue("token"))
	if errors.Is(err, directory.ErrNotFound) {
		apiError(w, http.StatusGone, linkGone)
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := p.queryTasks(r.Context(), true, `WHERE t.id = $1`, link.TaskID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := p.Directory.UserByHandle(r.Context(), link.Handle)
	if len(tasks) == 0 || err != nil {
		apiError(w, http.StatusGone, linkGone)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task": tasks[0], "for": map[string]string{"handle": u.Handle, "name": u.Name}, "expiresAt": link.ExpiresAt,
	})
}

// handleDecisionAnswer answers the task of a decision link as the person it was sent
// to, and uses the link up.
func (p *Platform) handleDecisionAnswer(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		apiError(w, http.StatusUnsupportedMediaType, "send JSON")
		return
	}
	var body answerBody
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {text, data, decision}")
		return
	}
	ctx, token := r.Context(), r.PathValue("token")
	link, err := p.Directory.ClaimDecisionLink(ctx, token)
	if errors.Is(err, directory.ErrNotFound) {
		apiError(w, http.StatusGone, linkGone)
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status, err := http.StatusGone, errors.New(linkGone)
	if u, uerr := p.Directory.UserByHandle(ctx, link.Handle); uerr == nil {
		status, err = p.answer(ctx, u, nil, link.TaskID, &body)
	}
	if err != nil {
		// The answer didn't go through, so the link still works.
		if rerr := p.Directory.ReleaseDecisionLink(context.WithoutCancel(ctx), token); rerr != nil {
			p.log.Warn("platform: releasing a decision link failed", "error", rerr)
		}
		apiError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (p *Platform) handleNotificationSettings(w http.ResponseWriter, r *http.Request, me *directory.User) {
	s, err := p.Directory.NotificationSettings(r.Context(), me.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "emailAvailable": p.cfg.Email != nil, "email": me.Email})
}

func (p *Platform) handleSetNotificationSettings(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var s directory.NotificationSettings
	if err := decode(r, &s); err != nil {
		apiError(w, http.StatusBadRequest, "send {email, slackWebhook}")
		return
	}
	s.SlackWebhook = strings.TrimSpace(s.SlackWebhook)
	if s.SlackWebhook != "" && !notify.ValidSlackWebhook(s.SlackWebhook) {
		apiError(w, http.StatusBadRequest, "a Slack incoming webhook URL starts with https://hooks.slack.com/")
		return
	}
	if err := p.Directory.SetNotificationSettings(r.Context(), me.ID, &s); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "emailAvailable": p.cfg.Email != nil, "email": me.Email})
}

func (p *Platform) handleTestNotification(w http.ResponseWriter, r *http.Request, me *directory.User) {
	if err := p.Notify.Test(r.Context(), me); err != nil {
		apiError(w, http.StatusBadGateway, "the test notification failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCancel cancels a task: its sender or recipient may, and so may the workspace's
// admins for the workspace's tasks.
func (p *Platform) handleCancel(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	stored, tenant, err := p.Tasks.Load(r.Context(), a2a.TaskID(r.PathValue("id")))
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	if stored.User != me.Handle && tenant != me.Handle && !(s.can(directory.RoleAdmin) && s.sees(stored.User, tenant)) {
		apiError(w, http.StatusForbidden, "only the sender can cancel this task")
		return
	}
	task, err := p.A2A.CancelTask(callAs(r.Context(), stored.User, nil), &a2a.CancelTaskRequest{Tenant: tenant, ID: stored.Task.ID})
	if err != nil {
		apiError(w, statusOf(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

// handleGraph is the snapshot the live graph starts from; /api/events continues it. It
// has the workspace's addresses, and the outside ones its tasks went to or came from.
func (p *Platform) handleGraph(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	seq, err := p.Activity.Last(ctx)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views, err := p.addressViews(ctx, s)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := p.queryTasks(ctx, false, `WHERE t.updated_at > now() - interval '24 hours' AND `+visibleSQL(1)+` ORDER BY t.updated_at DESC LIMIT 300`, s.handles)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views, err = p.withExternal(ctx, s, views, tasks); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "addresses": views, "tasks": tasks})
}

// withExternal adds the addresses of other workspaces that tasks went to or came from,
// marked external and without their tunnels.
func (p *Platform) withExternal(ctx context.Context, s *scope, views []*AddressView, tasks []*TaskView) ([]*AddressView, error) {
	seen := map[string]bool{}
	for _, t := range tasks {
		for _, h := range []string{t.Sender, t.Recipient} {
			if h == "" || s.in[h] || seen[h] {
				continue
			}
			seen[h] = true
			a, err := p.Directory.Address(ctx, h)
			if errors.Is(err, directory.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			v := p.addressView(a)
			v.External, v.Sessions, v.Stats = true, []SessionView{}, mailbox.Stats{}
			views = append(views, v)
		}
	}
	return views, nil
}

// handleEvents streams the workspace's activity. The workspace's addresses are looked
// up again whenever an address changes, so new agents and members show up.
func (p *Platform) handleEvents(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	ctx := r.Context()
	p.Activity.Stream(w, r, func(e *activity.Entry) bool {
		if e.Kind == activity.KindAddress {
			if fresh, err := p.scopeFor(ctx, s.Workspace); err == nil {
				s = fresh
			}
		}
		return s.entry(e)
	})
}

// handleActivity pages through the activity log (?since=seq&limit=n), for replay.
func (p *Platform) handleActivity(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	entries, err := p.Activity.SinceFor(r.Context(), since, limit, s.handles)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []*activity.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleRegistrySearch answers like the registry's GET /agents, so agents built on the
// registry client find each other on Lagrange. ?skill matches a skill ID or tag, ?q the
// name, description and skills; offline agents are left out unless ?all=1, and addresses
// that take tasks from their workspace only are never listed. Each card is graded by the
// registry's linter, as served here.
func (p *Platform) handleRegistrySearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	skill, text, all := strings.ToLower(q.Get("skill")), strings.ToLower(q.Get("q")), q.Get("all") == "1"
	views, err := p.addressViews(r.Context(), nil)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entry struct {
		URL        string         `json:"url"`
		CardURL    string         `json:"cardUrl"`
		Card       *a2a.AgentCard `json:"card"`
		Report     lint.Report    `json:"report"`
		Registered time.Time      `json:"registered"`
		LastSeen   time.Time      `json:"lastSeen"`
		Up         bool           `json:"up"`
	}
	agents := []entry{}
	now := time.Now()
	for _, v := range views {
		if !v.Online && !all || v.Access == directory.AccessWorkspace {
			continue
		}
		if skill != "" && !hasSkill(v.Card, skill) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(v.Card.Name+" "+v.Card.Description+" "+skillText(v.Card)), text) {
			continue
		}
		raw, _ := json.Marshal(v.Card)
		agents = append(agents, entry{
			URL: p.cfg.PublicURL + "/a/" + v.Handle, CardURL: v.CardURL, Card: v.Card,
			Report: lint.Check(raw), Registered: v.CreatedAt, LastSeen: now, Up: v.Online,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func hasSkill(card *a2a.AgentCard, skill string) bool {
	for _, s := range card.Skills {
		if strings.ToLower(s.ID) == skill {
			return true
		}
		for _, t := range s.Tags {
			if strings.ToLower(t) == skill {
				return true
			}
		}
	}
	return false
}

func skillText(card *a2a.AgentCard) string {
	var b strings.Builder
	for _, s := range card.Skills {
		b.WriteString(s.ID + " " + s.Name + " " + s.Description + " " + strings.Join(s.Tags, " ") + " ")
	}
	return b.String()
}

// webHandler serves the web app with a fallback to index.html for client-side routes.
func webHandler(web fs.FS) http.Handler {
	if web == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Lagrange</title><body style="font-family:system-ui;padding:2rem"><h1>Lagrange</h1><p>The server is running. The web app wasn't built into this binary: run <code>npm run build</code> in <code>web/</code>, then rebuild.</p></body>`)
		})
	}
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decision links (/d/<token>) must not leak to other sites through Referer.
		w.Header().Set("Referrer-Policy", "same-origin")
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(web, name); err != nil {
			// A client-side route: let the app handle it.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// clip shortens s to at most n bytes, on a rune boundary, with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
