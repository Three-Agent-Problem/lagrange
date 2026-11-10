package platform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/three-agent-problem/lagrange/ask"
	"github.com/three-agent-problem/lagrange/platform"
	"github.com/three-agent-problem/lagrange/relay"
	"github.com/three-agent-problem/lagrange/report"
	"github.com/three-agent-problem/lagrange/telegram"
)

const tgToken = "123456789:AAH-fake-token-for-tests-0123456789abcd"

// tgMsg is a message the fake Telegram holds: its latest text and buttons.
type tgMsg struct {
	ID     int64
	Chat   int64
	Text   string
	Kb     telegram.Keyboard
	Edited int
}

// fakeTelegram is the Bot API, enough of it for one bot.
type fakeTelegram struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	updates   []map[string]any
	nextUp    int64
	msgs      []*tgMsg
	callbacks map[string]string // callback id -> the text it was answered with
	wake      chan struct{}
	nextCB    int
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{t: t, callbacks: map[string]string{}, wake: make(chan struct{}, 1)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/bot")
	token, method, _ := strings.Cut(path, "/")
	ok := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
	if token != tgToken {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	num := func(k string) int64 { var v int64; _ = json.Unmarshal(p[k], &v); return v }
	str := func(k string) string { var v string; _ = json.Unmarshal(p[k], &v); return v }
	kb := func() telegram.Keyboard {
		var m struct {
			InlineKeyboard telegram.Keyboard `json:"inline_keyboard"`
		}
		_ = json.Unmarshal(p["reply_markup"], &m)
		return m.InlineKeyboard
	}
	switch method {
	case "getMe":
		ok(map[string]any{"id": 123456789, "is_bot": true, "username": "ada_lagrange_bot", "first_name": "Ada's bot"})
	case "deleteWebhook":
		ok(true)
	case "getUpdates":
		offset := num("offset")
		deadline := time.After(time.Duration(num("timeout")) * time.Second)
		for {
			f.mu.Lock()
			var out []map[string]any
			for _, u := range f.updates {
				if u["update_id"].(int64) >= offset {
					out = append(out, u)
				}
			}
			f.mu.Unlock()
			if len(out) > 0 {
				ok(out)
				return
			}
			select {
			case <-f.wake:
			case <-deadline:
				ok([]any{})
				return
			case <-r.Context().Done():
				return
			}
		}
	case "sendMessage":
		f.mu.Lock()
		m := &tgMsg{ID: int64(len(f.msgs) + 1), Chat: num("chat_id"), Text: str("text"), Kb: kb()}
		f.msgs = append(f.msgs, m)
		f.mu.Unlock()
		ok(map[string]any{"message_id": m.ID, "chat": map[string]any{"id": m.Chat, "type": "private"}})
	case "editMessageText":
		f.mu.Lock()
		id := num("message_id")
		if id >= 1 && int(id) <= len(f.msgs) {
			m := f.msgs[id-1]
			m.Text, m.Kb = str("text"), kb()
			m.Edited++
		}
		f.mu.Unlock()
		ok(true)
	case "answerCallbackQuery":
		f.mu.Lock()
		f.callbacks[str("callback_query_id")] = str("text")
		f.mu.Unlock()
		ok(true)
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "unknown method " + method})
	}
}

func (f *fakeTelegram) push(u map[string]any) {
	f.mu.Lock()
	f.nextUp++
	u["update_id"] = f.nextUp
	f.updates = append(f.updates, u)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// say sends text from a Telegram user in a chat.
func (f *fakeTelegram) say(chat, user int64, chatType, text string) {
	f.push(map[string]any{"message": map[string]any{
		"message_id": 1000 + f.nextUp, "text": text,
		"chat": map[string]any{"id": chat, "type": chatType},
		"from": map[string]any{"id": user, "is_bot": false, "username": fmt.Sprintf("user%d", user)},
	}})
}

// press presses a button of message m as user, and returns the callback's ID.
func (f *fakeTelegram) press(m *tgMsg, user int64, data string) string {
	f.mu.Lock()
	f.nextCB++
	id := fmt.Sprintf("cb%d", f.nextCB)
	f.mu.Unlock()
	f.push(map[string]any{"callback_query": map[string]any{
		"id": id, "data": data,
		"from":    map[string]any{"id": user, "is_bot": false},
		"message": map[string]any{"message_id": m.ID, "chat": map[string]any{"id": m.Chat, "type": "private"}},
	}})
	return id
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// next waits for the n-th message (from 1).
func (f *fakeTelegram) next(n int) *tgMsg {
	f.t.Helper()
	waitFor(f.t, fmt.Sprintf("telegram message %d", n), func() bool { return f.count() >= n })
	return f.get(n)
}

func (f *fakeTelegram) get(n int) *tgMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := *f.msgs[n-1]
	return &m
}

// callback waits for the answer to a callback.
func (f *fakeTelegram) callback(id string) string {
	f.t.Helper()
	var text string
	waitFor(f.t, "callback "+id, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		var ok bool
		text, ok = f.callbacks[id]
		return ok
	})
	return text
}

