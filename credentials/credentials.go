// Package credentials lets an agent obtain a user's access to a third-party API in the
// middle of a task, the case A2A models as TASK_STATE_AUTH_REQUIRED (spec 7.6), without
// the agent ever holding the user's raw token.
//
// The flow is:
//
//  1. Lookup: does the user already have a connection for the integration?
//  2. If not, Begin returns a Challenge with a link. The agent puts the task into
//     AUTH_REQUIRED and sends the link with NewChallengeParts.
//  3. Await blocks until the user completes the flow at the broker. Spec 7.6.1 lets the
//     agent continue on its own at that point, without a follow-up message.
//  4. The agent calls the provider's API through Grant.Call. The broker adds the
//     credential, so the token never travels through the chain of A2A agents (spec 7.6.3).
//
// Implementations: credentials/demo (a fake consent page, no accounts needed) and
// credentials/nango (Nango Connect, then API calls through the Nango proxy).
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

var (
	// ErrNoGrant means the user has no usable connection for the integration.
	ErrNoGrant = errors.New("credentials: no grant for this user and integration")
	// ErrDenied means the user declined the request.
	ErrDenied = errors.New("credentials: the user denied access")
	// ErrExpired means the challenge expired before the user completed it.
	ErrExpired = errors.New("credentials: the challenge expired")
)

// Broker obtains and uses user credentials on an agent's behalf.
type Broker interface {
	// Lookup returns the user's existing grant for the integration, or ErrNoGrant.
	Lookup(ctx context.Context, user, integration string) (Grant, error)
	// Begin starts a new authorization flow and returns the link to send to the user.
	Begin(ctx context.Context, user, integration string) (*Challenge, error)
	// Await blocks until the challenge is completed, denied, expired or ctx is done.
	Await(ctx context.Context, challenge *Challenge) (Grant, error)
}

// Grant is a connection the user approved.
type Grant interface {
	// ConnectionID identifies the connection at the broker.
	ConnectionID() string
	// Call sends a request to a path of the provider's API, for example
	// "/calendar/v3/calendars/primary/events", with body encoded as JSON. When out is
	// non-nil, the JSON response is decoded into it.
	Call(ctx context.Context, method, path string, body, out any) error
}

// Challenge is an authorization flow waiting for the user.
type Challenge struct {
	// ID is the broker's identifier for this flow.
	ID          string    `json:"id"`
	User        string    `json:"user"`
	Integration string    `json:"integration"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// MetadataKey marks the data part that carries a Challenge in an AUTH_REQUIRED status
// message. It lets a client or console show a "connect" button instead of parsing text.
const MetadataKey = "https://threeagents.dev/ext/credential-challenge/v1"

// NewChallengeParts returns the parts of an AUTH_REQUIRED status message: a sentence for
// humans and a data part with the challenge for machines.
func NewChallengeParts(c *Challenge, reason string) []*a2a.Part {
	text := a2a.NewTextPart(fmt.Sprintf("%s Open this link to connect %s: %s", reason, c.Integration, c.URL))
	data := a2a.NewDataPart(map[string]any{
		"id":          c.ID,
		"integration": c.Integration,
		"url":         c.URL,
		"expiresAt":   c.ExpiresAt.UTC().Format(time.RFC3339),
	})
	data.SetMeta(MetadataKey, true)
	return []*a2a.Part{text, data}
}

// ChallengeFromMessage finds a Challenge written by NewChallengeParts.
func ChallengeFromMessage(msg *a2a.Message) (*Challenge, bool) {
	if msg == nil {
		return nil, false
	}
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		if marked, _ := part.Meta()[MetadataKey].(bool); !marked {
			continue
		}
		raw, err := json.Marshal(part.Data())
		if err != nil {
			return nil, false
		}
		var c Challenge
		if err := json.Unmarshal(raw, &c); err != nil || c.URL == "" {
			return nil, false
		}
		return &c, true
	}
	return nil, false
}
