package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/three-agent-problem/lagrange/activity"
	"github.com/three-agent-problem/lagrange/ask"
)

// Levels: what a bot sends besides questions.
const (
	// LevelQuestions sends only what waits on the person.
	LevelQuestions = "questions"
	// LevelMilestones also sends report notices and finished runs.
	LevelMilestones = "milestones"
)

// bot is a person's bot, as stored.
type bot struct {
	UserID       int64
	Handle       string
	Token        string
	BotID        int64
	Username     string
	ChatID       int64 // 0 until paired
	TGUserID     int64
	TGUsername   string
	PairHash     string
	PairExpires  *time.Time
	PairAttempts int
	Level        string
	MutedUntil   *time.Time
	Offset       int64
}

func (b *bot) paired() bool { return b.ChatID != 0 && b.TGUserID != 0 }

func (b *bot) muted(now time.Time) bool { return b.MutedUntil != nil && now.Before(*b.MutedUntil) }

func (b *bot) pairing(now time.Time) bool {
	return b.PairHash != "" && b.PairExpires != nil && now.Before(*b.PairExpires)
}

const botColumns = `b.user_id, u.handle, b.token, b.bot_id, b.bot_username, coalesce(b.chat_id, 0), coalesce(b.tg_user_id, 0), b.tg_username,
	b.pair_code_hash, b.pair_expires_at, b.pair_attempts, b.level, b.muted_until, b.update_offset`

func scanBot(row pgx.Row) (*bot, error) {
	b := &bot{}
	err := row.Scan(&b.UserID, &b.Handle, &b.Token, &b.BotID, &b.Username, &b.ChatID, &b.TGUserID, &b.TGUsername,
		&b.PairHash, &b.PairExpires, &b.PairAttempts, &b.Level, &b.MutedUntil, &b.Offset)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// botOf returns a person's bot, or nil.
func (s *Service) botOf(ctx context.Context, userID int64) (*bot, error) {
	return scanBot(s.DB.Pool.QueryRow(ctx, `SELECT `+botColumns+` FROM telegram_bots b JOIN users u ON u.id = b.user_id WHERE b.user_id = $1`, userID))
}

func (s *Service) allBots(ctx context.Context) ([]*bot, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+botColumns+` FROM telegram_bots b JOIN users u ON u.id = b.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// errBotTaken means the bot is connected to someone else's account.
var errBotTaken = errors.New("this bot is already connected to another account; create a new bot for yourself with @BotFather")

// saveBot connects a bot to a person, replacing their old one; pairing starts over.
func (s *Service) saveBot(ctx context.Context, userID int64, token string, id int64, username string) error {
	_, err := s.DB.Pool.Exec(ctx, `INSERT INTO telegram_bots (user_id, token, bot_id, bot_username) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET token = $2, bot_id = $3, bot_username = $4, chat_id = NULL, tg_user_id = NULL,
			tg_username = '', pair_code_hash = '', pair_expires_at = NULL, pair_attempts = 0, update_offset = 0, updated_at = now()`,
		userID, token, id, username)
	if err != nil && strings.Contains(err.Error(), "telegram_bots_bot_id_key") {
		return errBotTaken
	}
	return err
}

func (s *Service) deleteBot(ctx context.Context, userID int64) error {
	_, err := s.DB.Pool.Exec(ctx, `DELETE FROM telegram_bots WHERE user_id = $1`, userID)
	return err
}

// PairTTL is how long a pairing code works.
const PairTTL = 10 * time.Minute

// maxPairAttempts is how many wrong codes a bot takes before its code stops working.
const maxPairAttempts = 5

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// newPairCode stores a fresh pairing code for a person's bot and returns it.
func (s *Service) newPairCode(ctx context.Context, userID int64) (string, time.Time, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	expires := s.now().Add(PairTTL)
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET pair_code_hash = $2, pair_expires_at = $3, pair_attempts = 0, updated_at = now() WHERE user_id = $1`,
		userID, hashCode(code), expires)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("connect a bot first")
	}
	return code, expires, err
}

