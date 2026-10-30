package activity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Directions of an exchange.
const (
	// DirIn is a call to an address, at /a/<handle>.
	DirIn = "in"
	// DirOut is the relay's call to the agent behind an address.
	DirOut = "out"
	// DirPush is a push notification or webhook delivered through /t/<handle>.
	DirPush = "push"
)

// MaxBody caps each stored request and response body.
const MaxBody = 64 << 10

// Exchange is one HTTP request and its response, as the platform carried it.
type Exchange struct {
	ID           int64     `json:"id"`
	At           time.Time `json:"at"`
	Direction    string    `json:"direction"`
	TaskID       string    `json:"taskId,omitempty"`
	ParentTaskID string    `json:"parentTaskId,omitempty"`
	ContextID    string    `json:"contextId,omitempty"`
	Sender       string    `json:"sender,omitempty"`
	Recipient    string    `json:"recipient,omitempty"`
	Method       string    `json:"method"`
	URL          string    `json:"url"`
	Status       int       `json:"status"`
	DurationMS   int64     `json:"durationMs"`
	// Headers holds the "request" and "response" headers that are safe to keep.
	Headers   map[string]map[string]string `json:"headers,omitempty"`
	Request   string                       `json:"request"`
	Response  string                       `json:"response"`
	Truncated bool                         `json:"truncated,omitempty"`
	Error     string                       `json:"error,omitempty"`
}

