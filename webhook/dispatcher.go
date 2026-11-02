package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/pg"
)

// Dispatcher fans activity out to webhooks and sends the deliveries.
type Dispatcher struct {
	DB       *pg.DB
	Activity *activity.Log
	// Handles returns the addresses of a workspace: its events are those from or to them.
	Handles func(ctx context.Context, workspaceID int64) ([]string, error)
	// PublicURL makes links back to the web app.
	PublicURL string
	// Client sends the requests; nil means an SSRF-guarded client (see NewClient).
	Client *http.Client
	// RetryBase is the first retry delay (default 10s); each next one is three times
	// longer, up to six hours.
	RetryBase time.Duration
	// MaxAttempts is how often a delivery is tried before it fails (default 8).
	MaxAttempts int
	Logger      *slog.Logger
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Run fans out and sends every tick until ctx ends.
func (d *Dispatcher) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Fanout(ctx); err != nil && ctx.Err() == nil {
				d.log().Warn("webhook: fan-out", "error", err)
			}
			if err := d.SendDue(ctx); err != nil && ctx.Err() == nil {
				d.log().Warn("webhook: send", "error", err)
			}
		}
	}
}

type hook struct {
	id        int64
	workspace string
	events    map[string]bool
	in        map[string]bool
}

// Fanout turns new activity entries into deliveries for the webhooks that want them.
// Only one replica moves the cursor at a time; the others skip.
func (d *Dispatcher) Fanout(ctx context.Context) error {
	return pgx.BeginFunc(ctx, d.DB.Pool, func(tx pgx.Tx) error {
		var cursor int64
		err := tx.QueryRow(ctx, `SELECT seq FROM webhook_cursor WHERE id = 1 FOR UPDATE SKIP LOCKED`).Scan(&cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // another replica is on it
		}
		if err != nil {
			return err
		}
		entries, err := d.Activity.Since(ctx, cursor, 500)
		if err != nil || len(entries) == 0 {
			return err
		}
		hooks, err := d.hooks(ctx, tx)
		if err != nil {
			return err
		}
		for _, e := range entries {
			typ, data, err := d.eventOf(ctx, tx, e)
			if err != nil {
				return err
			}
			if typ == "" {
				continue
			}
			for _, h := range hooks {
				if !h.events[typ] || !(h.in[e.Sender] || h.in[e.Recipient]) {
					continue
				}
				ev := Event{ID: "evt_" + strconv.FormatInt(e.Seq, 10), Type: typ, CreatedAt: e.At, Workspace: h.workspace, Data: data}
				if err := insertDelivery(ctx, tx, h.id, &ev); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE webhook_cursor SET seq = $1 WHERE id = 1`, entries[len(entries)-1].Seq)
		return err
	})
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insertDelivery(ctx context.Context, q execer, hookID int64, ev *Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO webhook_deliveries (webhook_id, event_id, event_type, payload) VALUES ($1, $2, $3, $4)
		ON CONFLICT (webhook_id, event_id) DO NOTHING`, hookID, ev.ID, ev.Type, payload)
	return err
}

func (d *Dispatcher) hooks(ctx context.Context, tx pgx.Tx) ([]*hook, error) {
	rows, err := tx.Query(ctx, `SELECT h.id, h.workspace_id, w.slug, h.events FROM webhooks h JOIN workspaces w ON w.id = h.workspace_id WHERE h.active`)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, ws int64
		slug   string
		events []string
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.ws, &x.slug, &x.events)
	})
	if err != nil {
		return nil, err
	}
	handles := map[int64]map[string]bool{}
	var out []*hook
	for _, x := range all {
		in, ok := handles[x.ws]
		if !ok {
			hs, err := d.Handles(ctx, x.ws)
			if err != nil {
				return nil, err
			}
			in = make(map[string]bool, len(hs))
			for _, h := range hs {
				in[h] = true
			}
			handles[x.ws] = in
		}
		h := &hook{id: x.id, workspace: x.slug, events: map[string]bool{}, in: in}
		for _, e := range x.events {
			h.events[e] = true
		}
		out = append(out, h)
	}
	return out, nil
}

