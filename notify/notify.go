// Package notify tells people that a task waits on them: by email and in Slack, each
// with a one-time link that opens the question and answers it without logging in.
//
// The relay calls Waiting whenever a task stops on INPUT_REQUIRED or AUTH_REQUIRED. The
// task waits on the address it names as awaiting, or else on its sender (an agent's
// question goes back to whoever asked it). Only people are notified: an agent that sent
// a task hears about the question from its own A2A client.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"

	"github.com/three-agent-problem/lagrange/credentials"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/relay"
)

// LinkTTL is how long a decision link works.
const LinkTTL = 7 * 24 * time.Hour

// Email sends one plain-text email.
type Email interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Message is one notification, before it is rendered for a channel.
type Message struct {
	Subject string
	// Intro comes first: why this arrives again (a reminder) or to someone else (an escalation).
	Intro string
	// Asker is the address asking, Question what it asks (text, then data as key: value lines).
	Asker, Question string
	// Of is who is asked, when it isn't the reader (an escalation); empty means "you".
	Of string
	// Link answers the task once, without logging in. Inbox is the person's inbox.
	Link, Inbox string
}

// Text renders the message as a plain-text email body.
func (m *Message) Text() string {
	var b strings.Builder
	if m.Intro != "" {
		fmt.Fprintf(&b, "%s\n\n", m.Intro)
	}
	of := "you"
	if m.Of != "" {
		of = "@" + m.Of
	}
	fmt.Fprintf(&b, "@%s asks %s:\n\n%s\n\n", m.Asker, of, m.Question)
	if m.Link != "" {
		fmt.Fprintf(&b, "Answer here (no login needed; the link works once and expires in %d days):\n%s\n\n", int(LinkTTL.Hours()/24), m.Link)
	}
	fmt.Fprintf(&b, "Or open your inbox: %s\n", m.Inbox)
	return b.String()
}

// Channel is a notification channel of its own, such as a person's Telegram bot. It
// decides for itself whether a person has it on, and gets no decision link: a channel
// that answers does so through its own, narrower path.
type Channel interface {
	// Notice sends nt to u, if u has the channel on; sent says whether it did.
	Notice(ctx context.Context, u *directory.User, nt Notice) (sent bool, err error)
	// Test sends a test message to u, if u has the channel on.
	Test(ctx context.Context, u *directory.User) (sent bool, err error)
}

// Notifier sends notifications. Its zero channels are off: without Email it sends no
// email, and Slack is per person (NotificationSettings.SlackWebhook).
type Notifier struct {
	Directory *directory.Directory
	// PublicURL is the server's base URL, for links.
	PublicURL string
	Email     Email
	// Channels are the other channels (Telegram).
	Channels []Channel
	// HTTP posts to Slack (default: a client with a 10s timeout).
	HTTP   *http.Client
	Logger *slog.Logger

	wg sync.WaitGroup
}

func (n *Notifier) log() *slog.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return slog.Default()
}

// Waiting notifies the person a task waits on, in the background. Wait waits for the
// notifications in flight.
func (n *Notifier) Waiting(ctx context.Context, w relay.Waiting) {
	e := w.Entry
	who := e.Sender
	if h, ok := e.Data["awaiting"].(string); ok && h != "" {
		who = h
	}
	if who == "" {
		return
	}
	asker := e.Recipient
	if who == e.Recipient {
		asker = e.Sender
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := n.Notify(ctx, Notice{To: who, Asker: asker, TaskID: e.TaskID, State: e.State, Question: w.Question}); err != nil {
			n.log().Warn("notify: sending failed", "to", who, "task", e.TaskID, "error", err)
		}
	}()
}

// Wait blocks until the notifications in flight are sent.
func (n *Notifier) Wait() { n.wg.Wait() }

// Notice is one notification about a task waiting on someone.
type Notice struct {
	// To is the person notified; Asker the address asking.
	To, Asker string
	TaskID    string
	// State is the task's state (TASK_STATE_AUTH_REQUIRED asks to connect an account).
	State    string
	Question *a2a.Message
	// Kind is "" for the first notice, NoticeReminder or NoticeEscalation.
	Kind string
	// Waited is how long the task has waited, for reminders and escalations.
	Waited time.Duration
	// For is who the task waits on, when To is someone else (an escalation).
	For string
}

// Notice kinds.
const (
	NoticeReminder   = "reminder"
	NoticeEscalation = "escalation"
)