// AddExchange stores x. ID is filled in.
func (l *Log) AddExchange(ctx context.Context, x *Exchange) error {
	var headers []byte
	if len(x.Headers) > 0 {
		headers, _ = json.Marshal(x.Headers)
	}
	err := l.db.Pool.QueryRow(ctx, `INSERT INTO exchanges (at, direction, task_id, parent_task_id, context_id, sender, recipient,
			method, url, status, duration_ms, headers, request, response, truncated, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		x.At, x.Direction, x.TaskID, x.ParentTaskID, x.ContextID, x.Sender, x.Recipient,
		x.Method, x.URL, x.Status, x.DurationMS, headers, x.Request, x.Response, x.Truncated, x.Error).Scan(&x.ID)
	if err != nil {
		return fmt.Errorf("activity: exchange: %w", err)
	}
	return nil
}

// Exchanges returns the exchanges of the given tasks (or with them as parent) and
// contexts, oldest first.
func (l *Log) Exchanges(ctx context.Context, taskIDs, contextIDs []string) ([]*Exchange, error) {
	rows, err := l.db.Pool.Query(ctx, `SELECT id, at, direction, task_id, parent_task_id, context_id, sender, recipient,
			method, url, status, duration_ms, headers, request, response, truncated, error
		FROM exchanges
		WHERE task_id = ANY($1) OR parent_task_id = ANY($1) OR (context_id <> '' AND context_id = ANY($2))
		ORDER BY at, id`, taskIDs, contextIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Exchange, error) {
		var x Exchange
		var headers []byte
		if err := row.Scan(&x.ID, &x.At, &x.Direction, &x.TaskID, &x.ParentTaskID, &x.ContextID, &x.Sender, &x.Recipient,
			&x.Method, &x.URL, &x.Status, &x.DurationMS, &headers, &x.Request, &x.Response, &x.Truncated, &x.Error); err != nil {
			return nil, err
		}
		if len(headers) > 0 {
			_ = json.Unmarshal(headers, &x.Headers)
		}
		return &x, nil
	})
}

// keptHeaders are the headers stored with an exchange. Anything else (Authorization,
// cookies, API keys) is dropped.
var keptHeaders = []string{"Content-Type", "A2A-Version", "A2A-Extensions", "Lagrange-Task", "Lagrange-Sender", "Lagrange-Agent-Unreachable", "User-Agent"}

func safeHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range keptHeaders {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// capture keeps the first MaxBody bytes written to it.
type capture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := MaxBody - c.buf.Len()
	switch {
	case room >= len(p):
		c.buf.Write(p)
	case room > 0:
		c.buf.Write(p[:room])
		c.truncated = true
	case len(p) > 0:
		c.truncated = true
	}
	return len(p), nil
}

func (c *capture) String() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.ToValidUTF8(c.buf.String(), "�"), c.truncated
}

type teeBody struct {
	io.ReadCloser
	w io.Writer
}

func (t teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		_, _ = t.w.Write(p[:n])
	}
	return n, err
}

// finish fills in the bodies and the task and context the exchange belongs to, and stores it.
func (l *Log) finish(ctx context.Context, x *Exchange, req, resp *capture) {
	x.Request, _ = req.String()
	var truncated bool
	x.Response, truncated = resp.String()
	_, reqTruncated := req.String()
	x.Truncated = truncated || reqTruncated
	if x.TaskID == "" || x.ContextID == "" {
		task, ctxID := idsIn(x.Response)
		if task == "" || ctxID == "" {
			t2, c2 := idsIn(x.Request)
			task, ctxID = firstNonEmpty(task, t2), firstNonEmpty(ctxID, c2)
		}
		x.TaskID, x.ContextID = firstNonEmpty(x.TaskID, task), firstNonEmpty(x.ContextID, ctxID)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = l.AddExchange(ctx, x)
}

// Transport records every exchange sent through rt, starting from base (direction,
// parties and, if known, the task). Streaming responses are recorded when they end.
func (l *Log) Transport(rt http.RoundTripper, base Exchange) http.RoundTripper {
	return &recordingTransport{log: l, rt: rt, base: base}
}

type recordingTransport struct {
	log  *Log
	rt   http.RoundTripper
	base Exchange
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	x := t.base
	x.At = time.Now()
	x.Method = r.Method
	x.URL = r.URL.RequestURI()
	x.Headers = map[string]map[string]string{"request": safeHeaders(r.Header)}
	req, resp := &capture{}, &capture{}
	out := r
	if r.Body != nil && r.Body != http.NoBody {
		out = r.Clone(r.Context())
		out.Body = teeBody{r.Body, req}
	}
	res, err := t.rt.RoundTrip(out)
	if err != nil {
		x.Error = err.Error()
		x.DurationMS = time.Since(x.At).Milliseconds()
		t.log.finish(r.Context(), &x, req, resp)
		return nil, err
	}
	x.Status = res.StatusCode
	x.Headers["response"] = safeHeaders(res.Header)
	body := &recordingBody{ReadCloser: res.Body, w: resp}
	body.done = func() {
		x.DurationMS = time.Since(x.At).Milliseconds()
		t.log.finish(r.Context(), &x, req, resp)
	}
	res.Body = body
	return res, nil
}

// recordingBody captures a response body and calls done once, at its end or close.
type recordingBody struct {
	io.ReadCloser
	w    io.Writer
	once sync.Once
	done func()
}

func (b *recordingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.w.Write(p[:n])
	}
	if err != nil {
		b.once.Do(b.done)
	}
	return n, err
}

func (b *recordingBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}

// Handler records every exchange served by next. base is called before next with the
// request, to name the parties.
func (l *Log) Handler(next http.Handler, base func(*http.Request) Exchange) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x := base(r)
		x.At = time.Now()
		x.Method = r.Method
		x.URL = r.URL.RequestURI()
		x.Headers = map[string]map[string]string{"request": safeHeaders(r.Header)}
		req, resp := &capture{}, &capture{}
		if r.Body != nil {
			r.Body = teeBody{r.Body, req}
		}
		rw := &recordingWriter{ResponseWriter: w, w: resp, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		x.Status = rw.status
		x.Headers["response"] = safeHeaders(w.Header())
		x.DurationMS = time.Since(x.At).Milliseconds()
		l.finish(r.Context(), &x, req, resp)
	})
}

type recordingWriter struct {
	http.ResponseWriter
	w      io.Writer
	status int
}

func (rw *recordingWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recordingWriter) Write(p []byte) (int, error) {
	_, _ = rw.w.Write(p)
	return rw.ResponseWriter.Write(p)
}

// Flush keeps Server-Sent Events streaming through the recorder.
func (rw *recordingWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *recordingWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// idsIn finds the first A2A task and context IDs in a JSON body, or in the data lines of
// a Server-Sent Events stream.
func idsIn(body string) (task, ctxID string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	docs := []string{body}
	if !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[") {
		docs = nil
		sc := bufio.NewScanner(strings.NewReader(body))
		sc.Buffer(make([]byte, 0, 64<<10), MaxBody)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				docs = append(docs, strings.TrimSpace(d))
			}
		}
	}
	for _, d := range docs {
		var v any
		if json.Unmarshal([]byte(d), &v) != nil {
			continue
		}
		t, c := walkIDs(v)
		task, ctxID = firstNonEmpty(task, t), firstNonEmpty(ctxID, c)
		if task != "" && ctxID != "" {
			break
		}
	}
	return task, ctxID
}

// walkIDs looks for "taskId", a "task" object's "id", or a task itself (an object with
// "id", "contextId" and "status"), and for "contextId", depth first.
func walkIDs(v any) (task, ctxID string) {
	switch v := v.(type) {
	case map[string]any:
		if s, ok := v["taskId"].(string); ok {
			task = s
		}
		if s, ok := v["contextId"].(string); ok {
			ctxID = s
		}
		if task == "" {
			if _, isTask := v["status"].(map[string]any); isTask && ctxID != "" {
				task, _ = v["id"].(string)
			}
		}
		for _, k := range []string{"result", "task", "message", "statusUpdate", "artifactUpdate", "params"} {
			if task != "" && ctxID != "" {
				break
			}
			if child, ok := v[k]; ok {
				t, c := walkIDs(child)
				task, ctxID = firstNonEmpty(task, t), firstNonEmpty(ctxID, c)
			}
		}
	case []any:
		for _, child := range v {
			t, c := walkIDs(child)
			task, ctxID = firstNonEmpty(task, t), firstNonEmpty(ctxID, c)
			if task != "" && ctxID != "" {
				break
			}
		}
	}
	return task, ctxID
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
