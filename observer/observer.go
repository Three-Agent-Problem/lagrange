// Package observer lets an operator watch every task of an a2a-go server without
// touching the agent's executor.
//
// a2a-go sends a push notification for each task event to every config that
// push.ConfigStore.List returns for that task. Store wraps the server's ConfigStore and
// appends one more config, the operator's subscriber, so the operator's console
// receives every event while client-registered webhooks keep working unchanged.
//
// The subscriber also shows up when a client lists the task's push configs. A runtime
// that owns the task store should deliver to its own console directly instead.
package observer

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
)

// SubscriberID is the push config ID used for the operator's subscriber.
const SubscriberID = "operator-observer"

// Store is a push.ConfigStore that adds the operator's subscriber to every task.
type Store struct {
	push.ConfigStore
	url   string
	token string
}

var _ push.ConfigStore = (*Store)(nil)

// Wrap returns inner with a subscriber that receives every task event at url.
// token is sent in the A2A-Notification-Token header so the receiver can authenticate it.
func Wrap(inner push.ConfigStore, url, token string) *Store {
	return &Store{ConfigStore: inner, url: url, token: token}
}

// List returns the configs stored for the task plus the operator's subscriber.
func (s *Store) List(ctx context.Context, taskID a2a.TaskID) ([]*a2a.PushConfig, error) {
	configs, err := s.ConfigStore.List(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return append(configs, &a2a.PushConfig{ID: SubscriberID, TaskID: taskID, URL: s.url, Token: s.token}), nil
}
