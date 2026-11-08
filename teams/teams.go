// Package teams delivers notifications to Microsoft Teams as Adaptive Cards.
//
// Teams' Office 365 connectors (incoming webhooks) are retired. Post to a
// channel or chat with a Workflows webhook instead: in Teams, add the "Post to
// a channel when a webhook request is received" workflow and copy its URL.
// That URL is the recipient's route for "teams".
//
//	notifier := notification.New(
//		notification.WithChannel("teams", teams.NewChannel()),
//	)
//
//	func (n IncidentOpened) ToTeams(notification.Notifiable) (*teams.Message, error) {
//		return teams.NewMessage("Incident opened").
//			Style(teams.ColorAttention).
//			Content(n.Summary).
//			Fact("Severity", n.Severity).
//			Fact("Service", n.Service).
//			Action("Open incident", n.URL), nil
//	}
//
// For layouts the builder does not cover, design a card at
// https://adaptivecards.io/designer and send it with [RawCard].
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

// Adaptive Card text colors, for [Message.Style].
const (
	ColorDefault   = "Default"
	ColorAccent    = "Accent"
	ColorGood      = "Good"
	ColorWarning   = "Warning"
	ColorAttention = "Attention"
)

// Notification is implemented by notifications that can be sent to Teams.
type Notification interface {
	ToTeams(to notification.Notifiable) (*Message, error)
}

// Message is a Teams message, built by a notification and rendered as an
// Adaptive Card. When Card is set it is sent as the card verbatim and every
// other field is ignored.
type Message struct {
	// Title is shown in bold at the top of the card.
	Title string
	// TitleColor colors the title, one of the Color constants.
	TitleColor string
	// Text is the body, rendered with Adaptive Card markdown.
	Text string
	// Facts are name/value pairs shown as a table.
	Facts []Fact
	// Actions are buttons that open URLs.
	Actions []Action
	// FullWidth stretches the card across the conversation.
	FullWidth bool
	// Card is a complete Adaptive Card, sent as is.
	Card json.RawMessage
}

// Fact is one row of a fact set.
type Fact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// Action is a button that opens a URL.
type Action struct {
	Title string
	URL   string
}

// NewMessage returns a message with the given title.
func NewMessage(title string) *Message {
	return &Message{Title: title}
}

// RawCard returns a message that sends card — a complete Adaptive Card,
// given as JSON bytes, a json.RawMessage, or any value that marshals to one —
// verbatim.
func RawCard(card any) (*Message, error) {
	var raw []byte
	switch c := card.(type) {
	case json.RawMessage:
		raw = c
	case []byte:
		raw = c
	case string:
		raw = []byte(c)
	default:
		b, err := json.Marshal(card)
		if err != nil {
			return nil, fmt.Errorf("teams: encoding card: %w", err)
		}
		raw = b
	}
	if !json.Valid(raw) {
		return nil, errors.New("teams: card is not valid JSON")
	}
	return &Message{Card: json.RawMessage(raw)}, nil
}

// Content sets the body text and returns m.
func (m *Message) Content(text string) *Message {
	m.Text = text
	return m
}

// Style colors the title and returns m.
func (m *Message) Style(color string) *Message {
	m.TitleColor = color
	return m
}

// Fact appends a fact and returns m.
func (m *Message) Fact(title, value string) *Message {
	m.Facts = append(m.Facts, Fact{Title: title, Value: value})
	return m
}

// Action appends a button that opens url and returns m.
func (m *Message) Action(title, url string) *Message {
	m.Actions = append(m.Actions, Action{Title: title, URL: url})
	return m
}

// Wide stretches the card across the conversation and returns m.
func (m *Message) Wide() *Message {
	m.FullWidth = true
	return m
}

// card renders m as an Adaptive Card.
func (m *Message) card() (json.RawMessage, error) {
	if len(m.Card) > 0 {
		return m.Card, nil
	}
	body := []map[string]any{}
	if m.Title != "" {
		tb := map[string]any{"type": "TextBlock", "text": m.Title, "weight": "Bolder", "size": "Medium", "wrap": true}
		if m.TitleColor != "" {
			tb["color"] = m.TitleColor
		}
		body = append(body, tb)
	}
	if m.Text != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": m.Text, "wrap": true})
	}
	if len(m.Facts) > 0 {
		body = append(body, map[string]any{"type": "FactSet", "facts": m.Facts})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if len(m.Actions) > 0 {
		actions := make([]map[string]any, 0, len(m.Actions))
		for _, a := range m.Actions {
			actions = append(actions, map[string]any{"type": "Action.OpenUrl", "title": a.Title, "url": a.URL})
		}
		card["actions"] = actions
	}
	if m.FullWidth {
		card["msteams"] = map[string]any{"width": "Full"}
	}
	return json.Marshal(card)
}

// Channel is the Teams [notification.Channel].
type Channel struct {
	client  *http.Client
	baseURL string
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(cl *http.Client) Option {
	return func(c *Channel) { c.client = cl }
}

// WithBaseURL replaces the scheme and host of every webhook URL, keeping its
// path and query. It exists for tests and proxies.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// NewChannel returns a Teams channel.
func NewChannel(opts ...Option) *Channel {
	c := &Channel{}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

type attachment struct {
	ContentType string          `json:"contentType"`
	ContentURL  *string         `json:"contentUrl"`
	Content     json.RawMessage `json:"content"`
}

type payload struct {
	Type        string       `json:"type"`
	Attachments []attachment `json:"attachments"`
}

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	route := strings.TrimSpace(d.Route())
	if route == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement teams.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToTeams(d.To)
	if err != nil {
		return err
	}
	if msg == nil || (len(msg.Card) == 0 && msg.Title == "" && msg.Text == "" && len(msg.Facts) == 0) {
		return notification.Permanent(errors.New("teams: message is empty"))
	}
	card, err := msg.card()
	if err != nil {
		return notification.Permanent(fmt.Errorf("teams: rendering card: %w", err))
	}
	endpoint, err := c.endpoint(route)
	if err != nil {
		return err
	}
	p := payload{
		Type: "message",
		Attachments: []attachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content:     card,
		}},
	}
	return httpx.JSON(ctx, c.client, "teams", http.MethodPost, endpoint, nil, p, nil)
}

func (c *Channel) endpoint(route string) (string, error) {
	u, err := url.Parse(route)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", notification.Permanent(errors.New("teams: route is not a webhook URL"))
	}
	if c.baseURL != "" {
		base, err := url.Parse(c.baseURL)
		if err != nil {
			return "", notification.Permanent(fmt.Errorf("teams: invalid base URL: %w", err))
		}
		u.Scheme, u.Host = base.Scheme, base.Host
	}
	return u.String(), nil
}
