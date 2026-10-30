package observer

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
)

func TestListAddsSubscriber(t *testing.T) {
	ctx := context.Background()
	inner := push.NewInMemoryStore()
	store := Wrap(inner, "http://console.example/push?agent=x", "secret")

	if _, err := store.Save(ctx, "task-1", &a2a.PushConfig{URL: "https://client.example/webhook"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	tests := []struct {
		name   string
		taskID a2a.TaskID
		want   int
	}{
		{name: "task with a client webhook", taskID: "task-1", want: 2},
		{name: "task without webhooks", taskID: "task-2", want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configs, err := store.List(ctx, tc.taskID)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(configs) != tc.want {
				t.Fatalf("List() returned %d configs, want %d", len(configs), tc.want)
			}
			sub := configs[len(configs)-1]
			if sub.ID != SubscriberID || sub.TaskID != tc.taskID || sub.Token != "secret" || sub.URL != "http://console.example/push?agent=x" {
				t.Errorf("subscriber = %+v", sub)
			}
		})
	}

	// The subscriber is added on read only; the wrapped store is unchanged.
	if configs, _ := inner.List(ctx, "task-2"); len(configs) != 0 {
		t.Errorf("inner store has %d configs for task-2, want 0", len(configs))
	}
}
