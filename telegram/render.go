package telegram

import (
	"fmt"
	"html"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/three-agent-problem/lagrange/ask"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/report"
)

// maxBody keeps a message under Telegram's 4096 characters, with room for the footer
// and the closing line.
const maxBody = 3200

func esc(s string) string { return html.EscapeString(s) }

// clip cuts s to n characters.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// questionBody is the text of a message about a task waiting on the person.
func questionBody(nt notify.Notice, inbox string) string {
	var b strings.Builder
	switch nt.Kind {
	case notify.NoticeReminder:
		fmt.Fprintf(&b, "⏰ <b>Reminder</b> · this has waited %s\n", esc(notify.Ago(nt.Waited)))
	case notify.NoticeEscalation:
		fmt.Fprintf(&b, "⤴️ <b>Escalated</b> · @%s hasn't answered for %s, so this comes to you\n", esc(nt.For), esc(notify.Ago(nt.Waited)))
	}
	of := "you"
	if nt.For != "" {
		of = "@" + nt.For
	}
	if nt.State == "TASK_STATE_AUTH_REQUIRED" {
		fmt.Fprintf(&b, "🔑 <b>@%s</b> needs %s to connect an account\n\n", esc(nt.Asker), esc(of))
	} else {
		fmt.Fprintf(&b, "❓ <b>@%s</b> asks %s\n\n", esc(nt.Asker), esc(of))
	}
	b.WriteString(esc(clip(notify.Render(nt.Question), maxBody)))
	fmt.Fprintf(&b, "\n\n<i>Inbox: %s</i>", esc(inbox))
	return b.String()
}

// closedLine says how a message's task ended, under its text.
func closed(body, line string) string { return body + "\n\n" + line }

// keyboard renders a message's buttons for its view.
func keyboard(m *message, bs []*button, confirming bool) Keyboard {
	byAction := func(action string) *button {
		for _, b := range bs {
			if b.Action == action {
				return b
			}
		}
		return nil
	}
	switch m.Kind {
	case kindApproval:
		if confirming {
			yes, back := byAction(actConfirm), byAction(actBack)
			if yes == nil || back == nil {
				return nil
			}
			return Keyboard{{{Text: "✓ Yes, approve", Data: yes.Token}, {Text: "← Back", Data: back.Token}}}
		}
		ok, no := byAction(actApprove), byAction(actReject)
		if ok == nil || no == nil {
			return nil
		}
		return Keyboard{{{Text: "✓ Approve", Data: ok.Token}, {Text: "✕ Reject", Data: no.Token}}}
	case kindForm:
		var kb Keyboard
		for qi, q := range m.Questions {
			var row []Button
			for oi, o := range q.Options {
				for _, b := range bs {
					if b.Action != actPick || b.Question != qi || b.Option != oi {
						continue
					}
					label := o.Label
					if len(m.Questions) > 1 {
						label = fmt.Sprintf("%d · %s", qi+1, label)
					}
					if qi < len(m.Picks) && slices.Contains(m.Picks[qi], oi) {
						label = "✓ " + label
					}
					row = append(row, Button{Text: clip(label, 40), Data: b.Token})
				}
			}
			// Two options a row: labels stay readable on a phone.
			for len(row) > 0 {
				n := min(2, len(row))
				kb = append(kb, row[:n])
				row = row[n:]
			}
		}
		if send := byAction(actSend); send != nil {
			kb = append(kb, []Button{{Text: "Send answers", Data: send.Token}})
		}
		return kb
	}
	return nil
}

// buttonsFor lists the buttons a message of kind needs.
func buttonsFor(kind string, qs []ask.Question) []*button {
	switch kind {
	case kindApproval:
		return []*button{{Action: actApprove}, {Action: actConfirm}, {Action: actBack}, {Action: actReject}}
	case kindForm:
		var bs []*button
		for qi, q := range qs {
			for oi := range q.Options {
				bs = append(bs, &button{Action: actPick, Question: qi, Option: oi})
			}
		}
		return append(bs, &button{Action: actSend})
	}
	return nil
}

// answers turns a form's picks into ask answers.
func answers(qs []ask.Question, picks [][]int) []ask.Answer {
	out := make([]ask.Answer, len(qs))
	for i := range qs {
		if i < len(picks) {
			out[i] = ask.Answer{Picked: picks[i]}
		}
	}
	return out
}

// toggle picks option o of question q: a single-choice question keeps one pick.
func toggle(qs []ask.Question, picks [][]int, q, o int) [][]int {
	for len(picks) < len(qs) {
		picks = append(picks, nil)
	}
	if q < 0 || q >= len(qs) || o < 0 || o >= len(qs[q].Options) {
		return picks
	}
	cur := picks[q]
	switch {
	case slices.Contains(cur, o):
		cur = slices.DeleteFunc(slices.Clone(cur), func(x int) bool { return x == o })
	case qs[q].MultiSelect:
		cur = append(slices.Clone(cur), o)
		slices.Sort(cur)
	default:
		cur = []int{o}
	}
	picks[q] = cur
	return picks
}

// unanswered returns the number (from 1) of the first question with no pick, or 0.
func unanswered(qs []ask.Question, picks [][]int) int {
	for i := range qs {
		if i >= len(picks) || len(picks[i]) == 0 {
			return i + 1
		}
	}
	return 0
}

// Run is a run, as Telegram shows it.
type Run struct {
	ID    string
	Title string
	// State is the root task's state; Status the run's group (running, waiting, done, failed).
	State, Status string
	Waiting       []string
	Cost          float64
	Currency      string
	StartedAt     time.Time
	UpdatedAt     time.Time
	Outcome       string
	Report        *report.Report
}

