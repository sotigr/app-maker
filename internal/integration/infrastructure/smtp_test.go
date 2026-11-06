package infrastructure

import (
	"bufio"
	"context"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"testing"

	"organon/internal/integration/domain"
)

// fakeSMTP accepts one message and returns its DATA.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close(); _ = l.Close() }()
		r := bufio.NewReader(c)
		say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		say("220 fake")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				say("354 go")
				var b strings.Builder
				for {
					l, _ := r.ReadString('\n')
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				got <- b.String()
				say("250 ok")
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return l.Addr().String(), got
}

// ADR 0022: SMTP delivery sends UTF-8 text and attachments as MIME.
func TestSMTPSender(t *testing.T) {
	addr, got := fakeSMTP(t)
	s := SMTPSender{Addr: addr, From: "shop@example.com"}
	err := s.Send(context.Background(), domain.EmailMessage{To: "customer@example.com", Subject: "Τιμολόγιο INV-1",
		Body: "Επισυνάπτεται.", Delivery: "run:node",
		Attachments: []domain.Attachment{{Name: "INV-1.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4 test")}}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(<-got))
	if err != nil {
		t.Fatal(err)
	}
	if subj, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject")); subj != "Τιμολόγιο INV-1" {
		t.Fatalf("subject %q", subj)
	}
	if msg.Header.Get("Message-Id") != "<run.node@organon>" {
		t.Fatalf("message id %q", msg.Header.Get("Message-Id"))
	}
	_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		parts = append(parts, p.Header.Get("Content-Type")+"|"+p.FileName())
	}
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "text/plain") || !strings.HasSuffix(parts[1], "|INV-1.pdf") {
		t.Fatalf("parts: %v", parts)
	}
}
