// Package slack delivers notifications to Slack, with Block Kit layouts.
//
// It supports both ways of posting to Slack, chosen per recipient by the
// route the notifiable returns for "slack":
//
//   - An incoming webhook URL ("https://hooks.slack.com/services/...") posts
//     to the channel the webhook was created for. No token is needed.
//   - A channel id or name ("C0123456789", "#alerts") posts with the Web API
//     method chat.postMessage, using the bot token given to [WithToken].
//
// Setting up the channel:
//
//	notifier := notification.New(
//		notification.WithChannel("slack", slack.NewChannel(slack.WithToken(os.Getenv("SLACK_BOT_TOKEN")))),
//	)
//
// and supporting it in a notification:
//
//	func (n InvoicePaid) ToSlack(notification.Notifiable) (*slack.Message, error) {
//		return slack.NewMessage("Invoice paid").
//			Header("Invoice paid").
//			Section(fmt.Sprintf("*#%d* was paid by %s", n.ID, n.Customer)).
//			Actions(slack.Button("View invoice", n.URL).Primary()), nil
//	}
package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

// DefaultBaseURL is the Slack Web API endpoint.
const DefaultBaseURL = "https://slack.com/api"

// Notification is implemented by notifications that can be sent to Slack.
type Notification interface {
	ToSlack(to notification.Notifiable) (*Message, error)
}

// Message is a Slack message, built by a notification.
type Message struct {
	// Text is the message text. When Blocks are set it is the fallback shown
	// in notifications and by clients that cannot render blocks.
	Text string `json:"text,omitempty"`
	// Blocks is the Block Kit layout.
	Blocks []Block `json:"blocks,omitempty"`
	// Channel overrides the channel when posting through the Web API.
	Channel string `json:"channel,omitempty"`
	// Username overrides the bot's display name, where the app allows it.
	Username string `json:"username,omitempty"`
	// IconEmoji overrides the bot's icon with an emoji (":rocket:").
	IconEmoji string `json:"icon_emoji,omitempty"`
	// IconURL overrides the bot's icon with an image.
	IconURL string `json:"icon_url,omitempty"`
	// ThreadTS posts the message as a reply in the thread of that message.
	ThreadTS string `json:"thread_ts,omitempty"`
	// UnfurlLinks controls link previews; nil leaves Slack's default.
	UnfurlLinks *bool `json:"unfurl_links,omitempty"`
	// UnfurlMedia controls media previews; nil leaves Slack's default.
	UnfurlMedia *bool `json:"unfurl_media,omitempty"`
}

// NewMessage returns a message with the given text.
func NewMessage(text string) *Message {
	return &Message{Text: text}
}

// Block appends blocks and returns m.
func (m *Message) Block(blocks ...Block) *Message {
	m.Blocks = append(m.Blocks, blocks...)
	return m
}

// Header appends a header block and returns m.
func (m *Message) Header(text string) *Message {
	return m.Block(HeaderBlock{Text: Plain(text)})
}

// Section appends a section block with mrkdwn text and returns m.
func (m *Message) Section(text string) *Message {
	t := Markdown(text)
	return m.Block(SectionBlock{Text: &t})
}

// Fields appends a section block showing fields in two columns, each a
// mrkdwn string, and returns m.
func (m *Message) Fields(fields ...string) *Message {
	b := SectionBlock{}
	for _, f := range fields {
		b.Fields = append(b.Fields, Markdown(f))
	}
	return m.Block(b)
}

// Divider appends a divider and returns m.
func (m *Message) Divider() *Message {
	return m.Block(DividerBlock{})
}

// Context appends a context block of mrkdwn strings and returns m.
func (m *Message) Context(texts ...string) *Message {
	b := ContextBlock{}
	for _, s := range texts {
		b.Elements = append(b.Elements, Markdown(s))
	}
	return m.Block(b)
}

// Image appends an image block and returns m.
func (m *Message) Image(url, alt string) *Message {
	return m.Block(ImageBlock{URL: url, Alt: alt})
}

// Actions appends an actions block with the given buttons and returns m.
func (m *Message) Actions(buttons ...*ButtonElement) *Message {
	b := ActionsBlock{}
	for _, btn := range buttons {
		if btn != nil {
			b.Elements = append(b.Elements, *btn)
		}
	}
	return m.Block(b)
}

// To overrides the channel and returns m.
func (m *Message) To(channel string) *Message {
	m.Channel = channel
	return m
}

// As overrides the bot's display name and returns m.
func (m *Message) As(username string) *Message {
	m.Username = username
	return m
}

// Emoji sets an emoji icon and returns m.
func (m *Message) Emoji(emoji string) *Message {
	m.IconEmoji = emoji
	return m
}

// Icon sets an image icon and returns m.
func (m *Message) Icon(url string) *Message {
	m.IconURL = url
	return m
}

// InThread posts the message as a reply to the message with timestamp ts
// and returns m.
func (m *Message) InThread(ts string) *Message {
	m.ThreadTS = ts
	return m
}

// Unfurl turns link and media previews on or off and returns m.
func (m *Message) Unfurl(on bool) *Message {
	m.UnfurlLinks, m.UnfurlMedia = &on, &on
	return m
}

// APIError is an error reported by the Slack Web API in an "ok": false
// response.
type APIError struct {
	// Code is Slack's error code, e.g. "channel_not_found".
	Code string
}

func (e *APIError) Error() string { return "slack: " + e.Code }

// Temporary reports whether retrying may succeed.
func (e *APIError) Temporary() bool {
	switch e.Code {
	case "ratelimited", "rate_limited", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		return true
	}
	return false
}

// Channel is the Slack [notification.Channel].
type Channel struct {
	token   string
	baseURL string
	client  *http.Client
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithToken sets the bot token ("xoxb-...") used for routes that are channel
// ids or names. Webhook routes do not need it.
func WithToken(token string) Option {
	return func(c *Channel) { c.token = token }
}

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(cl *http.Client) Option {
	return func(c *Channel) { c.client = cl }
}

// WithBaseURL overrides the Web API endpoint, for tests or proxies.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// NewChannel returns a Slack channel.
func NewChannel(opts ...Option) *Channel {
	c := &Channel{baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	route := strings.TrimSpace(d.Route())
	if route == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement slack.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToSlack(d.To)
	if err != nil {
		return err
	}
	if msg == nil || (msg.Text == "" && len(msg.Blocks) == 0) {
		return notification.Permanent(errors.New("slack: message has no text or blocks"))
	}

	if isURL(route) {
		return httpx.JSON(ctx, c.client, "slack", http.MethodPost, route, nil, msg, nil)
	}
	return c.postMessage(ctx, route, msg)
}

func (c *Channel) postMessage(ctx context.Context, channel string, msg *Message) error {
	if c.token == "" {
		return notification.Permanent(errors.New("slack: route is a channel but no bot token is configured (use slack.WithToken)"))
	}
	body := *msg
	if body.Channel == "" {
		body.Channel = channel
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.token)

	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := httpx.JSON(ctx, c.client, "slack", http.MethodPost, c.baseURL+"/chat.postMessage", header, body, &resp); err != nil {
		return err
	}
	if !resp.OK {
		apiErr := &APIError{Code: resp.Error}
		if apiErr.Code == "" {
			apiErr.Code = "unknown_error"
		}
		if apiErr.Temporary() {
			return apiErr
		}
		return notification.Permanent(apiErr)
	}
	return nil
}

func isURL(route string) bool {
	return strings.HasPrefix(route, "https://") || strings.HasPrefix(route, "http://")
}
