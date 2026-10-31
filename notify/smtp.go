package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// SMTP sends email through an SMTP server.
type SMTP struct {
	addr, host string
	// implicitTLS is smtps:// (port 465); smtp:// upgrades with STARTTLS when offered.
	implicitTLS bool
	auth        smtp.Auth
	from        string
}

// ParseSMTP configures SMTP from a URL such as smtp://user:pass@smtp.example.com:587 or
// smtps://user:pass@smtp.example.com:465, sending as from. Credentials are optional.
func ParseSMTP(rawURL, from string) (*SMTP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("notify: SMTP URL: %w", err)
	}
	if u.Scheme != "smtp" && u.Scheme != "smtps" {
		return nil, errors.New("notify: the SMTP URL must start with smtp:// or smtps://")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return nil, fmt.Errorf("notify: the sender address %q: %w", from, err)
	}
	s := &SMTP{host: u.Hostname(), implicitTLS: u.Scheme == "smtps", from: from}
	port := u.Port()
	if port == "" {
		port = map[bool]string{true: "465", false: "587"}[s.implicitTLS]
	}
	s.addr = net.JoinHostPort(s.host, port)
	if u.User != nil {
		pass, _ := u.User.Password()
		// PlainAuth refuses to send the password without TLS, except to localhost.
		s.auth = smtp.PlainAuth("", u.User.Username(), pass, s.host)
	}
	return s, nil
}

// Send implements Email.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if s.implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: s.host}}).DialContext(ctx, "tcp", s.addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", s.addr)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && !s.implicitTLS {
		if err := c.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
			return err
		}
	}
	if s.auth != nil {
		if err := c.Auth(s.auth); err != nil {
			return err
		}
	}
	if err := c.Mail(s.from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(compose(s.from, to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func compose(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, to, mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}
