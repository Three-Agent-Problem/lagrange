// Package telegram is a notification channel on each person's own Telegram bot: it
// tells them what waits on them, lets them answer with buttons, and reports their runs.
//
// It is built to do little, on purpose. A Telegram account is easier to lose than a
// laptop, and the agents it talks to may run code, so:
//
//   - The only thing it can change is the answer to a task that waits on the bot's
//     owner, and only with buttons: approve or reject an approval, pick options of a
//     form (lagrange/ask). It never starts, cancels or messages anything, and typed
//     text is never sent anywhere. Deps is the whole of what it can reach.
//   - It listens to one private chat and one Telegram user, bound by a one-time pairing
//     code; everything else gets no answer at all.
//   - Buttons carry random tokens that name rows here, never tasks. Closing the message
//     is the claim that lets one answer through; the answer then goes through the same
//     checks as the inbox.
//   - Approving asks twice.
//   - The bot token only ever goes to api.telegram.org, and never appears in errors,
//     logs or the API.
//
// Bots long-poll (getUpdates), so a server needs no public URL. One replica polls each
// bot, under a Postgres advisory lock.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/ask"
	"github.com/three-agent-problem/lagrange/directory"
	"github.com/three-agent-problem/lagrange/notify"
	"github.com/three-agent-problem/lagrange/pg"
	"github.com/three-agent-problem/lagrange/report"
)

// Answer is an answer from a button: a decision ("approve" or "reject") or a form's
// text, never typed text.
type Answer struct {
	Decision string
	Text     string
}

// ErrGone means the task no longer waits for this answer (answered elsewhere,
// canceled, or not the person's to answer).
var ErrGone = errors.New("the task no longer waits for this answer")

// Waiting is a task waiting on the person.
type Waiting struct {
	TaskID, Asker, State string
	// For is who it waits on, when it isn't the person (it was escalated to them).
	For      string
	Question *a2a.Message
	Since    time.Time
}

// Deps is everything the bot can reach beyond its own tables: one way to answer and
// three read-only queries, each on behalf of one person. Nothing here starts, cancels
// or sends a message, and nothing should be added that does.
type Deps struct {
	// Answer answers a task waiting on handle, with the inbox's checks. It returns
	// ErrGone when the task doesn't wait for this answer anymore.
	Answer func(ctx context.Context, handle, taskID string, a Answer) error
	// Waiting lists the tasks waiting on handle.
	Waiting func(ctx context.Context, handle string) ([]Waiting, error)
	// Runs lists handle's latest runs, newest first.
	Runs func(ctx context.Context, handle string, limit int) ([]Run, error)
	// Run returns the run rooted at taskID, if handle sees it.
	Run func(ctx context.Context, handle, taskID string) (*Run, error)
}

// Service runs every person's bot.
type Service struct {
	DB        *pg.DB
	Directory *directory.Directory
	Activity  *activity.Log
	// PublicURL is the server's base URL, for links.
	PublicURL string
	Deps      Deps
	// HTTP calls the Bot API (default: a client with a 70s timeout).
	HTTP   *http.Client
	Logger *slog.Logger
	// PollTimeout is getUpdates' long-poll timeout (default 50s).
	PollTimeout time.Duration
	// Rescan is how often the bot list is read again (default 30s); Kick does it now.
	Rescan time.Duration
	// Tick is how often the activity log is followed (default 1s).
	Tick time.Duration
	// MinRun is how long a run without a report must have taken for its end to be
	// worth a message (default 1m); failed runs and runs with a report always are.
	MinRun time.Duration
	// APIBase replaces https://api.telegram.org. Tests only: it is never set from
	// configuration or input.
	APIBase string
	// Now is the clock (tests).
	Now func() time.Time

	mu      sync.Mutex
	pollers map[int64]*poller
	kick    chan struct{}
	rate    map[int64][]time.Time
}

