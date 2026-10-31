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
	// Asker is the address asking, Question what it asks (text, then data as key: value lines).
	Asker, Question string
	// Link answers the task once, without logging in. Inbox is the person's inbox.
	Link, Inbox string
}

// Text renders the message as a plain-text email body.
func (m *Message) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "@%s asks you:\n\n%s\n\n", m.Asker, m.Question)
	if m.Link != "" {
		fmt.Fprintf(&b, "Answer here (no login needed; the link works once and expires in %d days):\n%s\n\n", int(LinkTTL.Hours()/24), m.Link)
	}
	fmt.Fprintf(&b, "Or open your inbox: %s\n", m.Inbox)
	return b.String()
}

// Notifier sends notifications. Its zero channels are off: without Email it sends no
// email, and Slack is per person (NotificationSettings.SlackWebhook).
type Notifier struct {
	Directory *directory.Directory
	// PublicURL is the server's base URL, for links.
	PublicURL string
	Email     Email
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
		if err := n.notify(ctx, who, asker, e.TaskID, e.State, w.Question); err != nil {
			n.log().Warn("notify: sending failed", "to", who, "task", e.TaskID, "error", err)
		}
	}()
}

// Wait blocks until the notifications in flight are sent.
func (n *Notifier) Wait() { n.wg.Wait() }

func (n *Notifier) notify(ctx context.Context, who, asker, taskID, state string, question *a2a.Message) error {
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
	email := n.Email != nil && s.Email && u.Email != ""
	if !email && s.SlackWebhook == "" {
		n.log().Debug("notify: no channel; the task waits in the inbox", "to", who, "task", taskID)
		return nil
	}
	token, err := n.Directory.MintDecisionLink(ctx, taskID, who, LinkTTL)
	if err != nil {
		return err
	}
	m := &Message{
		Subject:  "@" + asker + " needs your answer",
		Asker:    asker,
		Question: Render(question),
		Link:     n.PublicURL + "/d/" + token,
		Inbox:    n.PublicURL + "/inbox",
	}
	if state == string(a2a.TaskStateAuthRequired) {
		m.Subject = "@" + asker + " needs you to connect an account"
	}
	var errs []error
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
