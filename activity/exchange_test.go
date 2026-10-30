package activity

import (
	"net/http"
	"strings"
	"testing"
)

func TestIDsIn(t *testing.T) {
	tests := []struct {
		name, body, task, context string
	}{
		{"json-rpc task", `{"jsonrpc":"2.0","result":{"task":{"id":"t1","contextId":"c1","status":{}}}}`, "t1", "c1"},
		{"rest task", `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`, "t1", "c1"},
		{"bare task", `{"id":"t1","contextId":"c1","status":{}}`, "t1", "c1"},
		{"message", `{"jsonrpc":"2.0","result":{"message":{"messageId":"m1","contextId":"c1","role":"ROLE_AGENT"}}}`, "", "c1"},
		{"push", `{"statusUpdate":{"taskId":"t1","contextId":"c1","status":{}}}`, "t1", "c1"},
		{"sse", "event: message\ndata: {\"result\":{\"statusUpdate\":{\"taskId\":\"t1\",\"contextId\":\"c1\"}}}\n\n", "t1", "c1"},
		{"request", `{"params":{"message":{"taskId":"t1","contextId":"c1"}}}`, "t1", "c1"},
		{"empty", "", "", ""},
		{"not json", "nothing answers", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, ctxID := idsIn(tt.body)
			if task != tt.task || ctxID != tt.context {
				t.Fatalf("idsIn = %q, %q, want %q, %q", task, ctxID, tt.task, tt.context)
			}
		})
	}
}

func TestSafeHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret")
	h.Set("Cookie", "lagrange_session=secret")
	h.Set("X-Api-Key", "secret")
	h.Set("Content-Type", "application/json")
	h.Set("A2A-Version", "1.0")
	got := safeHeaders(h)
	if len(got) != 2 || got["Content-Type"] != "application/json" || got["A2A-Version"] != "1.0" {
		t.Fatalf("safeHeaders = %v", got)
	}
}

func TestCaptureCaps(t *testing.T) {
	var c capture
	_, _ = c.Write([]byte(strings.Repeat("a", MaxBody-1)))
	_, _ = c.Write([]byte("bcd"))
	s, truncated := c.String()
	if len(s) != MaxBody || !truncated || !strings.HasSuffix(s, "ab") {
		t.Fatalf("len %d, truncated %v", len(s), truncated)
	}
}
