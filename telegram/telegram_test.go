package telegram

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/three-agent-problem/lagrange/ask"
	"github.com/three-agent-problem/lagrange/notify"
)

const token = "123456789:AAH-secret-secret-secret-0123456789ab"

func TestValidToken(t *testing.T) {
	for _, ok := range []string{token, "1234567890:" + strings.Repeat("x", 35)} {
		if !ValidToken(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "nope", "123:short", "abc:" + strings.Repeat("x", 35), token + "/../x", "123456789:AAH secret secret secret 0123456789ab"} {
		if ValidToken(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

// The token is part of every URL; no error may show it.
func TestErrorsHideTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"bad request for ` + r.URL.Path + `"}`))
	}))
	a := &api{base: srv.URL, token: token, http: srv.Client()}
	err := a.call(t.Context(), "sendMessage", map[string]any{}, nil)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("api error shows the token: %v", err)
	}
	srv.Close()
	err = a.call(t.Context(), "getMe", map[string]any{}, nil)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error shows the token: %v", err)
	}
}

func TestCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"/mute 2h":             {"/mute", "2h"},
		"/Status@ada_bot":      {"/status", ""},
		"  /start abc ":        {"/start", "abc"},
		"hello there":          {"", "hello there"},
		"/new build something": {"/new", "build something"},
	} {
		cmd, arg := command(in)
		if cmd != want[0] || arg != want[1] {
			t.Errorf("command(%q) = %q, %q", in, cmd, arg)
		}
	}
}

func TestToggle(t *testing.T) {
	qs := []ask.Question{
		{Question: "One?", Options: []ask.Option{{Label: "a"}, {Label: "b"}}},
		{Question: "Many?", MultiSelect: true, Options: []ask.Option{{Label: "x"}, {Label: "y"}, {Label: "z"}}},
	}
	var p [][]int
	p = toggle(qs, p, 0, 0)
	p = toggle(qs, p, 0, 1) // single choice: replaces
	p = toggle(qs, p, 1, 2)
	p = toggle(qs, p, 1, 0)
	p = toggle(qs, p, 1, 1)
	p = toggle(qs, p, 1, 1) // off again
	p = toggle(qs, p, 5, 0) // out of range: ignored
	p = toggle(qs, p, 0, 9)
	if !slices.Equal(p[0], []int{1}) || !slices.Equal(p[1], []int{0, 2}) {
		t.Fatalf("picks = %v", p)
	}
	if unanswered(qs, [][]int{{0}}) != 2 || unanswered(qs, p) != 0 {
		t.Fatal("unanswered")
	}
	if got := ask.Format(qs, answers(qs, p)); got != "One?: b\nMany?: x, z" {
		t.Fatalf("format = %q", got)
	}
}

func TestKindOf(t *testing.T) {
	msg := func(meta map[string]any, parts ...*a2a.Part) *a2a.Message {
		m := a2a.NewMessage(a2a.MessageRoleUser, append([]*a2a.Part{a2a.NewTextPart("q")}, parts...)...)
		m.Metadata = meta
		return m
	}
	qs := []ask.Question{{Question: "x?", Options: []ask.Option{{Label: "a"}, {Label: "b"}}}}
	schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"approved": {Type: "boolean"}, "note": {Type: "string"}}, Required: []string{"approved"}}
	part, err := structuredinput.NewSchemaPart("approval", schema)
	if err != nil {
		t.Fatal(err)
	}
	free := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"size": {Type: "string"}}, Required: []string{"size"}}
	freePart, _ := structuredinput.NewSchemaPart("size", free)
	for name, tc := range map[string]struct {
		nt   notify.Notice
		want string
	}{
		"form":        {notify.Notice{Question: msg(map[string]any{ask.MetaKey: qs})}, kindForm},
		"approval":    {notify.Notice{Question: msg(map[string]any{ask.ApprovalKey: true})}, kindApproval},
		"schema":      {notify.Notice{Question: msg(nil, part)}, kindApproval},
		"free schema": {notify.Notice{Question: msg(nil, freePart)}, kindNotice},
		"plain text":  {notify.Notice{Question: msg(nil)}, kindNotice},
		"auth":        {notify.Notice{State: string(a2a.TaskStateAuthRequired), Question: msg(map[string]any{ask.ApprovalKey: true})}, kindNotice},
		"nothing":     {notify.Notice{}, kindNotice},
	} {
		if got, _ := kindOf(tc.nt); got != tc.want {
			t.Errorf("%s: kind = %s, want %s", name, got, tc.want)
		}
	}
}

func TestQuestionBodyEscapes(t *testing.T) {
	nt := notify.Notice{Asker: "lead", Question: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("<b>x</b> & <a href=x>y</a>"))}
	body := questionBody(nt, "http://localhost/inbox")
	if strings.Contains(body, "<a href") || !strings.Contains(body, "&lt;b&gt;x&lt;/b&gt; &amp;") {
		t.Fatalf("not escaped: %s", body)
	}
	long := notify.Notice{Asker: "lead", Question: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(strings.Repeat("plan ", 2000)))}
	if n := len([]rune(questionBody(long, "http://x/inbox"))); n > 3600 {
		t.Fatalf("body is %d characters", n)
	}
}