// quiet checks that nothing new is sent for a moment.
func (f *fakeTelegram) quiet(t *testing.T, n int, why string) {
	t.Helper()
	time.Sleep(400 * time.Millisecond)
	if got := f.count(); got != n {
		t.Fatalf("%s: telegram has %d messages, want %d: %q", why, got, n, f.get(got).Text)
	}
}

func button(t *testing.T, m *tgMsg, label string) string {
	t.Helper()
	for _, row := range m.Kb {
		for _, b := range row {
			if strings.Contains(b.Text, label) {
				return b.Data
			}
		}
	}
	t.Fatalf("no button %q in %+v", label, m.Kb)
	return ""
}

var pairCode = regexp.MustCompile(`\?start=([a-z0-9]+)$`)

const adaChat, adaTG, strangerTG = 4242, 4242, 666

// The bot's whole reach: answering through the inbox's checks, and three reads.
func TestTelegramDepsAreClosed(t *testing.T) {
	typ := reflect.TypeFor[telegram.Deps]()
	var names []string
	for f := range typ.Fields() {
		names = append(names, f.Name)
	}
	if want := []string{"Answer", "Waiting", "Runs", "Run"}; !slices.Equal(names, want) {
		t.Fatalf("telegram.Deps = %v, want %v: the bot must not reach anything that starts, cancels or sends", names, want)
	}
}

func telegramEnv(t *testing.T) (*env, *fakeTelegram, *api) {
	f := newFakeTelegram(t)
	e := newEnv(t, "", func(c *platform.Config) {
		c.TelegramAPIBase = f.srv.URL
		c.TelegramTick = 30 * time.Millisecond
		c.TelegramPoll = time.Second
	})
	ada := e.login(t, adminEmail, adminPassword)
	return e, f, ada
}

// pairAda connects and pairs Ada's bot, and returns the number of messages so far.
func pairAda(t *testing.T, f *fakeTelegram, ada *api) int {
	t.Helper()
	var st telegram.Status
	ada.do(http.MethodPut, "/api/settings/telegram", map[string]string{"token": tgToken}, http.StatusOK, &st)
	m := pairCode.FindStringSubmatch(st.PairLink)
	if m == nil || !st.Connected || st.Paired || st.BotUsername != "ada_lagrange_bot" {
		t.Fatalf("connect = %+v", st)
	}
	n := f.count()
	f.say(adaChat, adaTG, "private", "/start "+m[1])
	if got := f.next(n + 1); got.Chat != adaChat || !strings.Contains(got.Text, "Paired with <b>@ada</b>") {
		t.Fatalf("pairing reply = %+v", got)
	}
	return n + 1
}