// pair binds a bot to the chat and Telegram user that sent the right code, and uses the
// code up.
func (s *Service) pair(ctx context.Context, userID, chat, tgUser int64, tgUsername string) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET chat_id = $2, tg_user_id = $3, tg_username = $4, pair_code_hash = '',
		pair_expires_at = NULL, pair_attempts = 0, updated_at = now() WHERE user_id = $1`, userID, chat, tgUser, tgUsername)
	return err
}

// wrongCode counts a wrong pairing code; the last allowed one kills the code.
func (s *Service) wrongCode(ctx context.Context, userID int64) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET pair_attempts = pair_attempts + 1,
		pair_code_hash = CASE WHEN pair_attempts + 1 >= $2 THEN '' ELSE pair_code_hash END WHERE user_id = $1`, userID, maxPairAttempts)
	return err
}

func (s *Service) setOffset(ctx context.Context, userID, offset int64) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET update_offset = $2 WHERE user_id = $1 AND update_offset < $2`, userID, offset)
	return err
}

func (s *Service) setLevel(ctx context.Context, userID int64, level string) error {
	if level != LevelQuestions && level != LevelMilestones {
		return errors.New("level is questions or milestones")
	}
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET level = $2, updated_at = now() WHERE user_id = $1`, userID, level)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("connect a bot first")
	}
	return err
}

func (s *Service) setMute(ctx context.Context, userID int64, until *time.Time) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_bots SET muted_until = $2 WHERE user_id = $1`, userID, until)
	return err
}

// message is a message a bot sent, as stored.
type message struct {
	ID        int64
	UserID    int64
	ChatID    int64
	MessageID int64
	TaskID    string
	Kind      string
	Open      bool
	Body      string
	Questions []ask.Question
	Picks     [][]int
}

// Message kinds.
const (
	kindApproval = "approval"
	kindForm     = "form"
	kindNotice   = "notice"
	kindRun      = "run"
	kindInfo     = "info"
)

const messageColumns = `m.id, m.user_id, m.chat_id, m.message_id, m.task_id, m.kind, m.open, m.body, m.questions, m.picks`

func scanMessage(row pgx.Row) (*message, error) {
	m := &message{}
	var qs, picks []byte
	err := row.Scan(&m.ID, &m.UserID, &m.ChatID, &m.MessageID, &m.TaskID, &m.Kind, &m.Open, &m.Body, &qs, &picks)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(qs, &m.Questions)
	_ = json.Unmarshal(picks, &m.Picks)
	return m, nil
}

// newMessage stores a message before it is sent (its buttons need its ID).
func (s *Service) newMessage(ctx context.Context, m *message) error {
	qs, _ := json.Marshal(m.Questions)
	if m.Questions == nil {
		qs = []byte("[]")
	}
	picks, _ := json.Marshal(m.Picks)
	if m.Picks == nil {
		picks = []byte("[]")
	}
	return s.DB.Pool.QueryRow(ctx, `INSERT INTO telegram_messages (user_id, chat_id, message_id, task_id, kind, open, body, questions, picks)
		VALUES ($1, $2, 0, $3, $4, $5, $6, $7, $8) RETURNING id`, m.UserID, m.ChatID, m.TaskID, m.Kind, m.Open, m.Body, qs, picks).Scan(&m.ID)
}

// runMessage reserves the one finished-run message per person and run; false means it
// was sent already.
func (s *Service) runMessage(ctx context.Context, m *message) (bool, error) {
	err := s.DB.Pool.QueryRow(ctx, `INSERT INTO telegram_messages (user_id, chat_id, message_id, task_id, kind, body)
		VALUES ($1, $2, 0, $3, 'run', $4) ON CONFLICT (user_id, task_id) WHERE kind = 'run' DO NOTHING RETURNING id`,
		m.UserID, m.ChatID, m.TaskID, m.Body).Scan(&m.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) sentAs(ctx context.Context, id, messageID int64) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_messages SET message_id = $2 WHERE id = $1`, id, messageID)
	return err
}

