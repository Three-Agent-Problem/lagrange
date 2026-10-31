package platform_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/relay"
)

// find waits for an email matching ok and returns it.
func (o *outbox) find(t *testing.T, what string, ok func(sentMail) bool) sentMail {
	t.Helper()
	var got sentMail
	waitFor(t, what, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		for _, m := range o.sent {
			if ok(m) {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

func fastWaits(c *platform.Config) { c.WaitTick = 100 * time.Millisecond }

func seconds(n int64) *int64 { return &n }

// The team's policy reminds the person a task waits on, then escalates to a backup who
// can answer on their behalf.
func TestWaitPolicyRemindsAndEscalates(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out }, fastWaits)
	ctx := t.Context()
	if _, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	ada := e.login(t, adminEmail, adminPassword)
	ada.do(http.MethodPut, "/api/settings/wait-policy", platform.WaitPolicy{RemindAfter: seconds(1), EscalateAfter: seconds(3), EscalateTo: "bob"}, http.StatusOK, nil)

	c := e.client(t, "ada", e.agent(t, "buyer"), "")
	task := asTask(t)(c.SendMessage(ctx, text("Approve 200 hoodies?")))

	reminder := out.find(t, "a reminder to ada", func(m sentMail) bool { return m.to == adminEmail && strings.HasPrefix(m.subject, "Reminder: @buyer") })
	if !strings.Contains(reminder.body, "This has waited") || !strings.Contains(reminder.body, "Approve 200 hoodies?") {
		t.Fatalf("reminder:\n%s", reminder.body)
	}
	escalation := out.find(t, "an escalation to bob", func(m sentMail) bool { return m.to == "bob@example.com" })
	if !strings.HasPrefix(escalation.subject, "Escalated: @buyer has waited") || !strings.HasSuffix(escalation.subject, "for @ada") ||
		!strings.Contains(escalation.body, "@buyer asks @ada:") {
		t.Fatalf("escalation = %q:\n%s", escalation.subject, escalation.body)
	}

	var board struct {
		Waiting []struct {
			Who, EscalatedTo string
			Reminders        int
			RunID            string
		}
	}
	ada.do(http.MethodGet, "/api/waiting", nil, http.StatusOK, &board)
	if len(board.Waiting) != 1 || board.Waiting[0].Who != "ada" || board.Waiting[0].EscalatedTo != "bob" || board.Waiting[0].Reminders < 1 || board.Waiting[0].RunID != string(task.ID) {
		t.Fatalf("board = %+v", board.Waiting)
	}

	// Bob answers from his link, on Ada's behalf.
	anon(t, e, http.MethodPost, "/api/decisions/"+linkToken(t, escalation.body), "application/json", `{"text":"Approved while Ada is out."}`, http.StatusAccepted, nil)
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := done.Status.Message.Metadata[relay.AnsweredByKey]; got != "bob" {
		t.Fatalf("answered by %v", got)
	}
	ada.do(http.MethodGet, "/api/waiting", nil, http.StatusOK, &board)
	if len(board.Waiting) != 0 {
		t.Fatalf("board after the answer = %+v", board.Waiting)
	}
}

// A task that waits longer than the policy allows is canceled; only admins set the policy.
func TestWaitPolicyCancels(t *testing.T) {
	e := newEnv(t, "", fastWaits)
	ctx := t.Context()
	if _, err := e.p.Directory.CreateUser(ctx, "bob@example.com", "Bob", "bob", "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	bob := e.login(t, "bob@example.com", "hunter2hunter2")
	bob.do(http.MethodPut, "/api/settings/wait-policy", platform.WaitPolicy{CancelAfter: seconds(1)}, http.StatusForbidden, nil)
	ada := e.login(t, adminEmail, adminPassword)
	ada.do(http.MethodPut, "/api/settings/wait-policy", platform.WaitPolicy{EscalateAfter: seconds(1), EscalateTo: "buyer"}, http.StatusBadRequest, nil)
	ada.do(http.MethodPut, "/api/settings/wait-policy", platform.WaitPolicy{CancelAfter: seconds(0)}, http.StatusBadRequest, nil)
	ada.do(http.MethodPut, "/api/settings/wait-policy", platform.WaitPolicy{CancelAfter: seconds(1)}, http.StatusOK, nil)

	c := e.client(t, "ada", e.agent(t, "buyer"), "")
	task := asTask(t)(c.SendMessage(ctx, text("Approve?")))
	waitState(t, c, task.ID, a2a.TaskStateCanceled)
}

// Remind now nudges the person once a minute at most, and never an agent.
func TestRemindNow(t *testing.T) {
	out := &outbox{}
	e := newEnv(t, "", func(c *platform.Config) { c.Email = out })
	c := e.client(t, "ada", e.agent(t, "buyer"), "")
	task := asTask(t)(c.SendMessage(t.Context(), text("Approve?")))
	ada := e.login(t, adminEmail, adminPassword)
	out.next(t, 1)
	ada.do(http.MethodPost, "/api/waiting/"+string(task.ID)+"/remind", struct{}{}, http.StatusAccepted, nil)
	if m := out.next(t, 2); !strings.HasPrefix(m.subject, "Reminder: ") {
		t.Fatalf("reminder = %+v", m)
	}
	ada.do(http.MethodPost, "/api/waiting/"+string(task.ID)+"/remind", struct{}{}, http.StatusTooManyRequests, nil)
	ada.do(http.MethodPost, "/api/waiting/nope/remind", struct{}{}, http.StatusNotFound, nil)
}
