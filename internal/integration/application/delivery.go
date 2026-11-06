// Package application implements the effect workers (spec 14.5):
// deliver_email and deliver_webhook jobs.
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"

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
	// Blob reads stored files for attachments; nil refuses attachments.
	Blob func(ctx context.Context, id string, max int64) ([]byte, string, error)
}

type emailArgs struct {
	To          string         `json:"to"`
	Template    string         `json:"template"`
	Subject     string         `json:"subject"`
	Vars        map[string]any `json:"vars"`
	Delivery    string         `json:"delivery"`
	Lang        string         `json:"lang"`
	Attachments []struct {
		Blob string `json:"blob"`
		Name string `json:"name"`
	} `json:"attachments"`
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
	// Per-language templates: <name>.<lang>, then <name>.<base language>.
	if d.Templates != nil && a.Lang != "" {
		base, _, _ := strings.Cut(a.Lang, "-")
		for _, l := range []string{a.Lang, base} {
			if d.Templates.Has(name + "." + l) {
				name += "." + l
				break
			}
		}
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
	m := domain.EmailMessage{To: a.To, Subject: subject, Body: body, Delivery: a.Delivery}
	left := int64(domain.MaxAttachmentBytes)
	for i, at := range a.Attachments {
		if d.Blob == nil {
			return fmt.Errorf("attachments are not configured")
		}
		data, ct, err := d.Blob(ctx, at.Blob, left)
		if err != nil {
			return fmt.Errorf("attachment %d: %w", i+1, err)
		}
		left -= int64(len(data))
		n := at.Name
		if n == "" {
			n = fmt.Sprintf("attachment-%d%s", i+1, extension(ct))
		}
		m.Attachments = append(m.Attachments, domain.Attachment{Name: n, ContentType: ct, Data: data})
	}
	return d.Email.Send(ctx, m)
}

func extension(contentType string) string {
	if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
		if contentType == "application/pdf" {
			return ".pdf"
		}
		return exts[0]
	}
	return ""
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
