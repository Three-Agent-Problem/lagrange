// Package console is the seed of the runtime's dashboard. It receives the push
// notifications of every task an agent runs (see package observer), and shows:
//
//   - a task tree across agents, with time spent working vs waiting on a human vs waiting
//     on a credential (the "C" part: traces for free, because the events flow through us)
//   - each task's timeline and artifacts
//   - an inbox where a person answers tasks in INPUT_REQUIRED (the "B" part: a human in
//     the loop over plain A2A). The answer goes back to the agent as a structuredinput
//     response, and the task resumes.
//
// State lives in memory; a restart forgets everything.
package console

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2aext"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/three-agent-problem/lagrange/links"
)

// TokenHeader is the header a2a-go's push sender uses for PushConfig.Token.
const TokenHeader = "A2A-Notification-Token"

const maxMessages = 100

//go:embed templates/*.html
var templateFS embed.FS

// Config configures a Console.
type Config struct {
	// PublicURL is where people and agents reach the console, e.g. "http://localhost:8088".
	PublicURL string
	// Token is the push token agents must send. Empty disables the check.
	Token string
	// Notifier, if set, is told whenever a task starts waiting on a person.
	Notifier Notifier
	Logger   *slog.Logger
}

// Console holds the observed tasks.
type Console struct {
	cfg   Config
	pages *template.Template

	mu        sync.Mutex
	tasks     map[a2a.TaskID]*Task
	messages  []AgentMessage
	agents    map[string]*agent
	resolving map[string]bool
	nav       []navLink
	mux       *http.ServeMux
}

type agent struct {
	name   string
	card   *a2a.AgentCard
	client *a2aclient.Client
}

type navLink struct{ Label, Href string }

// New creates a console.
func New(cfg Config) *Console {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	c := &Console{
		cfg:       cfg,
		tasks:     make(map[a2a.TaskID]*Task),
		agents:    make(map[string]*agent),
		resolving: make(map[string]bool),
		mux:       http.NewServeMux(),
	}
	c.pages = template.Must(template.New("").Funcs(template.FuncMap{
		"short":   func(id a2a.TaskID) string { return shortID(string(id)) },
		"state":   stateClass,
		"label":   stateLabel,
		"pretty":  prettyJSON,
		"summary": summarize,
		"offset":  func(t, start time.Time) string { return "+" + formatDuration(t.Sub(start)) },
		"since":   func(t time.Time) string { return formatDuration(time.Since(t)) + " ago" },
		"partsOf": visibleParts,
		"hasDecision": func(fields []field) bool {
			return slices.ContainsFunc(fields, func(f field) bool { return f.Kind == "decision" })
		},
		"dict": func(kv ...any) map[string]any {
			m := make(map[string]any, len(kv)/2)
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
	}).ParseFS(templateFS, "templates/*.html"))

	c.mux.HandleFunc("POST /push", c.handlePush)
	c.mux.HandleFunc("GET /{$}", c.handleIndex)
	c.mux.HandleFunc("GET /tasks/{id}", c.handleTask)
	c.mux.HandleFunc("GET /inbox", c.handleInbox)
	c.mux.HandleFunc("POST /inbox/{id}", c.handleAnswer)
	c.mux.HandleFunc("GET /api/tasks", c.handleAPITasks)
	c.mux.HandleFunc("GET /api/tasks/{id}", c.handleAPITask)
	return c
}

// Handler returns the console's HTTP handler.
func (c *Console) Handler() http.Handler { return c.mux }

// Mount serves h under prefix (which must end in "/") and, when label is not empty,
// adds a navigation link to href.
func (c *Console) Mount(prefix string, h http.Handler, label, href string) {
	c.mux.Handle(prefix, http.StripPrefix(strings.TrimSuffix(prefix, "/"), h))
	if label != "" {
		c.mu.Lock()
		c.nav = append(c.nav, navLink{Label: label, Href: href})
		c.mu.Unlock()
	}
}

// PushURL is the webhook URL an agent at agentURL should push its task events to.
func (c *Console) PushURL(agentURL string) string {
	return c.cfg.PublicURL + "/push?agent=" + url.QueryEscape(strings.TrimSuffix(agentURL, "/"))
}

// Token is the push token agents must send.
func (c *Console) Token() string { return c.cfg.Token }

// Tasks returns copies of all observed tasks, newest first.
func (c *Console) Tasks() []*Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		out = append(out, t.clone())
	}
	slices.SortFunc(out, func(a, b *Task) int { return b.FirstSeen.Compare(a.FirstSeen) })
	return out
}

