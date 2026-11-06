package infrastructure

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"organon/internal/integration/domain"
)

// SMTPSender sends email over SMTP (ADR 0022): STARTTLS when the server
// offers it, or implicit TLS; PLAIN authentication when Username is set.
type SMTPSender struct {
	Addr     string
	From     string
	Username string
	Password string
	TLS      bool
	// Timeout bounds one delivery (default 30 s).
	Timeout time.Duration
}

// Send implements the EmailSender port.
func (s SMTPSender) Send(ctx context.Context, m domain.EmailMessage) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return fmt.Errorf("smtp addr: %w", err)
	}
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	if s.TLS {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", s.Addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", s.Addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok && !s.TLS {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(Message(s.From, m, time.Now())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Message renders m as a MIME message: UTF-8 text, and multipart/mixed
// with base64 attachments when there are any. The delivery key becomes
// the Message-ID, so a retried delivery is the same message.
func Message(from string, m domain.EmailMessage, at time.Time) []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", from)
	h("To", m.To)
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", at.UTC().Format(time.RFC1123Z))
	id := m.Delivery
	if id == "" {
		id = random()
	}
	h("Message-ID", "<"+strings.ReplaceAll(id, ":", ".")+"@organon>")
	h("MIME-Version", "1.0")
	text := func() {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		wrap(&b, base64.StdEncoding.EncodeToString([]byte(m.Body)))
	}
	if len(m.Attachments) == 0 {
		text()
		return b.Bytes()
	}
	boundary := "organon-" + random()
	h("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n--" + boundary + "\r\n")
	text()
	for _, a := range m.Attachments {
		b.WriteString("--" + boundary + "\r\n")
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := mime.QEncoding.Encode("utf-8", a.Name)
		h("Content-Type", ct+`; name="`+name+`"`)
		h("Content-Disposition", `attachment; filename="`+name+`"`)
		h("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		wrap(&b, base64.StdEncoding.EncodeToString(a.Data))
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func wrap(b *bytes.Buffer, s string) {
	for len(s) > 76 {
		b.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	b.WriteString(s + "\r\n")
}

func random() string {
	r := make([]byte, 12)
	_, _ = rand.Read(r)
	return hex.EncodeToString(r)
}
