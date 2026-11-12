package relay

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestTags(t *testing.T) {
	m := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))
	if Tags(m) != nil || Tags(nil) != nil {
		t.Fatal("tags without metadata")
	}
	m.Metadata = map[string]any{TagsKey: map[string]any{
		"subtask": "S3", "attempt": 2, "skip": nil, "": "no name",
		"long": strings.Repeat("x", 300), strings.Repeat("k", 65): "name too long",
	}}
	got := Tags(m)
	if got["subtask"] != "S3" || got["attempt"] != "2" || len([]rune(got["long"])) != 256 || len(got) != 3 {
		t.Fatalf("tags = %v", got)
	}
	many := map[string]any{}
	for i := range 40 {
		many[string(rune('a'+i%26))+strings.Repeat("z", i/26)] = "v"
	}
	m.Metadata[TagsKey] = many
	if n := len(Tags(m)); n != maxTags {
		t.Fatalf("%d tags kept, want %d", n, maxTags)
	}
}

func TestUsageOf(t *testing.T) {
	usage := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewDataPart(map[string]any{"usage": map[string]any{
		"used": float64(50000), "size": float64(200000), "cost": map[string]any{"amount": 0.42, "currency": "USD"}}}))
	u := usageOf(usage)
	if u == nil {
		t.Fatal("no usage")
	}
	if s := usageSummary(u); s != "usage: $0.42 · 25% of context" {
		t.Fatalf("summary = %q", s)
	}
	// A status message that says more than usage is an ordinary update.
	mixed := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("working"), usage.Parts[0])
	if usageOf(mixed) != nil || usageOf(nil) != nil {
		t.Fatal("usage found in a mixed message")
	}
	if s := usageSummary(map[string]any{"used": float64(1200), "cost": map[string]any{"amount": 3.0, "currency": "EUR"}}); s != "usage: 3.00 EUR · 1200 tokens" {
		t.Fatalf("summary = %q", s)
	}
	turn := map[string]any{"tokens": map[string]any{"input": float64(12), "output": float64(340), "cacheRead": float64(28400), "cacheWrite": float64(1500)}}
	if usageOf(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewDataPart(map[string]any{"usage": turn}))) == nil {
		t.Fatal("a tokens-only report isn't usage")
	}
	if s := usageSummary(turn); s != "usage: turn: 12 in · 340 out · 28k cache read · 1.5k cache write" {
		t.Fatalf("summary = %q", s)
	}
}

func TestCountText(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1k", 1540: "1.5k", 9999: "10k", 28400: "28k", 999_499: "999k", 1_200_000: "1.2M"} {
		if got := CountText(n); got != want {
			t.Errorf("CountText(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCapToolCall(t *testing.T) {
	tc := map[string]any{"id": "c", "output": strings.Repeat("o", 5000) + "END", "input": map[string]any{"content": strings.Repeat("i", 5000)}}
	got := capToolCall(tc)
	out, _ := got["output"].(string)
	in, _ := got["input"].(string)
	if !strings.HasSuffix(out, "END") || len(out) > maxToolField+len("…") || !strings.HasSuffix(in, "…") || len(in) > maxToolField+len("…") {
		t.Fatalf("output %d bytes, input %q…", len(out), in[:20])
	}
	if _, isMap := tc["input"].(map[string]any); !isMap {
		t.Fatal("capToolCall changed its argument")
	}
	small := map[string]any{"id": "c", "input": map[string]any{"path": "a.go"}}
	if _, isMap := capToolCall(small)["input"].(map[string]any); !isMap {
		t.Fatal("a small input was cut")
	}
}

func TestChunkText(t *testing.T) {
	parts := []*a2a.Part{a2a.NewTextPart("I've delegated S1 to ha"), a2a.NewTextPart("iku and I'm wai"), a2a.NewTextPart("ting.\n")}
	if got := ChunkText(parts); got != "I've delegated S1 to haiku and I'm waiting." {
		t.Fatalf("ChunkText = %q", got)
	}
	if got := ChunkText([]*a2a.Part{a2a.NewDataPart(map[string]any{"k": 1})}); got != "" {
		t.Fatalf("ChunkText of data = %q", got)
	}
}
