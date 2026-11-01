// Package application implements the effect workers (spec 14.5):
// deliver_email and deliver_webhook jobs.
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"organon/internal/integration/domain"
	"organon/internal/kernel"
)

// EmailSender sends rendered messages (SMTP, API providers, dev log).
type EmailSender interface {
	Send(ctx context.Context, m domain.EmailMessage) error
}

// HTTPDoer performs outbound requests through the egress component.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Delivery runs effect jobs.
type Delivery struct {
	Email     EmailSender
	HTTP      HTTPDoer
	Templates *domain.Templates
	Secrets   func(ref string) (string, bool)
	Clock     kernel.Clock
}

type emailArgs struct {
	To       string         `json:"to"`
	Template string         `json:"template"`
	Subject  string         `json:"subject"`
	Vars     map[string]any `json:"vars"`
	Delivery string         `json:"delivery"`
}

// HandleEmail renders templates/email/<template> and sends it.
func (d *Delivery) HandleEmail(ctx context.Context, raw json.RawMessage) error {
	var a emailArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	name := a.Template
	if d.Templates != nil && !d.Templates.Has(name) && d.Templates.Has("email/"+name) {
		name = "email/" + name
	}
	body := ""
	subject := a.Subject
	if d.Templates != nil && d.Templates.Has(name) {
		out, err := d.Templates.Render(name, a.Vars)
		if err != nil {
			return err
		}
		s, b := domain.SplitSubject(out)
		body = b
		if subject == "" {
			subject = s
		}
	} else {
		body = fmt.Sprintf("%v", a.Vars)
	}
	return d.Email.Send(ctx, domain.EmailMessage{To: a.To, Subject: subject, Body: body, Delivery: a.Delivery})
}

type webhookArgs struct {
	URL      string          `json:"url"`
	Payload  json.RawMessage `json:"payload"`
	Secret   string          `json:"secret"`
	Delivery string          `json:"delivery"`
}

// HandleWebhook POSTs the signed payload; 5xx and transport errors retry.
func (d *Delivery) HandleWebhook(ctx context.Context, raw json.RawMessage) error {
	var a webhookArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(a.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Organon-Delivery", a.Delivery)
	if a.Secret != "" && d.Secrets != nil {
		secret, ok := d.Secrets(a.Secret)
		if !ok {
			return fmt.Errorf("webhook secret %s is not configured", a.Secret)
		}
		req.Header.Set("X-Organon-Signature", domain.Sign(secret, a.Payload, d.Clock.Now()))
	}
	res, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook %s: HTTP %d", a.URL, res.StatusCode)
	}
	return nil
}
