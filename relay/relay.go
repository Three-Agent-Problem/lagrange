// Package relay is Lagrange's A2A executor. Every address on the platform is an A2A
// tenant served by one a2a-go handler, and the relay is what runs a task for it:
//
//   - For an agent, it forwards the task to the agent (through the agent's tunnel, or
//     directly to a public endpoint), and re-stamps the agent's events with the
//     platform's task and context IDs. The platform owns the task, so the task survives
//     the agent going offline, waits in the mailbox until the agent is back, and is
//     visible to the inbox and the graph.
//   - For a person, it parks the task as INPUT_REQUIRED in their inbox until they answer.
//
// Every event is written to the activity log as it happens.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/delegate"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/links"
	"github.com/three-agent-problem/lagrange/mailbox"
	"github.com/three-agent-problem/lagrange/tunnel"
)

const (
	// HeaderTask is set on every request the relay sends to an agent: the platform task
	// the agent is working on. An agent that propagates it on its own outgoing calls
	// (a2aext.NewServerPropagator and NewClientPropagator) lets the platform nest those
	// calls under the task.
	HeaderTask = delegate.HeaderTask
	// HeaderSender is set on requests to an agent: the address that sent the task.
	HeaderSender = "Lagrange-Sender"

	// AwaitingKey in a status message's metadata names the address the task waits for.
	AwaitingKey = "https://threeagents.dev/ext/awaiting/v1"
	// AnsweredByKey in a message's metadata names the person who answered from the inbox.
	AnsweredByKey = "https://threeagents.dev/ext/answered-by/v1"
	// FailureKey in a FAILED status message's metadata says why Lagrange could not deliver
	// the task: {"kind": Failure*, "address": handle, "detail": text}. Failures an agent
	// reports itself don't carry it.
	FailureKey = "https://threeagents.dev/ext/delivery-failure/v1"

	// AttrAnsweredBy, in the a2asrv.User attributes of an inbox answer, names the person
	// answering on the task owner's behalf. Only the platform's own API sets it.
	AttrAnsweredBy = "lagrange.answeredBy"
	// AttrDecision is "reject" when the person declined.
	AttrDecision = "lagrange.decision"
)

// Relay implements a2asrv.AgentExecutor for every address.
type Relay struct {
	Directory *directory.Directory
	Links     *Links
	Activity  *activity.Log
	Mailbox   *mailbox.Mailbox
	// Direct is the HTTP client for agents with a public endpoint.
	Direct *http.Client
	// OnWaiting, if set, is called when a task stops on INPUT_REQUIRED or AUTH_REQUIRED.
	OnWaiting func(ctx context.Context, w Waiting)
	Logger    *slog.Logger
}

// Waiting is a task that just stopped to wait on someone.
type Waiting struct {
	// Entry is the task's activity entry: its state, sender, recipient and awaiting.
	Entry *activity.Entry
	// Question is what is asked: the agent's status message, or, for a task addressed
	// to a person, the sender's message.
	Question *a2a.Message
}

var _ a2asrv.AgentExecutor = (*Relay)(nil)

func (r *Relay) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Execute implements a2asrv.AgentExecutor.
func (r *Relay) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		addr, err := r.Directory.Address(ctx, ec.Tenant)
		if err != nil {
			yield(nil, fmt.Errorf("no address %q on this server: %w", ec.Tenant, a2a.ErrInvalidParams))
			return
		}
		rec := r.recorder(ctx, ec, addr)
		if ec.StoredTask != nil && ec.Message != nil {
			rec.input(ec.Message, firstNonEmpty(userAttr(ec.User, AttrAnsweredBy), userName(ec.User)))
		}
		emit := func(ev a2a.Event) bool {
			rec.record(ev)
			return yield(ev, nil)
		}
		if addr.Kind == directory.KindHuman {
			r.human(ec, addr, emit)
			return
		}
		if err := r.deliver(ctx, ec, addr, rec, emit); err != nil {
			if ctx.Err() != nil {
				// This worker is stopping: the work queue hands the task to another one.
				yield(nil, ctx.Err())
				return
			}
			r.log().Warn("relay: forwarding failed", "address", addr.Handle, "task", ec.TaskID, "error", err)
			// An agent that isn't answering behind its tunnel fails the task with the reason
			// instead of retrying in the background while the caller waits.
			kind, detail := classifyFailure(err)
			text := "Lagrange could not reach " + addr.Name() + ": " + detail
			if kind == FailureAgentDown {
				text = addr.Name() + "'s tunnel is connected, but " + detail
			}
			// A connection problem still makes a (failed) task, so the run records why. An
			// agent's error stays an error: it may be refusing a bad answer to its question.
			if !rec.started && kind != FailureAgentError {
				emit(a2a.NewSubmittedTask(ec, ec.Message))
			}
			if !rec.started {
				yield(nil, err)
				return
			}
			msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
			msg.Metadata = map[string]any{FailureKey: map[string]any{"kind": kind, "address": addr.Handle, "detail": detail}}
			emit(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, msg))
		}
	}
}

