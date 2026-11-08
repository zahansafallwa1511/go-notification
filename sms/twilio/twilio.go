// Package twilio is an [sms.Provider] for the Twilio Programmable Messaging
// API.
//
//	provider := twilio.New(accountSID, authToken)
//	ch := sms.NewChannel(provider, sms.WithFrom("+15550100"))
//
// To send through a Messaging Service instead of a fixed number, use
// [WithMessagingServiceSID]; the From address is then optional.
package twilio

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
	"github.com/zahansafallwa1511/go-notification/sms"
)

// DefaultBaseURL is the Twilio REST API endpoint.
const DefaultBaseURL = "https://api.twilio.com"

// Provider sends SMS through Twilio. It is safe for concurrent use.
type Provider struct {
	sid            string
	user           string
	token          string
	baseURL        string
	client         *http.Client
	serviceSID     string
	statusCallback string
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

// WithMessagingServiceSID sends through a Messaging Service. Twilio then picks
// the sender, so a From address is not required.
func WithMessagingServiceSID(sid string) Option {
	return func(p *Provider) { p.serviceSID = sid }
}

// WithStatusCallback asks Twilio to POST delivery status updates to u.
func WithStatusCallback(u string) Option {
	return func(p *Provider) { p.statusCallback = u }
}

// WithAPIKey authenticates with an API key SID and secret instead of the
// account's auth token. The account SID passed to [New] still names the
// account; its authToken argument is then ignored.
func WithAPIKey(keySID, secret string) Option {
	return func(p *Provider) { p.user, p.token = keySID, secret }
}

// New returns a Twilio provider for the account, authenticated with its auth
// token (or with an API key, see [WithAPIKey]).
func New(accountSID, authToken string, opts ...Option) *Provider {
	p := &Provider{sid: accountSID, user: accountSID, token: authToken, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// Send implements [sms.Provider].
func (p *Provider) Send(ctx context.Context, msg sms.SMS) error {
	if msg.To == "" {
		return notification.Permanent(errors.New("twilio: missing recipient"))
	}
	if msg.From == "" && p.serviceSID == "" {
		return notification.Permanent(errors.New("twilio: missing sender: set a From address or a messaging service SID"))
	}

	form := url.Values{}
	form.Set("To", msg.To)
	form.Set("Body", msg.Body)
	if p.serviceSID != "" {
		form.Set("MessagingServiceSid", p.serviceSID)
	}
	if msg.From != "" {
		form.Set("From", msg.From)
	}
	if p.statusCallback != "" {
		form.Set("StatusCallback", p.statusCallback)
	}

	header := http.Header{}
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.user+":"+p.token)))
	endpoint := p.baseURL + "/2010-04-01/Accounts/" + url.PathEscape(p.sid) + "/Messages.json"
	return httpx.Form(ctx, p.client, "twilio", endpoint, header, form, nil)
}