// Task returns a copy of one task.
func (c *Console) Task(id a2a.TaskID) (*Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tasks[id]
	if !ok {
		return nil, false
	}
	return t.clone(), true
}

// Messages returns the plain-message replies the console saw, newest first.
func (c *Console) Messages() []AgentMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := slices.Clone(c.messages)
	slices.Reverse(out)
	return out
}

// Record applies one event pushed by the agent at agentURL.
func (c *Console) Record(agentURL string, event a2a.Event, at time.Time) {
	agentURL = strings.TrimSuffix(agentURL, "/")
	name := c.agentName(agentURL)

	c.mu.Lock()
	var waiting *Task
	switch ev := event.(type) {
	case *a2a.Message:
		c.messages = append(c.messages, AgentMessage{At: at, AgentURL: agentURL, AgentName: name, Summary: summarize(ev)})
		if len(c.messages) > maxMessages {
			c.messages = c.messages[len(c.messages)-maxMessages:]
		}
	case *a2a.Task:
		t := c.upsert(ev.ID, agentURL, name, at)
		t.ContextID = ev.ContextID
		t.setState(ev.Status.State, at)
		t.Status = ev.Status.Message
		t.Artifacts = slices.Clone(ev.Artifacts)
		t.addEvent(at, "task", ev.Status.State, firstNonEmpty(summarize(ev.Status.Message), "task created"))
	case *a2a.TaskStatusUpdateEvent:
		t := c.upsert(ev.TaskID, agentURL, name, at)
		t.ContextID = ev.ContextID
		wasWaiting := t.Waiting()
		t.setState(ev.Status.State, at)
		t.Status = ev.Status.Message
		if child, ok := links.Get(ev.Metadata); ok {
			c.link(t, child, at)
		}
		t.addEvent(at, "status", ev.Status.State, summarize(ev.Status.Message))
		if t.Waiting() && !wasWaiting {
			waiting = t.clone()
		}
	case *a2a.TaskArtifactUpdateEvent:
		t := c.upsert(ev.TaskID, agentURL, name, at)
		t.ContextID = ev.ContextID
		t.mergeArtifact(ev)
		t.addEvent(at, "artifact", "", summarizeArtifact(ev.Artifact))
	}
	c.mu.Unlock()

	if waiting != nil && c.cfg.Notifier != nil {
		go c.notify(waiting)
	}
}

// upsert returns the task, creating it when it is first seen. Callers hold c.mu.
func (c *Console) upsert(id a2a.TaskID, agentURL, name string, at time.Time) *Task {
	t, ok := c.tasks[id]
	if !ok {
		t = &Task{ID: id, FirstSeen: at, TimeIn: make(map[a2a.TaskState]time.Duration)}
		c.tasks[id] = t
	}
	if t.AgentURL == "" || t.AgentName == t.AgentURL {
		t.AgentURL, t.AgentName = agentURL, name
	}
	if t.FirstSeen.IsZero() {
		t.FirstSeen = at
	}
	return t
}

// link records that parent delegated to child. Callers hold c.mu.
func (c *Console) link(parent *Task, child links.Child, at time.Time) {
	if !slices.Contains(parent.Children, child.TaskID) {
		parent.Children = append(parent.Children, child.TaskID)
	}
	t := c.upsert(child.TaskID, strings.TrimSuffix(child.AgentURL, "/"), c.cachedName(child.AgentURL), at)
	t.ParentID = parent.ID
}