type poller struct {
	token  string
	cancel context.CancelFunc
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) api(token string) *api {
	base := apiBase
	if s.APIBase != "" {
		base = s.APIBase
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 70 * time.Second}
	}
	return &api{base: base, token: token, http: client}
}

func (s *Service) inbox() string { return s.PublicURL + "/inbox" }

func (s *Service) runURL(id string) string { return s.PublicURL + "/runs/" + id }

// Run polls every bot and follows the activity log until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	kick := s.kick
	s.mu.Unlock()
	rescan, tick := s.Rescan, s.Tick
	if rescan <= 0 {
		rescan = 30 * time.Second
	}
	if tick <= 0 {
		tick = time.Second
	}
	r, t := time.NewTicker(rescan), time.NewTicker(tick)
	defer r.Stop()
	defer t.Stop()
	s.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			for id, p := range s.pollers {
				p.cancel()
				delete(s.pollers, id)
			}
			s.mu.Unlock()
			return
		case <-r.C:
			s.reconcile(ctx)
		case <-kick:
			s.reconcile(ctx)
		case <-t.C:
			if err := s.follow(ctx); err != nil && ctx.Err() == nil {
				s.log().Warn("telegram: following the activity log", "error", err)
			}
		}
	}
}

// Kick reads the bot list again now (after a bot is connected or removed).
func (s *Service) Kick() {
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	k := s.kick
	s.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// reconcile starts a poller for each bot that has none and stops pollers of bots that
// are gone or have a new token.
func (s *Service) reconcile(ctx context.Context) {
	bots, err := s.allBots(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("telegram: listing bots", "error", err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pollers == nil {
		s.pollers = map[int64]*poller{}
	}
	want := map[int64]*bot{}
	for _, b := range bots {
		want[b.UserID] = b
	}
	for id, p := range s.pollers {
		if b := want[id]; b == nil || b.Token != p.token {
			p.cancel()
			delete(s.pollers, id)
		}
	}
	for id, b := range want {
		if s.pollers[id] != nil {
			continue
		}
		pctx, cancel := context.WithCancel(ctx)
		p := &poller{token: b.Token, cancel: cancel}
		s.pollers[id] = p
		go func() {
			s.poll(pctx, b)
			s.mu.Lock()
			if s.pollers[id] == p {
				delete(s.pollers, id)
			}
			s.mu.Unlock()
			cancel()
		}()
	}
}

// poll long-polls one bot while this replica holds its lock.
func (s *Service) poll(ctx context.Context, b *bot) {
	conn, err := s.DB.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	key := "lagrange-telegram:" + strconv.FormatInt(b.BotID, 10)
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&locked); err != nil || !locked {
		return // another replica polls this bot
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key)
	}()
	a := s.api(b.Token)
	if err := a.deleteWebhook(ctx); err != nil && ctx.Err() == nil {
		s.log().Warn("telegram: removing the bot's webhook", "bot", b.Username, "error", err)
	}
	timeout := s.PollTimeout
	if timeout <= 0 {
		timeout = 50 * time.Second
	}
	backoff := time.Second
	for ctx.Err() == nil {
		cur, err := s.botOf(ctx, b.UserID)
		if err != nil || cur == nil || cur.Token != b.Token {
			return
		}
		updates, err := a.getUpdates(ctx, cur.Offset, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ae *apiError
			switch {
			case errors.Is(err, errUnauthorized):
				s.log().Warn("telegram: the bot's token stopped working", "bot", b.Username, "user", b.Handle)
				backoff = 5 * time.Minute
			case errors.As(err, &ae) && ae.Code == http.StatusConflict:
				s.log().Warn("telegram: something else polls this bot too (another app with the same token?)", "bot", b.Username)
			default:
				s.log().Warn("telegram: polling", "bot", b.Username, "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			cur, err := s.botOf(ctx, b.UserID)
			if err != nil || cur == nil || cur.Token != b.Token {
				return
			}
			s.handle(ctx, a, cur, u)
			if err := s.setOffset(ctx, b.UserID, u.UpdateID+1); err != nil {
				s.log().Warn("telegram: saving the offset", "error", err)
			}
		}
	}
}

// rateLimit is how many updates a minute one chat may send; the rest are dropped.
const rateLimit = 20

func (s *Service) allow(chat int64) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rate == nil {
		s.rate = map[int64][]time.Time{}
	}
	recent := s.rate[chat][:0]
	for _, t := range s.rate[chat] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= rateLimit {
		s.rate[chat] = recent
		return false
	}
	s.rate[chat] = append(recent, now)
	return true
}

// owner reports whether a chat and Telegram user are the ones the bot is paired with,
// in a private chat. Nothing from anyone else gets an answer.
func owner(b *bot, chat Chat, from *User) bool {
	return b.paired() && chat.Type == "private" && from != nil && !from.IsBot && chat.ID == b.ChatID && from.ID == b.TGUserID
}

func (s *Service) handle(ctx context.Context, a *api, b *bot, u Update) {
	switch {
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		if cq.Message == nil || !owner(b, cq.Message.Chat, &cq.From) {
			return
		}
		if !s.allow(b.ChatID) {
			return
		}
		s.press(ctx, a, b, cq)
	case u.Message != nil:
		s.message(ctx, a, b, u.Message)
	}
	// Edited messages and anything else: ignored.
}

func (s *Service) reply(ctx context.Context, a *api, chat int64, html string) {
	if _, err := a.send(ctx, chat, html, nil); err != nil && ctx.Err() == nil {
		s.log().Warn("telegram: replying", "error", err)
	}
}

// command splits "/mute@my_bot 2h" into "/mute" and "2h".
func command(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@")
	return strings.ToLower(cmd), strings.TrimSpace(arg)
}

func (s *Service) message(ctx context.Context, a *api, b *bot, m *Message) {
	if m.Chat.Type != "private" || m.From == nil || m.From.IsBot {
		return
	}
	cmd, arg := command(m.Text)
	// Pairing: the one thing a stranger's chat can do, and only with the code.
	if cmd == "/start" && arg != "" && b.pairing(s.now()) {
		if hashCode(arg) != b.PairHash {
			if err := s.wrongCode(ctx, b.UserID); err != nil {
				s.log().Warn("telegram: counting a wrong code", "error", err)
			}
			if s.allow(m.Chat.ID) {
				s.reply(ctx, a, m.Chat.ID, "That code didn't work. Make a new one in Lagrange (or Halo) and tap its link.")
			}
			return
		}
		if err := s.pair(ctx, b.UserID, m.Chat.ID, m.From.ID, m.From.Username); err != nil {
			s.log().Warn("telegram: pairing", "error", err)
			return
		}
		s.log().Info("telegram: bot paired", "bot", b.Username, "user", b.Handle)
		s.reply(ctx, a, m.Chat.ID, fmt.Sprintf("✓ Paired with <b>@%s</b> on %s.\n\nQuestions for you will arrive here, with buttons to answer.\n\n%s",
			esc(b.Handle), esc(s.PublicURL), helpText))
		return
	}
	if !owner(b, m.Chat, m.From) || !s.allow(b.ChatID) {
		return
	}
	switch cmd {
	case "/start", "/help":
		s.reply(ctx, a, b.ChatID, helpText)
	case "/status":
		s.reply(ctx, a, b.ChatID, s.status(ctx, b))
	case "/runs":
		s.reply(ctx, a, b.ChatID, s.runs(ctx, b))
	case "/waiting":
		s.resend(ctx, a, b)
	case "/mute":
		d := time.Hour
		if arg != "" {
			parsed, err := time.ParseDuration(arg)
			if err != nil || parsed <= 0 {
				s.reply(ctx, a, b.ChatID, "Say how long, like /mute 30m or /mute 2h.")
				return
			}
			d = min(parsed, 7*24*time.Hour)
		}
		until := s.now().Add(d)
		if err := s.setMute(ctx, b.UserID, &until); err != nil {
			s.reply(ctx, a, b.ChatID, "Muting failed.")
			return
		}
		s.reply(ctx, a, b.ChatID, fmt.Sprintf("🔕 Muted for %s. Questions still wait in your inbox; /waiting shows them. /unmute to turn it back on.", esc(Dur(d))))
	case "/unmute":
		if err := s.setMute(ctx, b.UserID, nil); err != nil {
			s.reply(ctx, a, b.ChatID, "Unmuting failed.")
			return
		}
		s.reply(ctx, a, b.ChatID, "🔔 Notifications are back on.")
	default:
		// Typed text, photos, files and unknown commands: never forwarded anywhere.
		s.reply(ctx, a, b.ChatID, esc(refusal))
	}
}

func (s *Service) status(ctx context.Context, b *bot) string {
	var out strings.Builder
	fmt.Fprintf(&out, "◉ <b>@%s</b> on %s", esc(b.Handle), esc(s.PublicURL))
	if b.muted(s.now()) {
		fmt.Fprintf(&out, "\n🔕 muted until %s", esc(b.MutedUntil.Format("15:04")))
	}
	if ws, err := s.Deps.Waiting(ctx, b.Handle); err == nil {
		if len(ws) == 0 {
			out.WriteString("\n\nNothing waits on you.")
		} else {
			fmt.Fprintf(&out, "\n\n✋ <b>%d waiting on you</b> · /waiting", len(ws))
			for i, w := range ws {
				if i == 5 {
					break
				}
				fmt.Fprintf(&out, "\n• @%s: %s", esc(w.Asker), esc(clip(notify.Render(w.Question), 80)))
			}
		}
	}
	runs, err := s.Deps.Runs(ctx, b.Handle, 20)
	if err != nil {
		return out.String()
	}
	var active []Run
	for _, r := range runs {
		if r.Status == "running" || r.Status == "waiting" {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		out.WriteString("\n\nNo runs going. /runs lists the last ones.")
		return out.String()
	}
	out.WriteString("\n\n<b>Going now</b>")
	for i, r := range active {
		if i == 8 {
			break
		}
		out.WriteString("\n" + runLine(r, s.now()))
	}
	return out.String()
}

func (s *Service) runs(ctx context.Context, b *bot) string {
	runs, err := s.Deps.Runs(ctx, b.Handle, 8)
	if err != nil {
		return "Couldn't read your runs."
	}
	if len(runs) == 0 {
		return "No runs yet."
	}
	var out strings.Builder
	out.WriteString("<b>Your last runs</b>")
	for _, r := range runs {
		out.WriteString("\n" + runLine(r, s.now()))
	}
	return out.String()
}

// resend sends what waits on the person again, as fresh messages with buttons.
func (s *Service) resend(ctx context.Context, a *api, b *bot) {
	ws, err := s.Deps.Waiting(ctx, b.Handle)
	if err != nil {
		s.reply(ctx, a, b.ChatID, "Couldn't read what waits on you.")
		return
	}
	if len(ws) == 0 {
		s.reply(ctx, a, b.ChatID, "Nothing waits on you. 🎉")
		return
	}
	for i, w := range ws {
		if i == 5 {
			s.reply(ctx, a, b.ChatID, fmt.Sprintf("… and %d more in your inbox: %s", len(ws)-5, esc(s.inbox())))
			break
		}
		nt := notify.Notice{To: b.Handle, Asker: w.Asker, TaskID: w.TaskID, State: w.State, Question: w.Question, For: w.For}
		if err := s.sendQuestion(ctx, a, b, nt); err != nil {
			s.log().Warn("telegram: sending a question", "error", err)
		}
	}
}

// kindOf decides how a question can be answered from Telegram: a form, an approval,
// or not at all (a notice that points to the inbox).
func kindOf(nt notify.Notice) (string, []ask.Question) {
	if nt.State == string(a2a.TaskStateAuthRequired) || nt.Question == nil {
		return kindNotice, nil
	}
	if qs := ask.Parse(nt.Question.Metadata); qs != nil {
		return kindForm, qs
	}
	if ask.IsApproval(nt.Question.Metadata) {
		return kindApproval, nil
	}
	if req, err := structuredinput.ParseRequest(nt.Question); err == nil && req != nil {
		if _, ok := ask.ApprovalField(req.Schema); ok {
			return kindApproval, nil
		}
	}
	return kindNotice, nil
}

// sendQuestion sends one waiting task, with the buttons that answer it.
func (s *Service) sendQuestion(ctx context.Context, a *api, b *bot, nt notify.Notice) error {
	kind, qs := kindOf(nt)
	body := questionBody(nt, s.inbox())
	if kind == kindNotice {
		body += "\n\n<i>Answer this one in the inbox or the terminal.</i>"
	}
	m := &message{UserID: b.UserID, ChatID: b.ChatID, TaskID: nt.TaskID, Kind: kind, Open: kind != kindNotice, Body: body, Questions: qs}
	if err := s.newMessage(ctx, m); err != nil {
		return err
	}
	bs := buttonsFor(kind, qs)
	if len(bs) > 0 {
		if err := s.addButtons(ctx, m.ID, bs); err != nil {
			s.dropMessage(ctx, m.ID)
			return err
		}
	}
	id, err := a.send(ctx, b.ChatID, body, keyboard(m, bs, false))
	if err != nil {
		s.dropMessage(ctx, m.ID)
		return err
	}
	return s.sentAs(ctx, m.ID, id)
}

// Notice sends a notice about a waiting task to the person's bot (notify.Channel).
func (s *Service) Notice(ctx context.Context, u *directory.User, nt notify.Notice) (bool, error) {
	b, err := s.botOf(ctx, u.ID)
	if err != nil || b == nil || !b.paired() || b.muted(s.now()) {
		return false, err
	}
	if err := s.sendQuestion(ctx, s.api(b.Token), b, nt); err != nil {
		return false, err
	}
	return true, nil
}

// Test sends a test message to the person's bot (notify.Channel).
func (s *Service) Test(ctx context.Context, u *directory.User) (bool, error) {
	b, err := s.botOf(ctx, u.ID)
	if err != nil || b == nil || !b.paired() {
		return false, err
	}
	_, err = s.api(b.Token).send(ctx, b.ChatID, "✓ <b>Lagrange notifications work.</b> Questions for you will arrive here.", nil)
	return err == nil, err
}

// press handles a button press from the bot's owner.
func (s *Service) press(ctx context.Context, a *api, b *bot, cq *CallbackQuery) {
	say := func(text string) {
		if err := a.answerCallback(ctx, cq.ID, text); err != nil && ctx.Err() == nil {
			s.log().Debug("telegram: answering a callback", "error", err)
		}
	}
	btn, m, err := s.pressed(ctx, cq.Data)
	if err != nil {
		say("Something went wrong.")
		return
	}
	if btn == nil || m.UserID != b.UserID || m.ChatID != b.ChatID || m.MessageID != cq.Message.MessageID {
		say("This button doesn't work anymore.")
		return
	}
	if !m.Open {
		say("This was already answered.")
		return
	}
	bs, err := s.buttonsOf(ctx, m.ID)
	if err != nil {
		say("Something went wrong.")
		return
	}
	switch btn.Action {
	case actApprove:
		body := m.Body + "\n\n<b>Approve?</b> The agent goes ahead as soon as you do."
		if err := a.edit(ctx, m.ChatID, m.MessageID, body, keyboard(m, bs, true)); err != nil {
			say("Something went wrong.")
			return
		}
		say("")
	case actBack:
		if err := a.edit(ctx, m.ChatID, m.MessageID, m.Body, keyboard(m, bs, false)); err != nil {
			say("Something went wrong.")
			return
		}
		say("")
	case actPick:
		m.Picks = toggle(m.Questions, m.Picks, btn.Question, btn.Option)
		if err := s.setPicks(ctx, m.ID, m.Picks); err != nil {
			say("Something went wrong.")
			return
		}
		_ = a.edit(ctx, m.ChatID, m.MessageID, m.Body, keyboard(m, bs, false))
		say("")
	case actConfirm:
		s.settle(ctx, a, b, m, Answer{Decision: "approve", Text: "Approved (from Telegram)."}, "✅ <b>Approved</b> from Telegram", say)
	case actReject:
		s.settle(ctx, a, b, m, Answer{Decision: "reject", Text: "Rejected (from Telegram)."}, "✕ <b>Rejected</b> from Telegram", say)
	case actSend:
		if n := unanswered(m.Questions, m.Picks); n > 0 {
			say(fmt.Sprintf("Pick an answer for question %d first.", n))
			return
		}
		text := ask.Format(m.Questions, answers(m.Questions, m.Picks))
		s.settle(ctx, a, b, m, Answer{Text: text}, "✅ <b>Answered</b> from Telegram:\n"+esc(text), say)
	default:
		say("This button doesn't work anymore.")
	}
}

// settle claims a message and sends its answer: only the press that closes the message
// answers, so a double tap or a second device can't answer twice.
func (s *Service) settle(ctx context.Context, a *api, b *bot, m *message, ans Answer, done string, say func(string)) {
	ok, err := s.claim(ctx, m.ID)
	if err != nil || !ok {
		say("This was already answered.")
		return
	}
	if err := s.Deps.Answer(ctx, b.Handle, m.TaskID, ans); err != nil {
		if errors.Is(err, ErrGone) {
			_ = a.edit(ctx, m.ChatID, m.MessageID, closed(m.Body, "<i>Already answered or canceled.</i>"), nil)
			say("This was already answered or canceled.")
			return
		}
		s.release(ctx, m.ID)
		s.log().Warn("telegram: answering", "task", m.TaskID, "error", err)
		say("Couldn't send the answer. Try again, or use the inbox.")
		return
	}
	_ = a.edit(ctx, m.ChatID, m.MessageID, closed(m.Body, done), nil)
	say("Sent.")
}

// follow reads new activity: closes messages about tasks that stopped waiting, and
// sends finished runs and report notices. One replica moves the cursor at a time.
func (s *Service) follow(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.DB.Pool, func(tx pgx.Tx) error {
		var cursor int64
		err := tx.QueryRow(ctx, `SELECT seq FROM telegram_cursor WHERE id = 1 FOR UPDATE SKIP LOCKED`).Scan(&cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		entries, err := s.Activity.Since(ctx, cursor, 500)
		if err != nil || len(entries) == 0 {
			return err
		}
		// A task stopped waiting when its last state here isn't a wait. Only messages
		// sent before that state close: a message about a newer wait stays open.
		last := map[string]*activity.Entry{}
		for _, e := range entries {
			if e.Kind == activity.KindTask && e.TaskID != "" {
				last[e.TaskID] = e
			}
		}
		var settled []*activity.Entry
		for _, e := range last {
			if !interrupted(e.State) {
				settled = append(settled, e)
			}
		}
		if len(settled) > 0 {
			msgs, err := s.closeOpen(ctx, tx, settled)
			if err != nil {
				return err
			}
			s.closeElsewhere(ctx, msgs, last)
		}
		for _, e := range entries {
			if e.ParentTaskID != "" || e.TaskID == "" || e.Sender == "" {
				continue
			}
			switch {
			case e.Kind == activity.KindTask && terminal(e.State):
				s.runEnded(ctx, e)
			case e.Kind == activity.KindArtifact:
				r, ok := report.FromMap(e.Data["report"])
				switch {
				case !ok:
				case r.Final():
					// The run is over even if its root task stays open (it may go on later).
					s.runEnded(ctx, e)
				case strings.TrimSpace(r.Notice) != "":
					s.reportNotice(ctx, e, &r)
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE telegram_cursor SET seq = $1 WHERE id = 1`, entries[len(entries)-1].Seq)
		return err
	})
}

func interrupted(state string) bool {
	return state == string(a2a.TaskStateInputRequired) || state == string(a2a.TaskStateAuthRequired)
}

func terminal(state string) bool { return a2a.TaskState(state).Terminal() }

// closeElsewhere marks messages whose task was answered (or ended) somewhere else.
func (s *Service) closeElsewhere(ctx context.Context, msgs []*message, last map[string]*activity.Entry) {
	bots := map[int64]*bot{}
	for _, m := range msgs {
		b, ok := bots[m.UserID]
		if !ok {
			b, _ = s.botOf(ctx, m.UserID)
			bots[m.UserID] = b
		}
		if b == nil || m.MessageID == 0 {
			continue
		}
		line := "<i>✓ Answered elsewhere.</i>"
		if e := last[m.TaskID]; e != nil {
			switch a2a.TaskState(e.State) {
			case a2a.TaskStateCanceled:
				line = "<i>Canceled.</i>"
			case a2a.TaskStateRejected:
				line = "<i>✕ Rejected elsewhere.</i>"
			case a2a.TaskStateFailed:
				line = "<i>The task failed.</i>"
			}
			if by, _ := e.Data["answeredBy"].(string); by != "" && e.State != string(a2a.TaskStateCanceled) {
				line = strings.Replace(line, "elsewhere", "by @"+esc(by)+" elsewhere", 1)
			}
		}
		if err := s.api(b.Token).edit(ctx, m.ChatID, m.MessageID, closed(m.Body, line), nil); err != nil && ctx.Err() == nil {
			s.log().Debug("telegram: closing a message", "error", err)
		}
	}
}

// milestoneBot returns the starter's bot, if it wants milestones now.
func (s *Service) milestoneBot(ctx context.Context, handle string) *bot {
	u, err := s.Directory.UserByHandle(ctx, handle)
	if err != nil {
		return nil // an agent, or gone
	}
	b, err := s.botOf(ctx, u.ID)
	if err != nil || b == nil || !b.paired() || b.Level != LevelMilestones || b.muted(s.now()) {
		return nil
	}
	return b
}

// runEnded sends a finished run to whoever started it, once: when its root task ends,
// or when it publishes a final report.
func (s *Service) runEnded(ctx context.Context, e *activity.Entry) {
	b := s.milestoneBot(ctx, e.Sender)
	if b == nil {
		return
	}
	r, err := s.Deps.Run(ctx, b.Handle, e.TaskID)
	if err != nil || r == nil || r.ID != e.TaskID {
		return
	}
	minRun := s.MinRun
	if minRun <= 0 {
		minRun = time.Minute
	}
	if r.Report == nil && r.Status != "failed" && r.UpdatedAt.Sub(r.StartedAt) < minRun {
		return // a quick exchange, not a run worth a message
	}
	body := runCard(*r, s.runURL(r.ID))
	m := &message{UserID: b.UserID, ChatID: b.ChatID, TaskID: r.ID, Kind: kindRun, Body: body}
	if ok, err := s.runMessage(ctx, m); err != nil || !ok {
		return
	}
	id, err := s.api(b.Token).send(ctx, b.ChatID, body, nil)
	if err != nil {
		s.log().Warn("telegram: sending a finished run", "error", err)
		s.dropMessage(ctx, m.ID)
		return
	}
	_ = s.sentAs(ctx, m.ID, id)
}

// reportNotice forwards a report's notice to whoever started the run.
func (s *Service) reportNotice(ctx context.Context, e *activity.Entry, r *report.Report) {
	b := s.milestoneBot(ctx, e.Sender)
	if b == nil {
		return
	}
	if _, err := s.api(b.Token).send(ctx, b.ChatID, noticeHTML(r, s.runURL(e.TaskID)), nil); err != nil && ctx.Err() == nil {
		s.log().Warn("telegram: sending a report notice", "error", err)
	}
}

// Status is a person's Telegram connection, as the API shows it. It never has the token.
type Status struct {
	Connected   bool   `json:"connected"`
	BotUsername string `json:"botUsername,omitempty"`
	Paired      bool   `json:"paired"`
	// ChatUsername is the Telegram user the bot is paired with.
	ChatUsername string `json:"chatUsername,omitempty"`
	// PairLink opens the bot with a fresh pairing code; only right after it is made.
	PairLink      string     `json:"pairLink,omitempty"`
	PairExpiresAt *time.Time `json:"pairExpiresAt,omitempty"`
	Level         string     `json:"level,omitempty"`
	MutedUntil    *time.Time `json:"mutedUntil,omitempty"`
}

func statusOf(b *bot, now time.Time) *Status {
	if b == nil {
		return &Status{}
	}
	st := &Status{Connected: true, BotUsername: b.Username, Paired: b.paired(), ChatUsername: b.TGUsername, Level: b.Level}
	if b.pairing(now) {
		st.PairExpiresAt = b.PairExpires
	}
	if b.muted(now) {
		st.MutedUntil = b.MutedUntil
	}
	return st
}

// Status returns u's connection.
func (s *Service) Status(ctx context.Context, u *directory.User) (*Status, error) {
	b, err := s.botOf(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return statusOf(b, s.now()), nil
}

// ErrBadToken means the token isn't a bot token, or Telegram doesn't know it.
var ErrBadToken = errors.New("that isn't a working bot token: copy the whole token @BotFather sent (digits, a colon, then letters)")

// Connect checks a bot token with Telegram, connects the bot to u (replacing any
// other), and starts pairing.
func (s *Service) Connect(ctx context.Context, u *directory.User, token string) (*Status, error) {
	token = strings.TrimSpace(token)
	if !ValidToken(token) {
		return nil, ErrBadToken
	}
	me, err := s.api(token).getMe(ctx)
	if errors.Is(err, errUnauthorized) {
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, fmt.Errorf("couldn't reach Telegram: %w", err)
	}
	if !me.IsBot || me.Username == "" || strconv.FormatInt(me.ID, 10) != botID(token) {
		return nil, ErrBadToken
	}
	if err := s.saveBot(ctx, u.ID, token, me.ID, me.Username); err != nil {
		if errors.Is(err, errBotTaken) {
			return nil, err
		}
		return nil, fmt.Errorf("saving the bot: %w", err)
	}
	s.log().Info("telegram: bot connected", "bot", me.Username, "user", u.Handle)
	return s.Pair(ctx, u)
}

// IsUserError reports whether err is the person's to fix (a bad token, a bot that is
// someone else's), not the server's.
func IsUserError(err error) bool { return errors.Is(err, ErrBadToken) || errors.Is(err, errBotTaken) }

// Pair makes a new pairing code for u's bot and returns the link that sends it.
func (s *Service) Pair(ctx context.Context, u *directory.User) (*Status, error) {
	code, _, err := s.newPairCode(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	b, err := s.botOf(ctx, u.ID)
	if err != nil || b == nil {
		return nil, errors.Join(err, errors.New("connect a bot first"))
	}
	s.Kick()
	st := statusOf(b, s.now())
	st.PairLink = "https://t.me/" + b.Username + "?start=" + code
	return st, nil
}

// SetLevel sets what u's bot sends besides questions.
func (s *Service) SetLevel(ctx context.Context, u *directory.User, level string) (*Status, error) {
	if err := s.setLevel(ctx, u.ID, level); err != nil {
		return nil, err
	}
	return s.Status(ctx, u)
}

// Disconnect removes u's bot. The bot itself stays theirs in Telegram.
func (s *Service) Disconnect(ctx context.Context, u *directory.User) error {
	if err := s.deleteBot(ctx, u.ID); err != nil {
		return err
	}
	s.Kick()
	return nil
}