// Cancel implements a2asrv.AgentExecutor.
func (r *Relay) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		addr, err := r.Directory.Address(ctx, ec.Tenant)
		if err == nil && addr.Kind == directory.KindAgent {
			if link, _ := r.Links.Get(ctx, ec.TaskID); link != nil && link.RemoteTaskID != "" {
				if client, err := r.client(ctx, addr, ec, r.anyTransport(addr), link.ParentTaskID); err == nil {
					if _, err := client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: link.RemoteTaskID}); err != nil {
						r.log().Info("relay: the agent did not cancel its task", "address", addr.Handle, "error", err)
					}
				}
			}
		}
		ev := a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil)
		if addr != nil {
			r.recorder(ctx, ec, addr).record(ev)
		}
		yield(ev, nil)
	}
}

// human parks a task in a person's inbox. When they answer (the platform API resumes
// the task with AttrAnsweredBy set to their handle) the answer completes the task.
func (r *Relay) human(ec *a2asrv.ExecutorContext, addr *directory.Address, emit func(a2a.Event) bool) {
	waiting := func() *a2a.TaskStatusUpdateEvent {
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("Waiting for "+addr.Name()+" to answer."))
		msg.Metadata = map[string]any{AwaitingKey: addr.Handle}
		ev := a2a.NewStatusUpdateEvent(ec, a2a.TaskStateInputRequired, msg)
		ev.Metadata = map[string]any{AwaitingKey: addr.Handle}
		return ev
	}
	if ec.StoredTask == nil {
		if emit(a2a.NewSubmittedTask(ec, ec.Message)) {
			emit(waiting())
		}
		return
	}
	answeredBy, decision := userAttr(ec.User, AttrAnsweredBy), userAttr(ec.User, AttrDecision)
	if answeredBy != addr.Handle {
		// The sender added something while the person hasn't answered yet.
		emit(waiting())
		return
	}
	reply := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, ec.Message.Parts...)
	reply.Metadata = map[string]any{AnsweredByKey: answeredBy}
	state := a2a.TaskStateCompleted
	if decision == "reject" {
		state = a2a.TaskStateRejected
	}
	emit(a2a.NewStatusUpdateEvent(ec, state, reply))
}

func userAttr(u *a2asrv.User, key string) string {
	if u == nil || u.Attributes == nil {
		return ""
	}
	s, _ := u.Attributes[key].(string)
	return s
}

func interrupted(s a2a.TaskState) bool {
	return s == a2a.TaskStateInputRequired || s == a2a.TaskStateAuthRequired
}

// maxRedeliveries bounds how often one execution waits for a dropped agent to return.
const maxRedeliveries = 5

// errSessionLost wraps the error of a call whose tunnel session closed under it.
type errSessionLost struct{ err error }

func (e errSessionLost) Error() string { return "the agent disconnected: " + e.err.Error() }
func (e errSessionLost) Unwrap() error { return e.err }

// deliver runs the task on the agent. If the agent's tunnel drops mid-task, the task
// goes back to the mailbox and is redelivered when the agent reconnects: the agent's
// task is followed if it still has it, and the message is sent again if it doesn't.
func (r *Relay) deliver(ctx context.Context, ec *a2asrv.ExecutorContext, addr *directory.Address, rec *recorder, emit func(a2a.Event) bool) error {
	link, err := r.Links.Get(ctx, ec.TaskID)
	if err != nil {
		return err
	}
	if link == nil {
		link = &Link{TaskID: ec.TaskID}
	}
	rec.started = ec.StoredTask != nil
	tr := &translator{r: r, ctx: ctx, ec: ec, addr: addr, link: link, rec: rec, emit: emit}
	if ec.StoredTask != nil {
		tr.state = ec.StoredTask.Status.State
		for _, a := range ec.StoredTask.Artifacts {
			tr.seen(a.ID)
		}
	}
	for attempt := 0; ; attempt++ {
		err := r.forward(ctx, ec, addr, tr)
		var lost errSessionLost
		if err == nil || !errors.As(err, &lost) || attempt >= maxRedeliveries || ctx.Err() != nil || tr.done {
			return err
		}
		r.log().Info("relay: the agent disconnected mid-task, redelivering", "address", addr.Handle, "task", ec.TaskID)
		rec.note(activity.KindQueued, "The connection to "+addr.Name()+" dropped: the task waits in the mailbox to be redelivered.")
	}
}

