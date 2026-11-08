// Package discord delivers notifications to Discord through channel webhooks.
//
// The recipient's route for "discord" is a webhook URL
// ("https://discord.com/api/webhooks/{id}/{token}"), created in the channel's
// Integrations settings.
//
//	notifier := notification.New(
//		notification.WithChannel("discord", discord.NewChannel()),
//	)
//
//	func (n DeployFinished) ToDiscord(notification.Notifiable) (*discord.Message, error) {
//		return discord.NewMessage("Deploy finished").Embed(discord.Embed{
//			Title:       "v1.4.2 is live",
//			Description: n.Changelog,
//			Color:       discord.ColorGreen,
//			URL:         n.ReleaseURL,
//		}.Field("Duration", n.Duration.String(), true)), nil
//	}
package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

// Common embed colors.
const (
	ColorGreen  = 0x2ECC71
	ColorRed    = 0xE74C3C
	ColorYellow = 0xF1C40F
	ColorBlue   = 0x3498DB
	ColorGrey   = 0x95A5A6
)

// Notification is implemented by notifications that can be sent to Discord.
type Notification interface {
	ToDiscord(to notification.Notifiable) (*Message, error)
}

// Message is a Discord webhook message, built by a notification. At least one
// of Content and Embeds must be set.
type Message struct {
	// Content is the message text, up to 2000 characters.
	Content string `json:"content,omitempty"`
	// Username overrides the webhook's name.
	Username string `json:"username,omitempty"`
	// AvatarURL overrides the webhook's avatar.
	AvatarURL string `json:"avatar_url,omitempty"`
	// TTS reads the message aloud.
	TTS bool `json:"tts,omitempty"`
	// Embeds are rich cards, up to 10.
	Embeds []Embed `json:"embeds,omitempty"`
	// AllowedMentions limits who the message may ping. Nil leaves Discord's
	// default, which honours every mention in Content.
	AllowedMentions *AllowedMentions `json:"allowed_mentions,omitempty"`
	// ThreadID posts into a thread of the webhook's channel.
	ThreadID string `json:"-"`
}

// AllowedMentions controls which mentions in a message notify anyone.
type AllowedMentions struct {
	// Parse lists mention types to honour: "roles", "users", "everyone".
	// An empty, non-nil slice suppresses all of them.
	Parse []string `json:"parse"`
	Roles []string `json:"roles,omitempty"`
	Users []string `json:"users,omitempty"`
}

// NewMessage returns a message with the given content.
func NewMessage(content string) *Message {
	return &Message{Content: content}
}

// As overrides the webhook's name and returns m.
func (m *Message) As(username string) *Message {
	m.Username = username
	return m
}

// Avatar overrides the webhook's avatar and returns m.
func (m *Message) Avatar(url string) *Message {
	m.AvatarURL = url
	return m
}

// Speak makes the message text-to-speech and returns m.
func (m *Message) Speak() *Message {
	m.TTS = true
	return m
}

// Embed appends embeds and returns m.
func (m *Message) Embed(embeds ...Embed) *Message {
	m.Embeds = append(m.Embeds, embeds...)
	return m
}

// NoMentions stops the message from pinging anyone and returns m.
func (m *Message) NoMentions() *Message {
	m.AllowedMentions = &AllowedMentions{Parse: []string{}}
	return m
}

// InThread posts the message into the thread with the given id and returns m.
func (m *Message) InThread(id string) *Message {
	m.ThreadID = id
	return m
}

// Embed is a rich card attached to a message.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	URL         string       `json:"url,omitempty"`
	Color       int          `json:"color,omitempty"`
	Timestamp   *time.Time   `json:"timestamp,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Thumbnail   *EmbedImage  `json:"thumbnail,omitempty"`
	Image       *EmbedImage  `json:"image,omitempty"`
}

// Field returns e with a field appended.
func (e Embed) Field(name, value string, inline bool) Embed {
	e.Fields = append(e.Fields, EmbedField{Name: name, Value: value, Inline: inline})
	return e
}

// EmbedField is a name/value pair shown in an embed.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// EmbedFooter is the small text at the bottom of an embed.
type EmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedAuthor is shown at the top of an embed.
type EmbedAuthor struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedImage is an embed image or thumbnail.
type EmbedImage struct {
	URL string `json:"url"`
}

// Channel is the Discord [notification.Channel].
type Channel struct {
	client  *http.Client
	baseURL string
	wait    bool
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(cl *http.Client) Option {
	return func(c *Channel) { c.client = cl }
}

// WithBaseURL replaces the scheme and host of every webhook URL, keeping its
// path. It exists for tests and proxies.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithWait makes Discord confirm the message was created before answering
// (the wait=true query parameter), so validation errors surface as errors.
func WithWait(wait bool) Option {
	return func(c *Channel) { c.wait = wait }
}

// NewChannel returns a Discord channel.
func NewChannel(opts ...Option) *Channel {
	c := &Channel{}
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
		return fmt.Errorf("%w: %T does not implement discord.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToDiscord(d.To)
	if err != nil {
		return err
	}
	if msg == nil || (msg.Content == "" && len(msg.Embeds) == 0) {
		return notification.Permanent(errors.New("discord: message has no content or embeds"))
	}

	endpoint, err := c.endpoint(route, msg)
	if err != nil {
		return err
	}
	return httpx.JSON(ctx, c.client, "discord", http.MethodPost, endpoint, nil, msg, nil)
}

func (c *Channel) endpoint(route string, msg *Message) (string, error) {
	u, err := url.Parse(route)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", notification.Permanent(errors.New("discord: route is not a webhook URL"))
	}
	if c.baseURL != "" {
		base, err := url.Parse(c.baseURL)
		if err != nil {
			return "", notification.Permanent(fmt.Errorf("discord: invalid base URL: %w", err))
		}
		u.Scheme, u.Host = base.Scheme, base.Host
	}
	q := u.Query()
	if c.wait {
		q.Set("wait", "true")
	}
	if msg.ThreadID != "" {
		q.Set("thread_id", msg.ThreadID)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
