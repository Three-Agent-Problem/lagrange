package platform

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/directory"
)

// A workspace is a team: its agents and its members. Every request acts in one of the
// caller's workspaces and sees the traffic of that workspace's addresses only: a task is
// visible when its sender or its recipient is one of them. A call between two
// workspaces shows in both, and each side's internal work stays its own.

// WorkspaceHeader names the workspace a request acts in (its slug). The event stream,
// which can't set headers, takes ?workspace= instead. Without either, a request acts in
// the caller's oldest workspace.
const WorkspaceHeader = "Lagrange-Workspace"

// scope is what a request may see: a workspace, the caller's role in it, and its
// addresses.
type scope struct {
	*directory.Workspace
	handles []string
	in      map[string]bool
}

func newScope(ws *directory.Workspace, handles []string) *scope {
	s := &scope{Workspace: ws, handles: handles, in: make(map[string]bool, len(handles))}
	for _, h := range handles {
		s.in[h] = true
	}
	return s
}

// sees reports whether traffic between two addresses is visible.
func (s *scope) sees(sender, recipient string) bool { return s.in[sender] || s.in[recipient] }

func (s *scope) task(v *TaskView) bool { return s.sees(v.Sender, v.Recipient) }

// entry reports whether an activity entry is visible. Address entries with no address
// (a reset) reach everyone.
func (s *scope) entry(e *activity.Entry) bool {
	switch e.Kind {
	case activity.KindAddress, activity.KindPresence:
		return e.Recipient == "" || s.in[e.Recipient]
	}
	return s.sees(e.Sender, e.Recipient)
}

func (s *scope) can(role string) bool { return directory.RoleAtLeast(s.Role, role) }

// visibleSQL is the SQL condition for tasks the scope sees, on taskSelect's aliases, with
// the scope's handles as parameter $n.
func visibleSQL(n int) string {
	p := "$" + strconv.Itoa(n)
	return `(t.tenant = ANY(` + p + `) OR coalesce(l.sender, t.owner) = ANY(` + p + `))`
}

type scopedHandler func(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope)

// in requires a verified session and at least role in the request's workspace.
func (p *Platform) in(role string, h scopedHandler) http.Handler {
	return p.authed(func(w http.ResponseWriter, r *http.Request, me *directory.User) {
		s, status, err := p.scopeOf(r, me)
		if err != nil {
			apiError(w, status, err.Error())
			return
		}
		if !s.can(role) {
			apiError(w, http.StatusForbidden, "this needs the "+role+" role in "+s.Name)
			return
		}
		h(w, r, me, s)
	})
}

var errNoWorkspace = errors.New("you aren't in any workspace: create one or ask for an invitation")

func (p *Platform) scopeOf(r *http.Request, me *directory.User) (*scope, int, error) {
	ctx := r.Context()
	slug := r.Header.Get(WorkspaceHeader)
	if slug == "" {
		slug = r.URL.Query().Get("workspace")
	}
	var ws *directory.Workspace
	if slug != "" {
		var err error
		if ws, err = p.Directory.Membership(ctx, me.ID, slug); errors.Is(err, directory.ErrNotMember) {
			return nil, http.StatusForbidden, errors.New("you aren't a member of " + slug)
		} else if err != nil {
			return nil, http.StatusInternalServerError, err
		}
	} else {
		all, err := p.Directory.Workspaces(ctx, me.ID)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if len(all) == 0 {
			return nil, http.StatusForbidden, errNoWorkspace
		}
		ws = all[0]
	}
	s, err := p.scopeFor(ctx, ws)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return s, 0, nil
}