func (s *Service) dropMessage(ctx context.Context, id int64) {
	_, _ = s.DB.Pool.Exec(context.WithoutCancel(ctx), `DELETE FROM telegram_messages WHERE id = $1`, id)
}

// claim closes an open message and reports whether this call closed it: the one way
// an answer from Telegram goes through.
func (s *Service) claim(ctx context.Context, id int64) (bool, error) {
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_messages SET open = false WHERE id = $1 AND open`, id)
	return err == nil && tag.RowsAffected() == 1, err
}

// release opens a claimed message again, after its answer failed.
func (s *Service) release(ctx context.Context, id int64) {
	_, _ = s.DB.Pool.Exec(context.WithoutCancel(ctx), `UPDATE telegram_messages SET open = true WHERE id = $1`, id)
}

func (s *Service) setPicks(ctx context.Context, id int64, picks [][]int) error {
	raw, _ := json.Marshal(picks)
	_, err := s.DB.Pool.Exec(ctx, `UPDATE telegram_messages SET picks = $2 WHERE id = $1 AND open`, id, raw)
	return err
}

// closeOpen closes the open messages about tasks that stopped waiting (each entry is a
// task's state that isn't a wait) sent before that state, and returns them.
func (s *Service) closeOpen(ctx context.Context, q pgx.Tx, settled []*activity.Entry) ([]*message, error) {
	var out []*message
	for _, e := range settled {
		rows, err := q.Query(ctx, `UPDATE telegram_messages m SET open = false WHERE m.open AND m.task_id = $1 AND m.created_at < $2 RETURNING `+messageColumns, e.TaskID, e.At)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// button is a button's row.
type button struct {
	Token    string
	Message  int64
	Action   string
	Question int
	Option   int
}

// Button actions.
const (
	actApprove = "approve" // show the confirmation
	actConfirm = "confirm" // approve (claims)
	actReject  = "reject"  // reject (claims)
	actBack    = "back"    // leave the confirmation
	actPick    = "pick"    // toggle an option of a form
	actSend    = "send"    // send the form (claims)
)

// ButtonTTL is how long buttons work.
const ButtonTTL = 7 * 24 * time.Hour

func newToken() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// addButtons stores the buttons of a message and returns them with their tokens.
func (s *Service) addButtons(ctx context.Context, messageID int64, bs []*button) error {
	expires := s.now().Add(ButtonTTL)
	batch := &pgx.Batch{}
	for _, b := range bs {
		b.Token, b.Message = newToken(), messageID
		batch.Queue(`INSERT INTO telegram_buttons (token, message, action, question, option, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			b.Token, b.Message, b.Action, b.Question, b.Option, expires)
	}
	return s.DB.Pool.SendBatch(ctx, batch).Close()
}

func (s *Service) buttonsOf(ctx context.Context, messageID int64) ([]*button, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT token, message, action, question, option FROM telegram_buttons WHERE message = $1`, messageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*button, error) {
		b := &button{}
		return b, r.Scan(&b.Token, &b.Message, &b.Action, &b.Question, &b.Option)
	})
}

// pressed returns the button with token and its message, or nils when the token is
// unknown or expired.
func (s *Service) pressed(ctx context.Context, token string) (*button, *message, error) {
	b, m := &button{}, &message{}
	var qs, picks []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT `+messageColumns+`, b.token, b.message, b.action, b.question, b.option
		FROM telegram_buttons b JOIN telegram_messages m ON m.id = b.message WHERE b.token = $1 AND b.expires_at > now()`, token).
		Scan(&m.ID, &m.UserID, &m.ChatID, &m.MessageID, &m.TaskID, &m.Kind, &m.Open, &m.Body, &qs, &picks,
			&b.Token, &b.Message, &b.Action, &b.Question, &b.Option)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal(qs, &m.Questions)
	_ = json.Unmarshal(picks, &m.Picks)
	return b, m, nil
}
