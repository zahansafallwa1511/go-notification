// Package resend is a [mail.Transport] for the Resend HTTP API
// (https://resend.com). It uses only the standard library.
//
//	t := resend.New(os.Getenv("RESEND_API_KEY"))
//	ch := mail.NewChannel(t, mail.From("Acme", "hello@acme.test"))
//
// The notification id the mail channel sets (mail.HeaderNotificationID) is
// sent as Resend's Idempotency-Key, so a retried delivery is not sent twice.
// Email tags become Resend tags with the value "true"; metadata entries become
// tags with their own value. Resend allows only ASCII letters, digits,
// underscores and dashes in tags, so other characters are replaced with "_".
package resend

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/zahansafallwa1511/go-notification/internal/httpx"
	"github.com/zahansafallwa1511/go-notification/mail"
)

// DefaultBaseURL is the Resend API endpoint.
const DefaultBaseURL = "https://api.resend.com"

// Transport sends email through Resend. It is safe for concurrent use.
type Transport struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

var _ mail.Transport = (*Transport)(nil)

// Option configures a [Transport].
type Option func(*Transport)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(c *http.Client) Option {
	return func(t *Transport) { t.client = c }
}

// WithBaseURL overrides the API endpoint, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(t *Transport) { t.baseURL = strings.TrimRight(u, "/") }
}

// New returns a transport authenticating with apiKey.
func New(apiKey string, opts ...Option) *Transport {
	t := &Transport{apiKey: apiKey, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}

type attachment struct {
	Filename    string `json:"filename"`
	Content     string `json:"content"`
	ContentType string `json:"content_type,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
}

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type request struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc,omitempty"`
	Bcc         []string          `json:"bcc,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []attachment      `json:"attachments,omitempty"`
	Tags        []tag             `json:"tags,omitempty"`
}

// Send implements [mail.Transport].
func (t *Transport) Send(ctx context.Context, e *mail.Email) error {
	req := request{
		From:    e.From.String(),
		To:      addrs(e.To),
		Cc:      addrs(e.Cc),
		Bcc:     addrs(e.Bcc),
		ReplyTo: addrs(e.ReplyTo),
		Subject: e.Subject,
		HTML:    e.HTML,
		Text:    e.Text,
	}
	header := http.Header{"Authorization": {"Bearer " + t.apiKey}}
	for k, v := range e.Headers {
		if strings.EqualFold(k, mail.HeaderNotificationID) {
			header.Set("Idempotency-Key", v)
		}
		if req.Headers == nil {
			req.Headers = map[string]string{}
		}
		req.Headers[k] = v
	}
	for _, a := range e.Attachments {
		att := attachment{
			Filename:    a.Filename,
			Content:     base64.StdEncoding.EncodeToString(a.Data),
			ContentType: a.MIMEType(),
		}
		if a.Inline {
			att.ContentID = a.CID()
		}
		req.Attachments = append(req.Attachments, att)
	}
	for _, name := range e.Tags {
		req.Tags = append(req.Tags, tag{Name: sanitize(name), Value: "true"})
	}
	for k, v := range e.Metadata {
		req.Tags = append(req.Tags, tag{Name: sanitize(k), Value: sanitize(v)})
	}

	return httpx.JSON(ctx, t.client, "resend", http.MethodPost, t.baseURL+"/emails", header, req, nil)
}

func addrs(list []mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}

// sanitize maps a string onto Resend's tag alphabet.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}
