// Package mailbox decides which connected copy of an agent gets the next task.
//
// Each agent address can have several tunnel sessions (copies of the agent on a
// laptop, a server, a GPU box), and each session says how many tasks it runs at once.
// Together they form a worker pool. Acquire hands out a slot on the least busy session
// and blocks while every session is full or none is connected, which is how a task
// waits for an offline agent.
package mailbox

import (
	"context"
	"net/http"
	"sort"
	"sync"
)

// Session is one connected copy of an agent.
type Session interface {
	// ID identifies the session.
	ID() string
	// Capacity is how many tasks the session runs at once.
	Capacity() int
	// Transport sends HTTP requests to the agent through the session.
	Transport() http.RoundTripper
	// Done is closed when the session ends.
	Done() <-chan struct{}
}

// Stats describes an address's mailbox.
type Stats struct {
	Sessions int `json:"sessions"`
	Capacity int `json:"capacity"`
	InFlight int `json:"inFlight"`
	Waiting  int `json:"waiting"`
}

type entry struct {
	Session
	inFlight int
}

type box struct {
	sessions []*entry
	waiting  int
	// changed is closed and replaced whenever a slot may have opened up.
	changed chan struct{}
}

// Mailbox tracks the sessions of every address.
type Mailbox struct {
	mu    sync.Mutex
	boxes map[string]*box
	// OnChange, if set, is called (without the lock held) after an address's stats change.
	OnChange func(handle string, stats Stats)
}

// New returns an empty mailbox.
func New() *Mailbox { return &Mailbox{boxes: map[string]*box{}} }

func (m *Mailbox) box(handle string) *box {
	b := m.boxes[handle]
	if b == nil {
		b = &box{changed: make(chan struct{})}
		m.boxes[handle] = b
	}
	return b
}

func (b *box) signal() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *box) stats() Stats {
	s := Stats{Sessions: len(b.sessions), Waiting: b.waiting}
	for _, e := range b.sessions {
		s.Capacity += e.Capacity()
		s.InFlight += e.inFlight
	}
	return s
}

// Add registers a session for handle until the session is done.
func (m *Mailbox) Add(handle string, s Session) {
	m.mu.Lock()
	b := m.box(handle)
	b.sessions = append(b.sessions, &entry{Session: s})
	b.signal()
	stats := b.stats()
	m.mu.Unlock()
	m.changed(handle, stats)

	go func() {
		<-s.Done()
		m.mu.Lock()
		b := m.box(handle)
		for i, e := range b.sessions {
			if e.Session == s {
				b.sessions = append(b.sessions[:i], b.sessions[i+1:]...)
				break
			}
		}
		b.signal()
		stats := b.stats()
		m.mu.Unlock()
		m.changed(handle, stats)
	}()
}

// Acquire waits for a free slot on one of handle's sessions. onWait, if not nil, is
// called once when Acquire has to wait. release must be called when the task is done
// with the session.
func (m *Mailbox) Acquire(ctx context.Context, handle string, onWait func()) (s Session, release func(), err error) {
	waited := false
	defer func() {
		if waited {
			m.mu.Lock()
			b := m.box(handle)
			b.waiting--
			stats := b.stats()
			m.mu.Unlock()
			m.changed(handle, stats)
		}
	}()
	for {
		m.mu.Lock()
		b := m.box(handle)
		if e := b.pick(); e != nil {
			e.inFlight++
			stats := b.stats()
			m.mu.Unlock()
			m.changed(handle, stats)
			var once sync.Once
			return e.Session, func() { once.Do(func() { m.release(handle, e) }) }, nil
		}
		if !waited {
			waited = true
			b.waiting++
		}
		changed := b.changed
		stats := b.stats()
		m.mu.Unlock()
		if waited && onWait != nil {
			m.changed(handle, stats)
			onWait()
			onWait = nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-changed:
		}
	}
}

// pick returns the session with the most free capacity, or nil.
func (b *box) pick() *entry {
	free := make([]*entry, 0, len(b.sessions))
	for _, e := range b.sessions {
		if e.inFlight < e.Capacity() {
			select {
			case <-e.Done():
				continue
			default:
			}
			free = append(free, e)
		}
	}
	if len(free) == 0 {
		return nil
	}
	sort.SliceStable(free, func(i, j int) bool {
		return free[i].Capacity()-free[i].inFlight > free[j].Capacity()-free[j].inFlight
	})
	return free[0]
}

func (m *Mailbox) release(handle string, e *entry) {
	m.mu.Lock()
	b := m.box(handle)
	e.inFlight--
	b.signal()
	stats := b.stats()
	m.mu.Unlock()
	m.changed(handle, stats)
}

// Any returns some live session of handle without taking a slot, for short calls such as
// fetching the card or cancelling a task.
func (m *Mailbox) Any(handle string) Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.box(handle).sessions {
		select {
		case <-e.Done():
		default:
			return e.Session
		}
	}
	return nil
}

// Stats returns handle's current stats.
func (m *Mailbox) Stats(handle string) Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.box(handle).stats()
}

// All returns the stats of every address that has or had sessions.
func (m *Mailbox) All() map[string]Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Stats, len(m.boxes))
	for h, b := range m.boxes {
		out[h] = b.stats()
	}
	return out
}

func (m *Mailbox) changed(handle string, s Stats) {
	if m.OnChange != nil {
		m.OnChange(handle, s)
	}
}
