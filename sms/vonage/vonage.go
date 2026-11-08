// Package vonage is an [sms.Provider] for the Vonage (formerly Nexmo) SMS API.
//
//	provider := vonage.New(apiKey, apiSecret)
//	ch := sms.NewChannel(provider, sms.WithFrom("ACME"))
//
// Vonage answers 200 even when it rejects a message and reports the outcome
// per message in the body. The provider checks it: throttling and internal
// errors are returned as retryable errors, everything else as permanent ones.
package vonage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
	"github.com/zahansafallwa1511/go-notification/sms"
)

// DefaultBaseURL is the Vonage SMS API endpoint.
const DefaultBaseURL = "https://rest.nexmo.com"

// Provider sends SMS through Vonage. It is safe for concurrent use.
type Provider struct {
	key, secret string
	baseURL     string
	client      *http.Client
	callback    string
}

var _ sms.Provider = (*Provider)(nil)

// Option configures a [Provider].
type Option func(*Provider)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// WithBaseURL overrides the API endpoint, for tests or proxies.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// WithCallback asks Vonage to send delivery receipts to u.
func WithCallback(u string) Option {
	return func(p *Provider) { p.callback = u }
}

// New returns a Vonage provider authenticated with an API key and secret.
func New(apiKey, apiSecret string, opts ...Option) *Provider {
	p := &Provider{key: apiKey, secret: apiSecret, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// Error is a message rejected by Vonage.
type Error struct {
	// Status is Vonage's status code ("1" throttled, "3" invalid
	// parameters, "15" invalid sender, ...).
	Status string
	// Text is Vonage's error text.
	Text string
}

func (e *Error) Error() string {
	return fmt.Sprintf("vonage: status %s: %s", e.Status, e.Text)
}

// Temporary reports whether retrying may succeed: throttling ("1") and
// internal errors ("5").
func (e *Error) Temporary() bool { return e.Status == "1" || e.Status == "5" }

type request struct {
	APIKey       string `json:"api_key"`
	APISecret    string `json:"api_secret"`
	From         string `json:"from"`
	To           string `json:"to"`
	Text         string `json:"text"`
	Type         string `json:"type,omitempty"`
	Callback     string `json:"callback,omitempty"`
	ClientRef    string `json:"client-ref,omitempty"`
	StatusReport string `json:"status-report-req,omitempty"`
}

type response struct {
	Messages []struct {
		Status    string `json:"status"`
		ErrorText string `json:"error-text"`
	} `json:"messages"`
}

// Send implements [sms.Provider].
func (p *Provider) Send(ctx context.Context, msg sms.SMS) error {
	if msg.To == "" {
		return notification.Permanent(errors.New("vonage: missing recipient"))
	}
	if msg.From == "" {
		return notification.Permanent(errors.New("vonage: missing sender"))
	}
	req := request{
		APIKey:    p.key,
		APISecret: p.secret,
		From:      msg.From,
		To:        strings.TrimPrefix(msg.To, "+"),
		Text:      msg.Body,
		Callback:  p.callback,
		ClientRef: clientRef(msg.ID),
	}
	if msg.Unicode {
		req.Type = "unicode"
	}
	if p.callback != "" {
		req.StatusReport = "1"
	}

	var resp response
	if err := httpx.JSON(ctx, p.client, "vonage", http.MethodPost, p.baseURL+"/sms/json", nil, req, &resp); err != nil {
		return err
	}
	if len(resp.Messages) == 0 {
		return errors.New("vonage: response contained no message status")
	}
	for _, m := range resp.Messages {
		if m.Status != "0" {
			verr := &Error{Status: m.Status, Text: m.ErrorText}
			if verr.Temporary() {
				return verr
			}
			return notification.Permanent(verr)
		}
	}
	return nil
}

// clientRef trims the delivery id to Vonage's 100 character limit.
func clientRef(id string) string {
	if len(id) > 100 {
		return id[:100]
	}
	return id
}