// forward makes one attempt at running the task on the agent.
func (r *Relay) forward(ctx context.Context, ec *a2asrv.ExecutorContext, addr *directory.Address, tr *translator) (err error) {
	transport, lost, release, err := r.transport(ctx, ec, addr, tr.rec, tr.emit)
	if err != nil {
		return err
	}
	defer release()
	defer func() {
		if err != nil && lost() {
			err = errSessionLost{err}
		}
	}()

	client, err := r.client(ctx, addr, ec, transport, tr.rec.parent)
	if err != nil {
		return err
	}
	card := client.Card()
	streaming := card != nil && card.Capabilities.Streaming
	link := tr.link

	// The task is running on the agent already: it was redelivered by the work queue (its
	// worker died) or by deliver (the tunnel dropped). Follow the agent's task instead of
	// sending the message again, unless the agent no longer knows it.
	if tr.rec.started && !interrupted(tr.state) && link.RemoteTaskID != "" {
		var err error
		if streaming {
			err = tr.consume(client.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: link.RemoteTaskID}))
		} else {
			err = tr.poll(client, link.RemoteTaskID)
		}
		if !errors.Is(err, a2a.ErrTaskNotFound) {
			return err
		}
		r.log().Info("relay: the agent lost the task, sending it again", "address", addr.Handle, "task", ec.TaskID)
		tr.forgetRemote()
	}

	msg := &a2a.Message{
		ID:         ec.Message.ID,
		Role:       a2a.MessageRoleUser,
		Parts:      ec.Message.Parts,
		Metadata:   ec.Message.Metadata,
		Extensions: ec.Message.Extensions,
		TaskID:     link.RemoteTaskID,
		ContextID:  link.RemoteContextID,
	}
	req := &a2a.SendMessageRequest{Message: msg}
	if streaming {
		return tr.consume(client.SendStreamingMessage(ctx, req))
	}
	res, err := client.SendMessage(ctx, req)
	if err != nil {
		return err
	}
	switch res := res.(type) {
	case *a2a.Message:
		tr.handle(res)
		return nil
	case *a2a.Task:
		if !tr.handle(res) {
			return nil
		}
		if res.Status.State.Terminal() || res.Status.State == a2a.TaskStateInputRequired {
			return nil
		}
		// A blocking call returns early on AUTH_REQUIRED (and agents may return early
		// otherwise); follow the task until it settles.
		return tr.poll(client, res.ID)
	}
	return fmt.Errorf("unexpected result %T", res)
}