func TestTelegramPairing(t *testing.T) {
	_, f, ada := telegramEnv(t)

	// Bad tokens never reach Telegram's API as a working bot.
	ada.do(http.MethodPut, "/api/settings/telegram", map[string]string{"token": "nope"}, http.StatusBadRequest, nil)
	ada.do(http.MethodPut, "/api/settings/telegram", map[string]string{"token": "987654321:AAH-some-other-token-0123456789abcdef"}, http.StatusBadRequest, nil)

	var raw json.RawMessage
	ada.do(http.MethodPut, "/api/settings/telegram", map[string]string{"token": tgToken}, http.StatusOK, &raw)
	if strings.Contains(string(raw), tgToken) || strings.Contains(string(raw), "AAH-fake") {
		t.Fatalf("the API returned the token: %s", raw)
	}
	var st telegram.Status
	_ = json.Unmarshal(raw, &st)
	code := pairCode.FindStringSubmatch(st.PairLink)[1]

	// A stranger guessing: told no, and never paired.
	f.say(9999, strangerTG, "private", "/start wrongcode")
	if got := f.next(1); !strings.Contains(got.Text, "didn't work") || got.Chat != 9999 {
		t.Fatalf("wrong code reply = %+v", got)
	}
	// A stranger's commands get nothing.
	f.say(9999, strangerTG, "private", "/status")
	f.quiet(t, 1, "a stranger's /status")

	// The right code, from a group: ignored.
	f.say(-100, adaTG, "group", "/start "+code)
	f.quiet(t, 1, "pairing from a group")

	f.say(adaChat, adaTG, "private", "/start "+code)
	if got := f.next(2); !strings.Contains(got.Text, "Paired with <b>@ada</b>") {
		t.Fatalf("pairing reply = %q", got.Text)
	}
	st = telegram.Status{}
	ada.do(http.MethodGet, "/api/settings/telegram", nil, http.StatusOK, &st)
	if !st.Paired || st.ChatUsername != fmt.Sprintf("user%d", adaTG) || st.PairLink != "" {
		t.Fatalf("status = %+v", st)
	}

	// The used code no longer pairs anyone, and the stranger still gets nothing.
	f.say(9999, strangerTG, "private", "/start "+code)
	f.say(9999, strangerTG, "private", "/status")
	f.say(adaChat, strangerTG, "private", "/status") // Ada's chat ID, someone else's user ID
	f.quiet(t, 2, "after pairing, strangers")

	// Typed text and unknown commands are refused, never forwarded.
	f.say(adaChat, adaTG, "private", "please delete the repo")
	if got := f.next(3); !strings.Contains(got.Text, "only answers with its buttons") {
		t.Fatalf("refusal = %q", got.Text)
	}
	f.say(adaChat, adaTG, "private", "/new build a thing")
	if got := f.next(4); !strings.Contains(got.Text, "only answers with its buttons") {
		t.Fatalf("refusal = %q", got.Text)
	}
	f.say(adaChat, adaTG, "private", "/status")
	if got := f.next(5); !strings.Contains(got.Text, "Nothing waits on you") {
		t.Fatalf("status = %q", got.Text)
	}

	// Five wrong codes kill a fresh code.
	ada.do(http.MethodPost, "/api/settings/telegram/pair", map[string]any{}, http.StatusOK, &st)
	fresh := pairCode.FindStringSubmatch(st.PairLink)[1]
	for i := range 5 {
		f.say(9999, strangerTG, "private", fmt.Sprintf("/start guess%d", i))
	}
	f.next(10)
	f.say(9999, strangerTG, "private", "/start "+fresh)
	f.quiet(t, 10, "a killed code")
	ada.do(http.MethodGet, "/api/settings/telegram", nil, http.StatusOK, &st)
	if !st.Paired || st.ChatUsername != fmt.Sprintf("user%d", adaTG) {
		t.Fatalf("the stranger took over: %+v", st)
	}

	ada.do(http.MethodDelete, "/api/settings/telegram", struct{}{}, http.StatusNoContent, nil)
	ada.do(http.MethodGet, "/api/settings/telegram", nil, http.StatusOK, &st)
	if st.Connected {
		t.Fatalf("still connected: %+v", st)
	}
}

