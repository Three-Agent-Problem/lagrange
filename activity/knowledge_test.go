package activity

import (
	"testing"
	"time"
)

func TestKnowledgeOf(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	call := func(task, agent, id, kind string, input map[string]any, s int) *Entry {
		tc := map[string]any{"id": id, "kind": kind, "status": "completed"}
		if input != nil {
			tc["input"] = input
		}
		return &Entry{Kind: KindTask, TaskID: task, Recipient: agent, At: at(s), Data: map[string]any{"toolCall": tc}}
	}
	entries := []*Entry{
		// t1: haiku reads two files (the start entry has no input yet), then edits one.
		call("t1", "haiku", "c1", "read", nil, 1),
		call("t1", "haiku", "c1", "read", map[string]any{"file_path": "/wt/S1/store/store.go"}, 2),
		call("t1", "haiku", "c2", "read", map[string]any{"file_path": "/wt/S1/store/store_test.go"}, 3),
		call("t1", "haiku", "c3", "edit", map[string]any{"file_path": "/wt/S1/store/store.go"}, 4),
		call("t1", "haiku", "c4", "execute", map[string]any{"command": "go test ./..."}, 5),
		{Kind: KindUsage, TaskID: "t1", Recipient: "haiku", At: at(6), Data: map[string]any{"usage": map[string]any{"used": 30535.0}}},
		// t2: later, sonnet only runs commands.
		call("t2", "sonnet", "d1", "execute", map[string]any{"command": "grep -n func store/store.go"}, 20),
		{Kind: KindMessage, TaskID: "", At: at(30)},
		nil,
	}
	ks := KnowledgeOf(entries)
	if len(ks) != 2 || ks[0].TaskID != "t2" || ks[1].TaskID != "t1" {
		t.Fatalf("tasks = %+v, want t2 then t1", ks)
	}
	k := ks[1]
	if k.Agent != "haiku" || k.Context != 30535 || !k.LastActive.Equal(at(6)) {
		t.Fatalf("t1 = %+v", k)
	}
	if len(k.Files) != 2 {
		t.Fatalf("files = %+v", k.Files)
	}
	if f := k.Files[0]; f.Path != "/wt/S1/store/store.go" || !f.Read || !f.Changed || !f.At.Equal(at(4)) {
		t.Fatalf("store.go = %+v", f)
	}
	if f := k.Files[1]; !f.Read || f.Changed {
		t.Fatalf("store_test.go = %+v", f)
	}
	if len(ks[0].Files) != 0 {
		t.Fatalf("commands aren't file touches: %+v", ks[0].Files)
	}
	for path, want := range map[string]bool{"store.go": true, "store/store.go": true, "/store/store.go": true, "re.go": false, "": false, "api.go": false} {
		if _, ok := k.Touched(path); ok != want {
			t.Errorf("Touched(%q) = %v, want %v", path, ok, want)
		}
	}
}
