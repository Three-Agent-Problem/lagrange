package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aext/structuredinput"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestRender(t *testing.T) {
	req := &structuredinput.Request{ID: "q1", Schema: &jsonschema.Schema{Type: "object"}}
	schema, err := req.Part()
	if err != nil {
		t.Fatal(err)
	}
	m := a2a.NewMessage(a2a.MessageRoleAgent,
		a2a.NewTextPart("Approve this order?"),
		a2a.NewDataPart(map[string]any{"total": 5416.5, "supplier": "Northwind", "lines": []any{"hoodie"}}),
		schema,
	)
	want := "Approve this order?\n\nlines: [\"hoodie\"]\nsupplier: Northwind\ntotal: 5416.5"
	if got := Render(m); got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

func TestParseSMTP(t *testing.T) {
	for _, tc := range []struct{ url, addr string }{
		{"smtp://mail.example.com", "mail.example.com:587"},
		{"smtps://u:p@mail.example.com", "mail.example.com:465"},
		{"smtp://localhost:1025", "localhost:1025"},
	} {
		s, err := ParseSMTP(tc.url, "lagrange@example.com")
		if err != nil || s.addr != tc.addr {
			t.Errorf("ParseSMTP(%q) = %+v, %v", tc.url, s, err)
		}
	}
	for _, bad := range []string{"http://mail.example.com", "::"} {
		if _, err := ParseSMTP(bad, "lagrange@example.com"); err == nil {
			t.Errorf("ParseSMTP(%q) accepted", bad)
		}
	}
	if _, err := ParseSMTP("smtp://mail.example.com", "not an address"); err == nil {
		t.Error("a bad sender was accepted")
	}
}

// fakeSMTP is just enough of an SMTP server to receive one message.
func fakeSMTP(t *testing.T) (addr string, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, w := bufio.NewReader(conn), conn
		say := func(s string) { _, _ = w.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					ch <- data.String()
					say("250 queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case cmd == "DATA":
				inData = true
				say("354 go on")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), ch
}

func TestSMTPSend(t *testing.T) {
	addr, got := fakeSMTP(t)
	s, err := ParseSMTP("smtp://"+addr, "Lagrange <lagrange@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "ada@example.com", "@buyer needs your answer", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	for _, want := range []string{"To: ada@example.com\r\n", "Subject: @buyer needs your answer\r\n", "\r\n\r\nline one\r\nline two"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
}