// A2A task states, as the log stores them.
const (
	stateCompleted     = "TASK_STATE_COMPLETED"
	stateFailed        = "TASK_STATE_FAILED"
	stateCanceled      = "TASK_STATE_CANCELED"
	stateRejected      = "TASK_STATE_REJECTED"
	stateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	stateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

func shortState(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "TASK_STATE_")), "_", "-")
}

func (d *Dispatcher) taskURL(id string) string {
	return strings.TrimSuffix(d.PublicURL, "/") + "/tasks/" + id
}

// eventOf maps a log entry to an event, or "" when it isn't one.
func (d *Dispatcher) eventOf(ctx context.Context, tx pgx.Tx, e *activity.Entry) (string, map[string]any, error) {
	switch e.Kind {
	case activity.KindTask:
		switch e.State {
		case stateInputRequired, stateAuthRequired:
			// Who it waits on and who asks, as the wait timers see it: a task addressed to
			// a person waits on them (awaiting), an agent's question waits on its sender.
			waitingOn, _ := e.Data["awaiting"].(string)
			if waitingOn == "" {
				waitingOn = e.Sender
			}
			asker, question := e.Recipient, e.Summary
			if waitingOn == e.Recipient {
				// The person addressed is asked: the question is the request itself.
				asker = e.Sender
				if err := tx.QueryRow(ctx, `SELECT summary FROM activity WHERE task_id = $1 AND kind = 'task' ORDER BY seq LIMIT 1`, e.TaskID).Scan(&question); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return "", nil, err
				}
			}
			return TaskWaiting, map[string]any{
				"taskId": e.TaskID, "contextId": e.ContextID, "parentTaskId": e.ParentTaskID, "from": e.Sender, "to": e.Recipient,
				"state": shortState(e.State), "waitingOn": waitingOn, "asker": asker, "question": question, "url": d.taskURL(e.TaskID),
			}, nil
		case stateCompleted, stateFailed, stateCanceled, stateRejected:
			if e.ParentTaskID != "" {
				return "", nil, nil // only whole runs: the root task
			}
			typ := RunCompleted
			if e.State != stateCompleted {
				typ = RunFailed
			}
			data := map[string]any{
				"runId": e.TaskID, "taskId": e.TaskID, "contextId": e.ContextID, "from": e.Sender, "to": e.Recipient,
				"state": shortState(e.State), "outcome": e.Summary, "url": d.taskURL(e.TaskID),
			}
			if by, ok := e.Data["answeredBy"].(string); ok {
				data["answeredBy"] = by
			}
			return typ, data, nil
		}
	case activity.KindInput:
		by, _ := e.Data["from"].(string)
		data := map[string]any{
			"taskId": e.TaskID, "contextId": e.ContextID, "from": e.Sender, "to": e.Recipient,
			"answeredBy": by, "answer": e.Summary, "url": d.taskURL(e.TaskID),
		}
		if c, ok := e.Data["content"]; ok {
			data["content"] = c
		}
		return TaskAnswered, data, nil
	case activity.KindPresence:
		sessions, _ := e.Data["sessions"].(float64)
		online := sessions > 0
		var was *bool
		err := tx.QueryRow(ctx, `SELECT online FROM webhook_presence WHERE handle = $1`, e.Recipient).Scan(&was)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", nil, err
		}
		if was != nil && *was == online {
			return "", nil, nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO webhook_presence (handle, online) VALUES ($1, $2)
			ON CONFLICT (handle) DO UPDATE SET online = $2`, e.Recipient, online); err != nil {
			return "", nil, err
		}
		if was == nil && !online {
			return "", nil, nil // never seen online: nothing changed
		}
		typ := AddressOnline
		if !online {
			typ = AddressOffline
		}
		capacity, _ := e.Data["capacity"].(float64)
		return typ, map[string]any{"address": e.Recipient, "sessions": int(sessions), "capacity": int(capacity)}, nil
	}
	return "", nil, nil
}

// Ping queues a test event for one webhook.
func (d *Dispatcher) Ping(ctx context.Context, hookID int64, workspace string) error {
	ev := Event{ID: fmt.Sprintf("evt_ping_%d", time.Now().UnixNano()), Type: Ping, CreatedAt: time.Now().UTC(), Workspace: workspace,
		Data: map[string]any{"message": "A test event from Lagrange."}}
	return insertDelivery(ctx, d.DB.Pool, hookID, &ev)
}

type due struct {
	id, hookID int64
	eventID    string
	typ        string
	payload    []byte
	attempts   int
	url        string
	secret     string
}

// leaseFor is how long a claimed delivery stays claimed while it is being sent.
const leaseFor = time.Minute

// SendDue sends the deliveries that are due, a batch at a time.
func (d *Dispatcher) SendDue(ctx context.Context) error {
	rows, err := d.DB.Pool.Query(ctx, `UPDATE webhook_deliveries d SET next_at = now() + $1::interval, attempts = d.attempts + 1
		FROM webhooks w
		WHERE w.id = d.webhook_id AND d.id IN (
			SELECT id FROM webhook_deliveries WHERE status = 'pending' AND next_at <= now() ORDER BY next_at, id LIMIT 50 FOR UPDATE SKIP LOCKED)
		RETURNING d.id, d.webhook_id, d.event_id, d.event_type, d.payload, d.attempts, w.url, w.secret`, leaseFor.String())
	if err != nil {
		return err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*due, error) {
		var x due
		return &x, r.Scan(&x.id, &x.hookID, &x.eventID, &x.typ, &x.payload, &x.attempts, &x.url, &x.secret)
	})
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, x := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d.send(ctx, x)
		}()
	}
	wg.Wait()
	return nil
}

func (d *Dispatcher) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return defaultClient
}

var defaultClient = NewClient(false)

func (d *Dispatcher) send(ctx context.Context, x *due) {
	status, sendErr := d.post(ctx, x)
	if sendErr == nil && status >= 200 && status < 300 {
		_, err := d.DB.Pool.Exec(ctx, `UPDATE webhook_deliveries SET status = 'delivered', delivered_at = now(), last_status = $2, last_error = '' WHERE id = $1`, x.id, status)
		if err != nil {
			d.log().Warn("webhook: recording a delivery", "error", err)
		}
		return
	}
	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
	} else {
		msg = fmt.Sprintf("the endpoint answered %d", status)
	}
	var code *int
	if status != 0 {
		code = &status
	}
	max := d.MaxAttempts
	if max <= 0 {
		max = 8
	}
	if x.attempts >= max {
		_, err := d.DB.Pool.Exec(ctx, `UPDATE webhook_deliveries SET status = 'failed', last_status = $2, last_error = $3 WHERE id = $1`, x.id, code, msg)
		if err != nil {
			d.log().Warn("webhook: recording a failure", "error", err)
		}
		return
	}
	_, err := d.DB.Pool.Exec(ctx, `UPDATE webhook_deliveries SET next_at = now() + $2::interval, last_status = $3, last_error = $4 WHERE id = $1`,
		x.id, d.backoff(x.attempts).String(), code, msg)
	if err != nil {
		d.log().Warn("webhook: scheduling a retry", "error", err)
	}
}

// backoff is the wait after attempt n (1-based): base, 3×base, 9×base, … up to six hours.
func (d *Dispatcher) backoff(n int) time.Duration {
	base := d.RetryBase
	if base <= 0 {
		base = 10 * time.Second
	}
	w := base
	for i := 1; i < n && w < 6*time.Hour; i++ {
		w *= 3
	}
	return min(w, 6*time.Hour)
}

func (d *Dispatcher) post(ctx context.Context, x *due) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.url, bytes.NewReader(x.payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Lagrange-Webhooks/1")
	req.Header.Set(EventHeader, x.typ)
	req.Header.Set(DeliveryHeader, strconv.FormatInt(x.id, 10))
	req.Header.Set(SignatureHeader, Sign(x.secret, time.Now(), x.payload))
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// NewClient returns the client deliveries use. Unless allowPrivate, it refuses to
// connect to loopback, private, link-local and unspecified addresses, checked on the
// resolved IP of every connection (so DNS tricks and redirects can't reach internal
// services), as the A2A spec asks of push notifications (13.2).
func NewClient(allowPrivate bool) *http.Client {
	c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	if allowPrivate {
		return c
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || blocked(ip) {
			return fmt.Errorf("webhook: refusing to connect to %s (a private address)", host)
		}
		return nil
	}
	c.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
		MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
	}
	return c
}

func blocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
