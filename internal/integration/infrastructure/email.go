// Package infrastructure holds email sender adapters: SMTP, and the log
// and recording senders used in development and tests.
package infrastructure

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"organon/internal/integration/domain"
)

// LogSender writes emails to the log (organon dev).
type LogSender struct{ Log *slog.Logger }

// Send implements the EmailSender port.
func (s LogSender) Send(ctx context.Context, m domain.EmailMessage) error {
	var files []string
	for _, a := range m.Attachments {
		files = append(files, fmt.Sprintf("%s (%s, %d bytes)", a.Name, a.ContentType, len(a.Data)))
	}
	s.Log.InfoContext(ctx, "email", "to", m.To, "subject", m.Subject, "delivery", m.Delivery, "body", m.Body, "attachments", files)
	return nil
}

// Recorder keeps sent emails in memory and dedupes by delivery key, the
// way a provider with idempotency keys would (manifest tests, spec 12.6).
type Recorder struct {
	mu   sync.Mutex
	sent []domain.EmailMessage
	keys map[string]bool
}

// Send implements the EmailSender port.
func (r *Recorder) Send(_ context.Context, m domain.EmailMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = map[string]bool{}
	}
	if m.Delivery != "" && r.keys[m.Delivery] {
		return nil
	}
	r.keys[m.Delivery] = true
	r.sent = append(r.sent, m)
	return nil
}

// Sent returns the delivered messages.
func (r *Recorder) Sent() []domain.EmailMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.EmailMessage(nil), r.sent...)
}
