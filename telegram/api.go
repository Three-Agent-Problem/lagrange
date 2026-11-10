package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// apiBase is the only host a bot token is ever sent to. Tests point bots at a fake
// server through Service.base.
const apiBase = "https://api.telegram.org"

var tokenPattern = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{30,64}$`)

// ValidToken reports whether s looks like a bot token from @BotFather.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// botID is the bot's numeric ID, the part of the token before the colon.
func botID(token string) string {
	id, _, _ := strings.Cut(token, ":")
	return id
}

// api calls the Bot API with one token.
type api struct {
	base, token string
	http        *http.Client
}

// apiError is an error the Bot API answered with.
type apiError struct {
	Method      string
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// errUnauthorized means the token was revoked or is wrong.
var errUnauthorized = errors.New("telegram: the bot token is not valid (revoked in @BotFather?)")

// call posts params to method and decodes the result into out. Errors never contain
// the token (it is part of every URL).
func (a *api) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/bot"+a.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return a.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return a.redact(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return a.redact(err)
	}
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !env.OK {
		if env.ErrorCode == http.StatusUnauthorized || env.ErrorCode == http.StatusNotFound {
			return errUnauthorized
		}
		return &apiError{Method: method, Code: env.ErrorCode, Description: a.scrub(env.Description)}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (a *api) scrub(s string) string {
	if a.token == "" {
		return s
	}
	return strings.ReplaceAll(s, a.token, "<token>")
}

func (a *api) redact(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(a.scrub(err.Error()))
}

// User is a Telegram user (or bot).
type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is a Telegram message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is a button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one incoming update.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	EditedMessage *Message       `json:"edited_message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Button is an inline keyboard button.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Keyboard is rows of buttons.
type Keyboard [][]Button

type markup struct {
	InlineKeyboard Keyboard `json:"inline_keyboard"`
}

func (a *api) getMe(ctx context.Context) (*User, error) {
	var u User
	return &u, a.call(ctx, "getMe", map[string]any{}, &u)
}

func (a *api) deleteWebhook(ctx context.Context) error {
	return a.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (a *api) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var out []Update
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(timeout.Seconds()), "allowed_updates": []string{"message", "edited_message", "callback_query"},
	}, &out)
	return out, err
}

func (a *api) send(ctx context.Context, chat int64, html string, kb Keyboard) (int64, error) {
	params := map[string]any{"chat_id": chat, "text": html, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if kb != nil {
		params["reply_markup"] = markup{InlineKeyboard: kb}
	}
	var m Message
	if err := a.call(ctx, "sendMessage", params, &m); err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func (a *api) edit(ctx context.Context, chat, message int64, html string, kb Keyboard) error {
	params := map[string]any{"chat_id": chat, "message_id": message, "text": html, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if kb == nil {
		kb = Keyboard{}
	}
	params["reply_markup"] = markup{InlineKeyboard: kb}
	err := a.call(ctx, "editMessageText", params, nil)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

func (a *api) answerCallback(ctx context.Context, id, text string) error {
	return a.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}
