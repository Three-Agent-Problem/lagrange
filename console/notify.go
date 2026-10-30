package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notification tells people that a task is waiting on them.
type Notification struct {
	Title string
	Text  string
	Link  string
}

// Notifier delivers notifications to a channel such as Slack, email or WhatsApp.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// SlackWebhook posts notifications to a Slack incoming webhook. It only links to the
// inbox; answering inside Slack needs a Slack app with interactivity, which is a later step.
type SlackWebhook struct {
	URL    string
	Client *http.Client
}

// Notify implements Notifier.
func (s SlackWebhook) Notify(ctx context.Context, n Notification) error {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(map[string]string{
		"text": fmt.Sprintf("*%s*\n%s\n<%s|Open the inbox>", n.Title, n.Text, n.Link),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
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