// Notify sends a notice to its person's channels, with a one-time answer link. Agents
// and people with no channel on get nothing (the task waits in the inbox).
func (n *Notifier) Notify(ctx context.Context, nt Notice) error {
	who, asker, taskID, state := nt.To, nt.Asker, nt.TaskID, nt.State
	u, err := n.Directory.UserByHandle(ctx, who)
	if errors.Is(err, directory.ErrNotFound) {
		return nil // an agent
	}
	if err != nil {
		return err
	}
	s, err := n.Directory.NotificationSettings(ctx, u.ID)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range n.Channels {
		if _, err := c.Notice(ctx, u, nt); err != nil {
			errs = append(errs, err)
		}
	}
	email := n.Email != nil && s.Email && u.Email != ""
	if !email && s.SlackWebhook == "" {
		n.log().Debug("notify: no email or Slack", "to", who, "task", taskID)
		return errors.Join(errs...)
	}
	token, err := n.Directory.MintDecisionLink(ctx, taskID, who, LinkTTL)
	if err != nil {
		return err
	}
	m := &Message{
		Subject:  "@" + asker + " needs your answer",
		Asker:    asker,
		Question: Render(nt.Question),
		Link:     n.PublicURL + "/d/" + token,
		Inbox:    n.PublicURL + "/inbox",
	}
	if state == string(a2a.TaskStateAuthRequired) {
		m.Subject = "@" + asker + " needs you to connect an account"
	}
	switch nt.Kind {
	case NoticeReminder:
		m.Subject = "Reminder: " + m.Subject
		m.Intro = "This has waited " + Ago(nt.Waited) + "."
	case NoticeEscalation:
		m.Subject = fmt.Sprintf("Escalated: @%s has waited %s for @%s", asker, Ago(nt.Waited), nt.For)
		m.Intro = fmt.Sprintf("@%s hasn't answered for %s, so this comes to you. You can answer on their behalf.", nt.For, Ago(nt.Waited))
		m.Of = nt.For
	}
	if email {
		errs = append(errs, n.Email.Send(ctx, u.Email, m.Subject, m.Text()))
	}
	if s.SlackWebhook != "" {
		errs = append(errs, n.Slack(ctx, s.SlackWebhook, m))
	}
	return errors.Join(errs...)
}

// Test sends a test notification to a person's channels.
func (n *Notifier) Test(ctx context.Context, u *directory.User) error {
	s, err := n.Directory.NotificationSettings(ctx, u.ID)
	if err != nil {
		return err
	}
	m := &Message{Subject: "Lagrange notifications work", Asker: "lagrange", Question: "This is a test. Questions for you will arrive here.", Inbox: n.PublicURL + "/inbox"}
	var errs []error
	sent := false
	for _, c := range n.Channels {
		ok, err := c.Test(ctx, u)
		sent = sent || ok
		if err != nil {
			errs = append(errs, err)
		}
	}
	if n.Email != nil && s.Email {
		sent = true
		errs = append(errs, n.Email.Send(ctx, u.Email, m.Subject, m.Text()))
	}
	if s.SlackWebhook != "" {
		sent = true
		errs = append(errs, n.Slack(ctx, s.SlackWebhook, m))
	}
	if !sent {
		return errors.New("no channel is on")
	}
	return errors.Join(errs...)
}

// Slack posts m to a Slack incoming webhook.
func (n *Notifier) Slack(ctx context.Context, webhook string, m *Message) error {
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	text := fmt.Sprintf("*%s*\n%s", m.Subject, quote(m.Question))
	if m.Intro != "" {
		text = fmt.Sprintf("*%s*\n%s\n%s", m.Subject, m.Intro, quote(m.Question))
	}
	if m.Link != "" {
		text += fmt.Sprintf("\n<%s|Answer now> · <%s|Open the inbox>", m.Link, m.Inbox)
	} else {
		text += fmt.Sprintf("\n<%s|Open the inbox>", m.Inbox)
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %s", resp.Status)
	}
	return nil
}

func quote(s string) string {
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}

// ValidSlackWebhook reports whether url is a Slack incoming webhook. Only those are
// accepted, so a setting can't make the server post to arbitrary hosts.
func ValidSlackWebhook(url string) bool {
	return strings.HasPrefix(url, "https://hooks.slack.com/")
}

// Render renders a message for people: its text, then each data part as "key: value"
// lines, so nobody has to read JSON. Parts meant for machines (an input schema, a
// credential challenge, whose text part already carries the link) are left out.
func Render(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var out []string
	for _, p := range m.Parts {
		meta := p.Meta()
		switch {
		case meta[structuredinput.URI] != nil, meta[credentials.MetadataKey] != nil:
		case p.Text() != "":
			out = append(out, p.Text())
		case p.Data() != nil:
			out = append(out, renderData(p.Data()))
		case p.Filename != "":
			out = append(out, "File: "+p.Filename)
		}
	}
	return strings.Join(out, "\n\n")
}

func renderData(v any) string {
	obj, ok := v.(map[string]any)
	if !ok {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		val := obj[k]
		var s string
		switch val := val.(type) {
		case string:
			s = val
		default:
			raw, _ := json.Marshal(val)
			s = string(raw)
		}
		lines = append(lines, k+": "+s)
	}
	return strings.Join(lines, "\n")
}

// Ago is a waiting time in words: "45 minutes", "2 hours", "3 days".
func Ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d < time.Minute:
		return plural(max(1, int(d.Seconds())), "second")
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}
