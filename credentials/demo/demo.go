// Package demo is a credentials.Broker that needs no accounts: its consent page is a
// local web page, and the only provider API it fakes is Google Calendar event
// creation. It exists so the AUTH_REQUIRED flow can be run and tested end to end.
package demo

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/three-agent-problem/lagrange/credentials"
)

// CalendarEventsPath is the Google Calendar API path the demo grant understands.
const CalendarEventsPath = "/calendar/v3/calendars/primary/events"

//go:embed templates/*.html
var templateFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// CalendarEvent is the subset of a Google Calendar event the demo stores.
type CalendarEvent struct {
	ID          string    `json:"id"`
	User        string    `json:"user"`
	Summary     string    `json:"summary"`
	Description string    `json:"description,omitempty"`
	Start       EventDate `json:"start"`
	End         EventDate `json:"end"`
	Created     time.Time `json:"created"`
}

// EventDate is an all-day date, as in Google Calendar's {"date": "2026-11-20"}.
type EventDate struct {
	Date string `json:"date"`
}

// Broker is the demo credentials broker.
type Broker struct {
	baseURL string

	mu       sync.Mutex
	sessions map[string]*session
	grants   map[string]*grant
	events   []CalendarEvent
}

type session struct {
	challenge *credentials.Challenge
	done      chan struct{}
	grant     *grant
	denied    bool
}

var _ credentials.Broker = (*Broker)(nil)

// New creates a demo broker whose pages are served under baseURL, for example
// "http://localhost:8088/demo-auth". Mount Handler at that path.
func New(baseURL string) *Broker {
	return &Broker{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		sessions: make(map[string]*session),
		grants:   make(map[string]*grant),
	}
}

// Lookup implements credentials.Broker.
func (b *Broker) Lookup(ctx context.Context, user, integration string) (credentials.Grant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if g, ok := b.grants[key(user, integration)]; ok {
		return g, nil
	}
	return nil, credentials.ErrNoGrant
}

// Begin implements credentials.Broker.
func (b *Broker) Begin(ctx context.Context, user, integration string) (*credentials.Challenge, error) {
	id := newID()
	c := &credentials.Challenge{
		ID:          id,
		User:        user,
		Integration: integration,
		URL:         b.baseURL + "/connect/" + id,
		ExpiresAt:   time.Now().Add(30 * time.Minute),
	}
	b.mu.Lock()
	b.sessions[id] = &session{challenge: c, done: make(chan struct{})}
	b.mu.Unlock()
	return c, nil
}

// Await implements credentials.Broker.
func (b *Broker) Await(ctx context.Context, c *credentials.Challenge) (credentials.Grant, error) {
	b.mu.Lock()
	s, ok := b.sessions[c.ID]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("demo: unknown challenge %q", c.ID)
	}
	timer := time.NewTimer(time.Until(c.ExpiresAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, credentials.ErrExpired
	case <-s.done:
	}
	if s.denied {
		return nil, credentials.ErrDenied
	}
	return s.grant, nil
}

// Decide completes a challenge as if the user clicked Allow or Deny.
func (b *Broker) Decide(id string, allow bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[id]
	if !ok {
		return fmt.Errorf("demo: unknown challenge %q", id)
	}
	select {
	case <-s.done:
		return fmt.Errorf("demo: challenge %q was already decided", id)
	default:
	}
	if allow {
		g := &grant{broker: b, id: "demo-" + id, user: s.challenge.User, integration: s.challenge.Integration}
		s.grant = g
		b.grants[key(g.user, g.integration)] = g
	} else {
		s.denied = true
	}
	close(s.done)
	return nil
}

// Events returns the calendar events created through demo grants.
func (b *Broker) Events() []CalendarEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]CalendarEvent(nil), b.events...)
}

// Handler serves the consent page (/connect/{id}) and the fake calendar (/calendar).
// Mount it with http.StripPrefix at the path of baseURL.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /connect/{id}", b.handleConsentPage)
	mux.HandleFunc("POST /connect/{id}", b.handleDecision)
	mux.HandleFunc("GET /calendar", b.handleCalendar)
	return mux
}

func (b *Broker) handleConsentPage(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	s, ok := b.sessions[r.PathValue("id")]
	b.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	state := "pending"
	select {
	case <-s.done:
		state = "allowed"
		if s.denied {
			state = "denied"
		}
	default:
	}
	render(w, "consent.html", map[string]any{"Challenge": s.challenge, "State": state})
}

func (b *Broker) handleDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := b.Decide(id, r.FormValue("decision") == "allow"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, b.baseURL+"/connect/"+id, http.StatusSeeOther)
}

func (b *Broker) handleCalendar(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.Events())
		return
	}
	render(w, "calendar.html", map[string]any{"Events": b.Events()})
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type grant struct {
	broker      *Broker
	id          string
	user        string
	integration string
}

// ConnectionID implements credentials.Grant.
func (g *grant) ConnectionID() string { return g.id }

// Call implements credentials.Grant. Only event creation in the primary calendar is faked.
func (g *grant) Call(ctx context.Context, method, path string, body, out any) error {
	if method != http.MethodPost || path != CalendarEventsPath {
		return fmt.Errorf("demo: only POST %s is faked, got %s %s", CalendarEventsPath, method, path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var event CalendarEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("demo: invalid event: %w", err)
	}
	event.ID = newID()
	event.User = g.user
	event.Created = time.Now()

	g.broker.mu.Lock()
	g.broker.events = append(g.broker.events, event)
	g.broker.mu.Unlock()

	if out == nil {
		return nil
	}
	resp, err := json.Marshal(map[string]any{
		"id":       event.ID,
		"status":   "confirmed",
		"summary":  event.Summary,
		"htmlLink": g.broker.baseURL + "/calendar#" + event.ID,
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, out)
}

func key(user, integration string) string { return user + "\x00" + integration }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