func TestTelegramApproval(t *testing.T) {
	e, f, ada := telegramEnv(t)
	n := pairAda(t, f, ada)
	ctx := t.Context()
	c := e.client(t, "ada", e.agent(t, "buyer"), "")

	ask1 := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Approve the plan: 3 subtasks, sonnet and haiku?"))
	ask1.Metadata = map[string]any{ask.ApprovalKey: true}
	task := asTask(t)(c.SendMessage(ctx, &a2a.SendMessageRequest{Message: ask1}))

	q := f.next(n + 1)
	if q.Chat != adaChat || !strings.Contains(q.Text, "<b>@buyer</b> asks you") || !strings.Contains(q.Text, "Approve the plan") {
		t.Fatalf("question = %+v", q)
	}
	t.Logf("question: %q", q.Text)
	approve := button(t, q, "Approve")
	reject := button(t, q, "Reject")

	// Someone else pressing in the chat: nothing.
	cb := f.press(q, strangerTG, approve)
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	_, answered := f.callbacks[cb]
	f.mu.Unlock()
	if answered {
		t.Fatal("a stranger's press was answered")
	}

	// Approve asks again first.
	f.callback(f.press(q, adaTG, approve))
	waitFor(t, "the confirmation", func() bool { return strings.Contains(f.get(n+1).Text, "<b>Approve?</b>") })
	conf := f.get(n + 1)
	yes := button(t, conf, "Yes, approve")
	if st := waitState(t, c, task.ID, a2a.TaskStateInputRequired); st == nil {
		t.Fatal("approved before confirming")
	}
	if got := f.callback(f.press(conf, adaTG, yes)); got != "Sent." {
		t.Fatalf("confirm callback = %q", got)
	}
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if done.Status.Message.Metadata[relay.AnsweredByKey] != "ada" {
		t.Fatalf("answered by = %v", done.Status.Message.Metadata)
	}
	waitFor(t, "the closed message", func() bool { return strings.Contains(f.get(n+1).Text, "Approved</b> from Telegram") })
	if kb := f.get(n + 1).Kb; len(kb) != 0 {
		t.Fatalf("buttons left: %+v", kb)
	}
	// Replaying any of its buttons does nothing.
	for _, data := range []string{yes, approve, reject} {
		if got := f.callback(f.press(conf, adaTG, data)); !strings.Contains(got, "already answered") {
			t.Fatalf("replay callback = %q", got)
		}
	}
	// A made-up token does nothing.
	if got := f.callback(f.press(conf, adaTG, "AAAAAAAAAAAAAAAAAAAAAA")); !strings.Contains(got, "doesn't work") {
		t.Fatalf("forged callback = %q", got)
	}

	// Reject is one press.
	ask2 := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Approve 90,000 EUR?"))
	ask2.Metadata = map[string]any{ask.ApprovalKey: true}
	task2 := asTask(t)(c.SendMessage(ctx, &a2a.SendMessageRequest{Message: ask2}))
	q2 := f.next(n + 2)
	f.callback(f.press(q2, adaTG, button(t, q2, "Reject")))
	waitState(t, c, task2.ID, a2a.TaskStateRejected)

	// Answered in the inbox: the Telegram message says so and loses its buttons.
	ask3 := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Approve 12 mugs?"))
	ask3.Metadata = map[string]any{ask.ApprovalKey: true}
	task3 := asTask(t)(c.SendMessage(ctx, &a2a.SendMessageRequest{Message: ask3}))
	q3 := f.next(n + 3)
	ada.do(http.MethodPost, "/api/tasks/"+string(task3.ID)+"/answer", map[string]string{"text": "ok", "decision": "approve"}, http.StatusAccepted, nil)
	waitFor(t, "answered elsewhere", func() bool { return strings.Contains(f.get(n+3).Text, "Answered") && len(f.get(n+3).Kb) == 0 })
	if got := f.callback(f.press(q3, adaTG, button(t, q3, "Reject"))); !strings.Contains(got, "already answered") {
		t.Fatalf("press after answering elsewhere = %q", got)
	}
	waitState(t, c, task3.ID, a2a.TaskStateCompleted)

	// A plain question can't be answered from Telegram: it says to use the inbox.
	asTask(t)(c.SendMessage(ctx, text("What should the note say?")))
	plain := f.next(n + 4)
	if len(plain.Kb) != 0 || !strings.Contains(plain.Text, "Answer this one in the inbox") {
		t.Fatalf("plain question = %+v", plain)
	}
	// /waiting sends it again.
	f.say(adaChat, adaTG, "private", "/waiting")
	if got := f.next(n + 5); !strings.Contains(got.Text, "What should the note say?") {
		t.Fatalf("waiting = %q", got.Text)
	}
}