func (p *Platform) scopeFor(ctx context.Context, ws *directory.Workspace) (*scope, error) {
	handles, err := p.Directory.WorkspaceHandles(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	return newScope(ws, handles), nil
}

func (p *Platform) workspaceRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/workspaces", p.authed(p.handleWorkspaces))
	mux.Handle("POST /api/workspaces", p.authed(p.handleCreateWorkspace))
	mux.Handle("PUT /api/workspace", p.in(directory.RoleAdmin, p.handleRenameWorkspace))
	mux.Handle("GET /api/workspace/members", p.in(directory.RoleViewer, p.handleMembers))
	mux.Handle("PUT /api/workspace/members/{id}", p.in(directory.RoleOwner, p.handleSetRole))
	mux.Handle("DELETE /api/workspace/members/{id}", p.in(directory.RoleViewer, p.handleRemoveMember))
}

func (p *Platform) handleWorkspaces(w http.ResponseWriter, r *http.Request, me *directory.User) {
	all, err := p.Directory.Workspaces(r.Context(), me.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": all})
}

func (p *Platform) handleCreateWorkspace(w http.ResponseWriter, r *http.Request, me *directory.User) {
	var body struct{ Name, Slug string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {name, slug}")
		return
	}
	ws, err := p.Directory.CreateWorkspace(r.Context(), body.Name, body.Slug, me.ID)
	if err != nil {
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"workspace": ws})
}

func (p *Platform) handleRenameWorkspace(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	var body struct{ Name string }
	if err := decode(r, &body); err != nil {
		apiError(w, http.StatusBadRequest, "send {name}")
		return
	}
	if err := p.Directory.RenameWorkspace(r.Context(), s.ID, body.Name); err != nil {
		apiError(w, statusOf(err), userMessage(err))
		return
	}
	s.Name = body.Name
	writeJSON(w, http.StatusOK, map[string]any{"workspace": s.Workspace})
}

func (p *Platform) handleMembers(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	members, err := p.Directory.Members(r.Context(), s.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members, "workspace": s.Workspace})
}

func memberID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "members are named by their user ID")
		return 0, false
	}
	return id, true
}

func (p *Platform) memberError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrLastOwner):
		apiError(w, http.StatusConflict, "a workspace needs at least one owner: make someone else owner first")
	case errors.Is(err, directory.ErrNotMember):
		apiError(w, http.StatusNotFound, "no such member")
	default:
		apiError(w, statusOf(err), userMessage(err))
	}
}

// handleSetRole changes a member's role (owners only).
func (p *Platform) handleSetRole(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	id, ok := memberID(w, r)
	if !ok {
		return
	}
	var body struct{ Role string }
	if err := decode(r, &body); err != nil || !directory.ValidRole(body.Role) {
		apiError(w, http.StatusBadRequest, "send {role}: owner, admin, member or viewer")
		return
	}
	if err := p.Directory.SetRole(r.Context(), s.ID, id, body.Role); err != nil {
		p.memberError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveMember takes someone out of the workspace: owners remove anyone, everyone
// can leave.
func (p *Platform) handleRemoveMember(w http.ResponseWriter, r *http.Request, me *directory.User, s *scope) {
	id, ok := memberID(w, r)
	if !ok {
		return
	}
	if id != me.ID && !s.can(directory.RoleOwner) {
		apiError(w, http.StatusForbidden, "only owners can remove people")
		return
	}
	if err := p.Directory.RemoveMember(r.Context(), s.ID, id); err != nil {
		p.memberError(w, err)
		return
	}
	if u, err := p.Directory.UserByID(r.Context(), id); err == nil {
		p.addressChanged(r.Context(), u.Handle)
	}
	w.WriteHeader(http.StatusNoContent)
}

// grantable checks that the scope's member may give role (default member) to someone:
// admins give up to admin, owners any role.
func (p *Platform) grantable(w http.ResponseWriter, s *scope, role string) (string, bool) {
	if role == "" {
		role = directory.RoleMember
	}
	if !directory.ValidRole(role) {
		apiError(w, http.StatusBadRequest, "the role is owner, admin, member or viewer")
		return "", false
	}
	if role == directory.RoleOwner && !s.can(directory.RoleOwner) {
		apiError(w, http.StatusForbidden, "only owners can make someone an owner")
		return "", false
	}
	return role, true
}