func (c *Console) notify(t *Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := Notification{
		Title: t.AgentName + " is " + stateLabel(t.State),
		Text:  summarize(t.Status),
		Link:  c.cfg.PublicURL + "/inbox",
	}
	if err := c.cfg.Notifier.Notify(ctx, n); err != nil {
		c.cfg.Logger.Warn("notification failed", "task", t.ID, "error", err)
	}
}

// agentName returns the agent's card name if it is known. Otherwise it returns the URL
// and resolves the card in the background, so a push is never held up by a card fetch.
func (c *Console) agentName(agentURL string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.agents[agentURL]; ok {
		return a.name
	}
	if !c.resolving[agentURL] {
		c.resolving[agentURL] = true
		go func() {
			a, err := c.agent(context.Background(), agentURL)
			c.mu.Lock()
			defer c.mu.Unlock()
			delete(c.resolving, agentURL)
			if err != nil {
				c.cfg.Logger.Warn("agent card not resolved", "agent", agentURL, "error", err)
				return
			}
			for _, t := range c.tasks {
				if t.AgentURL == agentURL {
					t.AgentName = a.name
				}
			}
		}()
	}
	return agentURL
}

// cachedName is agentName for callers that already hold c.mu.
func (c *Console) cachedName(agentURL string) string {
	if a, ok := c.agents[strings.TrimSuffix(agentURL, "/")]; ok {
		return a.name
	}
	return agentURL
}

// agent resolves and caches the card and an A2A client for agentURL.
func (c *Console) agent(ctx context.Context, agentURL string) (*agent, error) {
	c.mu.Lock()
	a, ok := c.agents[agentURL]
	c.mu.Unlock()
	if ok {
		return a, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	card, err := agentcard.DefaultResolver.Resolve(ctx, agentURL)
	if err != nil {
		return nil, err
	}
	// The activator declares structuredinput, which the inbox uses to answer.
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(a2aext.NewActivator(structuredinput.URI)))
	if err != nil {
		return nil, err
	}
	a = &agent{name: card.Name, card: card, client: client}
	c.mu.Lock()
	if existing, ok := c.agents[agentURL]; ok {
		a = existing
	} else {
		c.agents[agentURL] = a
	}
	c.mu.Unlock()
	return a, nil
}

func (c *Console) handlePush(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Token != "" && r.Header.Get(TokenHeader) != c.cfg.Token {
		http.Error(w, "invalid push token", http.StatusUnauthorized)
		return
	}
	agentURL := r.URL.Query().Get("agent")
	if agentURL == "" {
		http.Error(w, "missing ?agent=", http.StatusBadRequest)
		return
	}
	// a2a-go's push sender posts a StreamResponse, the same envelope as a streaming event.
	var sr a2a.StreamResponse
	if err := json.NewDecoder(r.Body).Decode(&sr); err != nil || sr.Event == nil {
		http.Error(w, "body is not an A2A StreamResponse", http.StatusBadRequest)
		return
	}
	c.Record(agentURL, sr.Event, time.Now())
	w.WriteHeader(http.StatusNoContent)
}

func (c *Console) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tasks": c.Tasks(), "messages": c.Messages()})
}

func (c *Console) handleAPITask(w http.ResponseWriter, r *http.Request) {
	t, ok := c.Task(a2a.TaskID(r.PathValue("id")))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown task"})
		return
	}
	writeJSON(w, http.StatusOK, t)
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

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func stateLabel(s a2a.TaskState) string {
	if l, ok := stateLabels[s]; ok {
		return l
	}
	return strings.ToLower(strings.TrimPrefix(s.String(), "TASK_STATE_"))
}

func stateClass(s a2a.TaskState) string {
	switch {
	case s == a2a.TaskStateCompleted:
		return "ok"
	case s == a2a.TaskStateInputRequired || s == a2a.TaskStateAuthRequired:
		return "wait"
	case s.Terminal():
		return "bad"
	default:
		return "run"
	}
}