func TestTelegramForm(t *testing.T) {
	e, f, ada := telegramEnv(t)
	n := pairAda(t, f, ada)
	ctx := t.Context()
	c := e.client(t, "ada", e.agent(t, "lead"), "")

	qs := []ask.Question{
		{Header: "Auth", Question: "How do users sign in?", Options: []ask.Option{{Label: "OAuth"}, {Label: "Email link"}}},
		{Header: "Scope", Question: "Which surfaces?", MultiSelect: true, Options: []ask.Option{{Label: "API"}, {Label: "CLI"}, {Label: "Web"}}},
	}
	m := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("A few questions:\n\n"+ask.Text(qs)))
	m.Metadata = map[string]any{ask.MetaKey: qs}
	task := asTask(t)(c.SendMessage(ctx, &a2a.SendMessageRequest{Message: m}))

	q := f.next(n + 1)
	if got := f.callback(f.press(q, adaTG, button(t, q, "Send answers"))); !strings.Contains(got, "question 1") {
		t.Fatalf("early send = %q", got)
	}
	f.callback(f.press(q, adaTG, button(t, q, "1 · Email link")))
	f.callback(f.press(q, adaTG, button(t, q, "2 · API")))
	f.callback(f.press(q, adaTG, button(t, q, "2 · Web")))
	f.callback(f.press(q, adaTG, button(t, q, "2 · CLI")))
	f.callback(f.press(q, adaTG, button(t, q, "2 · CLI"))) // and off again
	waitFor(t, "the picks", func() bool {
		cur := f.get(n + 1)
		return slices.ContainsFunc(cur.Kb, func(r []telegram.Button) bool {
			return slices.ContainsFunc(r, func(b telegram.Button) bool { return b.Text == "✓ 2 · Web" })
		})
	})
	cur := f.get(n + 1)
	f.callback(f.press(cur, adaTG, button(t, cur, "Send answers")))
	done := waitState(t, c, task.ID, a2a.TaskStateCompleted)
	if got := relay.Text(done.Status.Message); got != "Auth: Email link\nScope: API, Web" {
		t.Fatalf("answer = %q", got)
	}
}

// reporter publishes a final report, then completes: a run worth a message.
func reporter() a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			progress := report.Report{Headline: "Add rate limiting", Status: "working", Progress: &report.Progress{Done: 1, Total: 2}, Notice: "S1 failed on haiku; escalated to sonnet"}
			if !yield(progress.Event(ec), nil) {
				return
			}
			final := report.Report{
				Headline: "Add rate limiting", Status: "landed", Progress: &report.Progress{Done: 2, Total: 2},
				Built: []report.Item{{Title: "S1 token bucket", Detail: "sonnet · +120 −4"}}, Stats: []report.Stat{{Label: "Wall time", Value: "14m"}},
				Outcome: "Merged into main.", Notice: "Landed on main",
			}
			if !yield(final.Event(ec), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("done"))), nil)
		}
	})
}

func TestTelegramRunReport(t *testing.T) {
	e, f, ada := telegramEnv(t)
	n := pairAda(t, f, ada)
	tok := e.agent(t, "lead")
	e.connect(t, "lead", tok, agentServer("lead", reporter()), 1)

	ada.do(http.MethodPost, "/api/send", map[string]any{"to": "lead", "text": "Add rate limiting"}, http.StatusOK, nil)
	notice := f.next(n + 1)
	if !strings.Contains(notice.Text, "🔔 S1 failed on haiku") || !strings.Contains(notice.Text, "1/2 done") {
		t.Fatalf("notice = %q", notice.Text)
	}
	card := f.next(n + 2)
	for _, want := range []string{"Run finished", "<b>Add rate limiting</b>", "landed · 2/2 done", "Merged into main.", "• S1 token bucket", "Wall time 14m", "/runs/"} {
		if !strings.Contains(card.Text, want) {
			t.Fatalf("card lacks %q:\n%s", want, card.Text)
		}
	}
	// The root task completing doesn't send the card twice.
	f.quiet(t, n+2, "a second card")

	// The run's report is on the Runs API too.
	var runs struct {
		Runs []struct {
			Report *report.Report `json:"report"`
		} `json:"runs"`
	}
	ada.do(http.MethodGet, "/api/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) == 0 || runs.Runs[0].Report == nil || runs.Runs[0].Report.Outcome != "Merged into main." {
		t.Fatalf("runs = %+v", runs)
	}

	// /status and /runs read it back.
	f.say(adaChat, adaTG, "private", "/runs")
	if got := f.next(n + 3); !strings.Contains(got.Text, "Add rate limiting") || !strings.Contains(got.Text, "done") {
		t.Fatalf("runs = %q", got.Text)
	}

	// Muted: nothing, and level "questions" sends no run cards.
	f.say(adaChat, adaTG, "private", "/mute 1h")
	if got := f.next(n + 4); !strings.Contains(got.Text, "Muted for 1h") {
		t.Fatalf("mute = %q", got.Text)
	}
	ada.do(http.MethodPost, "/api/send", map[string]any{"to": "lead", "text": "Again"}, http.StatusOK, nil)
	f.quiet(t, n+4, "muted")
}
