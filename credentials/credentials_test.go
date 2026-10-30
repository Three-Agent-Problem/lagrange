package credentials

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestChallengeRoundTrip(t *testing.T) {
	want := &Challenge{ID: "s1", Integration: "google-calendar", URL: "https://connect.example/?s=1", ExpiresAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	msg := a2a.NewMessage(a2a.MessageRoleAgent, NewChallengeParts(want, "I need your calendar.")...)

	// The message reaches clients and the console as JSON.
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded a2a.Message
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}

	got, ok := ChallengeFromMessage(&decoded)
	if !ok {
		t.Fatal("ChallengeFromMessage() found no challenge")
	}
	if got.ID != want.ID || got.URL != want.URL || got.Integration != want.Integration || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ChallengeFromMessage() = %+v, want %+v", got, want)
	}
	if text := decoded.Parts[0].Text(); !strings.Contains(text, want.URL) {
		t.Errorf("first part = %q, want the link for people", text)
	}
}

func TestChallengeFromMessageIgnoresOtherData(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewDataPart(map[string]any{"url": "https://example.com"}))
	if _, ok := ChallengeFromMessage(msg); ok {
		t.Error("ChallengeFromMessage() matched a data part without the challenge marker")
	}
}
