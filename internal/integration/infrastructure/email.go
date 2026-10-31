// Package infrastructure holds email sender adapters. SMTP and provider
// adapters arrive with the hardening phase; development uses the log and
// recording senders.
package infrastructure

import (
	"context"
	"log/slog"
	"sync"

	"app-maker/internal/integration/domain"
)

// LogSender writes emails to the log (fw dev).
type LogSender struct{ Log *slog.Logger }

// Send implements the EmailSender port.
func (s LogSender) Send(ctx context.Context, m domain.EmailMessage) error {
	s.Log.InfoContext(ctx, "email", "to", m.To, "subject", m.Subject, "delivery", m.Delivery, "body", m.Body)
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