// transport returns how to reach the agent: its public endpoint, or a slot on one of
// its tunnel sessions, waiting for one if none is free or connected.
// lost reports whether the session closed since.
func (r *Relay) transport(ctx context.Context, ec *a2asrv.ExecutorContext, addr *directory.Address, rec *recorder, emit func(a2a.Event) bool) (rt http.RoundTripper, lost func() bool, release func(), err error) {
	if addr.Endpoint != "" {
		client := r.Direct
		if client == nil {
			client = http.DefaultClient
		}
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		return transport, func() bool { return false }, func() {}, nil
	}
	waited := false
	sess, release, err := r.Mailbox.Acquire(ctx, addr.Handle, func() {
		waited = true
		// Make the task exist (and visible to the caller) while it waits.
		if !rec.started {
			emit(a2a.NewSubmittedTask(ec, ec.Message))
		}
		rec.note(activity.KindQueued, "Waiting in "+addr.Name()+"'s mailbox: the agent is offline or busy.")
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if waited {
		rec.note(activity.KindDelivered, "Delivered to "+addr.Name()+".")
	}
	lost = func() bool {
		// A stream error can arrive just before the session notices it closed.
		select {
		case <-sess.Done():
			return true
		case <-time.After(time.Second):
			return false
		}
	}
	return sess.Transport(), lost, release, nil
}

// anyTransport is for short calls (cancel) that shouldn't wait for a slot.
func (r *Relay) anyTransport(addr *directory.Address) http.RoundTripper {
	if addr.Endpoint != "" {
		if r.Direct != nil && r.Direct.Transport != nil {
			return r.Direct.Transport
		}
		return http.DefaultTransport
	}
	if s := r.Mailbox.Any(addr.Handle); s != nil {
		return s.Transport()
	}
	return unreachable{}
}

type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("the agent is offline")
}

// client builds an A2A client for the agent over transport, from the agent's card.
// Every exchange with the agent is recorded under the platform task.
func (r *Relay) client(ctx context.Context, addr *directory.Address, ec *a2asrv.ExecutorContext, transport http.RoundTripper, parent a2a.TaskID) (*a2aclient.Client, error) {
	sender := ""
	if ec.User != nil {
		sender = ec.User.Name
	}
	httpClient := &http.Client{Transport: r.Activity.Transport(transport, activity.Exchange{
		Direction: activity.DirOut, TaskID: string(ec.TaskID), ParentTaskID: string(parent), ContextID: ec.ContextID,
		Sender: sender, Recipient: addr.Handle,
	})}
	card := addr.Card
	if card == nil || len(card.SupportedInterfaces) == 0 {
		fetched, err := FetchCard(ctx, httpClient, addr.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("fetching %s's agent card: %w", addr.Handle, err)
		}
		card = fetched
		if err := r.Directory.SetCard(ctx, addr.Handle, card); err != nil {
			r.log().Warn("relay: storing card failed", "address", addr.Handle, "error", err)
		}
	}
	return a2aclient.NewFromCard(ctx, card,
		a2aclient.WithJSONRPCTransport(httpClient),
		a2aclient.WithRESTTransport(httpClient),
		a2aclient.WithCallInterceptors(headers{HeaderTask: string(ec.TaskID), HeaderSender: sender}),
	)
}

// headers adds fixed service params (HTTP headers) to every call.
type headers map[string]string

func (h headers) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	for k, v := range h {
		if v != "" {
			req.ServiceParams[k] = []string{v}
		}
	}
	return ctx, nil, nil
}

func (headers) After(context.Context, *a2aclient.Response) error { return nil }

// FetchCard gets an agent's card from base (empty means "whatever is behind this
// client's transport", used for tunnels) at the well-known path.
func FetchCard(ctx context.Context, client *http.Client, base string) (*a2a.AgentCard, error) {
	if base == "" {
		base = "http://agent"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+a2asrv.WellKnownAgentCardPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", req.URL.Path, resp.Status)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&card); err != nil {
		return nil, fmt.Errorf("decoding the card: %w", err)
	}
	return &card, nil
}

// translator turns the agent's events into the platform task's events.
type translator struct {
	r    *Relay
	ctx  context.Context
	ec   *a2asrv.ExecutorContext
	addr *directory.Address
	link *Link
	rec  *recorder
	emit func(a2a.Event) bool

	state a2a.TaskState
	// done is set once the platform task reached a terminal or interrupted state.
	done      bool
	artifacts map[a2a.ArtifactID]bool
}

func (t *translator) seen(id a2a.ArtifactID) {
	if t.artifacts == nil {
		t.artifacts = map[a2a.ArtifactID]bool{}
	}
	t.artifacts[id] = true
}

func (t *translator) consume(events iter.Seq2[a2a.Event, error]) error {
	for ev, err := range events {
		if err != nil {
			return err
		}
		if !t.handle(ev) {
			return nil
		}
	}
	return nil
}