func money(v float64, currency string) string {
	if currency == "" || currency == "USD" {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, currency)
}

// Dur is a duration in a few characters: "45s", "14m", "2h05m", "3d".
func Dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(math.Max(0, d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

var statusIcon = map[string]string{"running": "⏳", "waiting": "✋", "done": "✅", "failed": "❌"}

// runLine is a run in one line, for /status and /runs.
func runLine(r Run, now time.Time) string {
	icon := statusIcon[r.Status]
	if icon == "" {
		icon = "•"
	}
	title := r.Title
	if r.Report != nil {
		title = r.Report.Headline
	}
	parts := []string{r.Status}
	if r.Report != nil && r.Report.Progress != nil && r.Report.Progress.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", r.Report.Progress.Done, r.Report.Progress.Total))
	}
	if len(r.Waiting) > 0 {
		parts = append(parts, "waiting on @"+strings.Join(r.Waiting, ", @"))
	}
	if r.Cost > 0 {
		parts = append(parts, money(r.Cost, r.Currency))
	}
	end := r.UpdatedAt
	if r.Status == "running" || r.Status == "waiting" {
		end = now
	}
	parts = append(parts, Dur(end.Sub(r.StartedAt)))
	return fmt.Sprintf("%s <b>%s</b>\n    %s", icon, esc(clip(title, 80)), esc(strings.Join(parts, " · ")))
}

// reportHTML renders a report for Telegram.
func reportHTML(r *report.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>", esc(clip(r.Headline, 200)))
	var line []string
	if r.Status != "" {
		line = append(line, r.Status)
	}
	if p := r.Progress; p != nil && p.Total > 0 {
		line = append(line, fmt.Sprintf("%d/%d done", p.Done, p.Total))
	}
	if len(line) > 0 {
		fmt.Fprintf(&b, "\n%s", esc(strings.Join(line, " · ")))
	}
	if r.Outcome != "" {
		fmt.Fprintf(&b, "\n\n%s", esc(clip(r.Outcome, 800)))
	}
	if len(r.Built) > 0 {
		b.WriteString("\n\n<b>Built</b>")
		for i, it := range r.Built {
			if i == 15 {
				fmt.Fprintf(&b, "\n• … and %d more", len(r.Built)-i)
				break
			}
			if it.Detail != "" {
				fmt.Fprintf(&b, "\n• %s <i>· %s</i>", esc(clip(it.Title, 100)), esc(clip(it.Detail, 100)))
			} else {
				fmt.Fprintf(&b, "\n• %s", esc(clip(it.Title, 100)))
			}
		}
	}
	if len(r.Stats) > 0 {
		parts := make([]string, len(r.Stats))
		for i, s := range r.Stats {
			parts[i] = s.Label + " " + s.Value
		}
		fmt.Fprintf(&b, "\n\n%s", esc(clip(strings.Join(parts, " · "), 600)))
	}
	return b.String()
}

// runCard is the message about a finished run.
func runCard(r Run, url string) string {
	icon, word := "✅", "Run finished"
	if r.Status == "failed" {
		icon, word = "❌", "Run ended: "+strings.ToLower(strings.TrimPrefix(r.State, "TASK_STATE_"))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> · %s\n\n", icon, esc(word), esc(Dur(r.UpdatedAt.Sub(r.StartedAt))))
	if r.Report != nil {
		b.WriteString(reportHTML(r.Report))
	} else {
		fmt.Fprintf(&b, "<b>%s</b>", esc(clip(r.Title, 200)))
		if r.Outcome != "" {
			fmt.Fprintf(&b, "\n\n%s", esc(clip(r.Outcome, 800)))
		}
	}
	if r.Cost > 0 && (r.Report == nil || !hasStat(r.Report, "cost")) {
		fmt.Fprintf(&b, "\n\nCost %s", esc(money(r.Cost, r.Currency)))
	}
	fmt.Fprintf(&b, "\n\n<i>%s</i>", esc(url))
	return b.String()
}

func hasStat(r *report.Report, label string) bool {
	for _, s := range r.Stats {
		if strings.EqualFold(s.Label, label) {
			return true
		}
	}
	return false
}

// noticeHTML is a report's notice, forwarded to whoever started the run.
func noticeHTML(r *report.Report, url string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔔 %s\n\n", esc(clip(r.Notice, 600)))
	fmt.Fprintf(&b, "<b>%s</b>", esc(clip(r.Headline, 200)))
	var line []string
	if r.Status != "" {
		line = append(line, r.Status)
	}
	if p := r.Progress; p != nil && p.Total > 0 {
		line = append(line, fmt.Sprintf("%d/%d done", p.Done, p.Total))
	}
	if len(line) > 0 {
		fmt.Fprintf(&b, "\n%s", esc(strings.Join(line, " · ")))
	}
	fmt.Fprintf(&b, "\n\n<i>%s</i>", esc(url))
	return b.String()
}

const helpText = `◉ <b>Lagrange</b>

This bot tells you when something waits on you, and you answer with its buttons.

/status · what waits on you and your runs now
/waiting · send what waits on you again, with buttons
/runs · your last runs
/mute 2h · quiet for a while (default 1h)
/unmute · notifications back on

It can't start, stop or message anything: do that in the terminal or the web app.`

const refusal = "This bot only answers with its buttons. To send a message or start something, use the terminal or the web app. /help"
