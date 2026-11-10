package delegate

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestTag(t *testing.T) {
	m := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))
	Tag(m, map[string]string{"empty": ""})
	if m.Metadata != nil {
		t.Fatalf("metadata = %v", m.Metadata)
	}
	Tag(m, map[string]string{"subtask": "S3"})
	Tag(m, map[string]string{"run": "r1"})
	if tags := m.Metadata[TagsKey].(map[string]any); tags["subtask"] != "S3" || tags["run"] != "r1" {
		t.Fatalf("tags = %v", tags)
	}
}