// poll follows a task of an agent that doesn't stream.
func (t *translator) poll(client *a2aclient.Client, remote a2a.TaskID) error {
	for {
		task, err := client.GetTask(t.ctx, &a2a.GetTaskRequest{ID: remote})
		if err != nil {
			return err
		}
		if !t.handle(task) {
			return nil
		}
		if task.Status.State.Terminal() || task.Status.State == a2a.TaskStateInputRequired {
			return nil
		}
		select {
		case <-t.ctx.Done():
			return t.ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// start makes sure the platform task exists before anything else is emitted for it.
func (t *translator) start() bool {
	if t.rec.started {
		return true
	}
	return t.emit(a2a.NewSubmittedTask(t.ec, t.ec.Message))
}

// handle emits the platform's version of one agent event. It returns false when
// nothing more should be emitted.
func (t *translator) handle(ev a2a.Event) bool {
	switch ev := ev.(type) {
	case *a2a.Message:
		t.done = true
		if !t.rec.started {
			// The agent answered without a task, and the platform hasn't created one either.
			reply := t.restamp(ev)
			reply.TaskID = ""
			t.emit(reply)
			return false
		}
		// The platform task exists (it waited in the mailbox, or is being resumed): the
		// agent's message is the task's answer.
		t.emit(a2a.NewStatusUpdateEvent(t.ec, a2a.TaskStateCompleted, t.restamp(ev)))
		return false

	case *a2a.Task:
		// The platform task (and its link) must exist before the agent's IDs go on the link.
		if !t.start() {
			return false
		}
		t.remote(ev.ID, ev.ContextID)
		for _, a := range ev.Artifacts {
			if t.artifacts[a.ID] {
				continue
			}
			t.seen(a.ID)
			if !t.emit(&a2a.TaskArtifactUpdateEvent{TaskID: t.ec.TaskID, ContextID: t.ec.ContextID, Artifact: a, LastChunk: true}) {
				return false
			}
		}
		if ev.Status.State != a2a.TaskStateSubmitted && ev.Status.State != t.state {
			return t.status(ev.Status, nil)
		}
		return true

	case *a2a.TaskStatusUpdateEvent:
		if !t.start() {
			return false
		}
		t.remote(ev.TaskID, ev.ContextID)
		return t.status(ev.Status, ev.Metadata)

	case *a2a.TaskArtifactUpdateEvent:
		if !t.start() {
			return false
		}
		t.remote(ev.TaskID, ev.ContextID)
		t.seen(ev.Artifact.ID)
		return t.emit(&a2a.TaskArtifactUpdateEvent{
			TaskID: t.ec.TaskID, ContextID: t.ec.ContextID,
			Artifact: ev.Artifact, Append: ev.Append, LastChunk: ev.LastChunk, Metadata: ev.Metadata,
		})
	}
	return true
}

// forgetRemote drops the agent's IDs, for an agent that restarted without the task.
func (t *translator) forgetRemote() {
	t.link.RemoteTaskID, t.link.RemoteContextID = "", ""
	if err := t.r.Links.SetRemote(t.ctx, t.ec.TaskID, "", ""); err != nil {
		t.r.log().Warn("relay: clearing the agent's task ID failed", "task", t.ec.TaskID, "error", err)
	}
}

// remote records the agent's task and context IDs on the link.
func (t *translator) remote(id a2a.TaskID, contextID string) {
	if id == "" || t.link.RemoteTaskID == id {
		return
	}
	t.link.RemoteTaskID, t.link.RemoteContextID = id, contextID
	if err := t.r.Links.SetRemote(t.ctx, t.ec.TaskID, id, contextID); err != nil {
		t.r.log().Warn("relay: storing the agent's task ID failed", "task", t.ec.TaskID, "error", err)
	}
}

func (t *translator) status(st a2a.TaskStatus, metadata map[string]any) bool {
	t.state = st.State
	t.done = st.State.Terminal() || interrupted(st.State)
	var msg *a2a.Message
	if st.Message != nil {
		msg = t.restamp(st.Message)
	}
	out := a2a.NewStatusUpdateEvent(t.ec, st.State, msg)
	out.Metadata = metadata
	if child, ok := links.Get(metadata); ok && child.TaskID != "" {
		// The agent delegated through the platform and said so with the links convention.
		if err := t.r.Links.SetParent(t.ctx, a2a.TaskID(child.TaskID), t.ec.TaskID); err != nil {
			t.r.log().Warn("relay: linking a child task failed", "task", t.ec.TaskID, "error", err)
		}
	}
	return t.emit(out)
}

// restamp copies an agent message into the platform task.
func (t *translator) restamp(m *a2a.Message) *a2a.Message {
	out := *m
	out.TaskID = t.ec.TaskID
	out.ContextID = t.ec.ContextID
	out.ReferenceTasks = nil
	return &out
}

func userName(u *a2asrv.User) string {
	if u == nil {
		return ""
	}
	return u.Name
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Delivery failure kinds, in FailureKey metadata.
const (
	// FailureAgentDown: the agent's tunnel is connected, but nothing answers behind it.
	FailureAgentDown = "agent-down"
	// FailureUnreachable: Lagrange could not connect to the agent's endpoint.
	FailureUnreachable = "unreachable"
	// FailureTimeout: the agent did not answer in time.
	FailureTimeout = "timeout"
	// FailureAgentError: the agent answered with an error, or with something that isn't A2A.
	FailureAgentError = "agent-error"
)

// classifyFailure sorts a delivery error into a Failure kind and a short detail.
func classifyFailure(err error) (kind, detail string) {
	var down *tunnel.UnreachableError
	if errors.As(err, &down) {
		return FailureAgentDown, down.Reason
	}
	var netErr net.Error
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout"):
		return FailureTimeout, msg
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no such host") || strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "connection reset"):
		return FailureUnreachable, msg
	}
	return FailureAgentError, msg
}
