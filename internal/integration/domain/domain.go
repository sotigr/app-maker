// Package domain is the Integration context's domain (spec 14.5): email
// messages, webhook calls, template rendering, and webhook signing. It
// knows nothing about features beyond the payload it is given.
package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// EmailMessage is one rendered email.
type EmailMessage struct {
	To       string
	Subject  string
	Body     string
	Delivery string // idempotency key: runID:node
}

// WebhookCall is one outbound webhook.
type WebhookCall struct {
	URL      string
	Payload  []byte
	Secret   string
	Delivery string
}

// Templates renders manifest templates (templates/<path>) with Go
// text/template. A first line "Subject: ..." sets the email subject.
type Templates struct {
	byName map[string]*template.Template
}

// NewTemplates parses templates keyed by path relative to templates/,
// without extension ("email/late-task").
func NewTemplates(sources map[string]string) (*Templates, error) {
	t := &Templates{byName: map[string]*template.Template{}}
	for name, src := range sources {
		tpl, err := template.New(name).Option("missingkey=zero").Parse(src)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		t.byName[name] = tpl
	}
	return t, nil
}

// Render renders a template.
func (t *Templates) Render(name string, vars map[string]any) (string, error) {
	tpl, ok := t.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown template %q", name)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Has reports whether a template exists.
func (t *Templates) Has(name string) bool {
	_, ok := t.byName[name]
	return ok
}

// SplitSubject separates a "Subject: ..." first line from the body.
func SplitSubject(rendered string) (subject, body string) {
	first, rest, ok := strings.Cut(rendered, "\n")
	if ok && strings.HasPrefix(first, "Subject:") {
		return strings.TrimSpace(strings.TrimPrefix(first, "Subject:")), strings.TrimLeft(rest, "\n")
	}
	return "", rendered
}

// Sign returns the X-Organon-Signature header: "t=<unix>,v1=<hex hmac>" over
// "<unix>.<payload>", so receivers can reject replays (spec 14.5).
func Sign(secret string, payload []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
