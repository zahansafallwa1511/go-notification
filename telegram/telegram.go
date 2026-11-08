// Package telegram delivers notifications through a Telegram bot.
//
// The channel is created with the bot token from @BotFather. The recipient's
// route for "telegram" is the chat id — a user or group id ("123456789",
// "-1001234567890") or a public channel username ("@acme_status"). The user
// must have started a conversation with the bot first.
//
//	notifier := notification.New(
//		notification.WithChannel("telegram", telegram.NewChannel(os.Getenv("TELEGRAM_BOT_TOKEN"))),
//	)
//
//	func (n OrderShipped) ToTelegram(notification.Notifiable) (*telegram.Message, error) {
//		return telegram.NewMessage("<b>Order shipped</b>\nTracking: "+n.Tracking).
//			HTML().
//			Row(telegram.Button("Track parcel", n.TrackingURL)), nil
//	}
//
// The bot token is part of every request URL; this package keeps it out of the
// errors it returns.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

// DefaultBaseURL is the Telegram Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// Parse modes for [Message.ParseMode].
const (
	ParseModeMarkdownV2 = "MarkdownV2"
	ParseModeHTML       = "HTML"
)

// Notification is implemented by notifications that can be sent to Telegram.
type Notification interface {
	ToTelegram(to notification.Notifiable) (*Message, error)
}

// Message is a Telegram message, built by a notification.
type Message struct {
	// Text is the message, up to 4096 characters after entity parsing.
	Text string
	// ParseMode is "", [ParseModeMarkdownV2] or [ParseModeHTML].
	ParseMode string
	// DisablePreview turns off the link preview.
	DisablePreview bool
	// Silent delivers the message without a sound.
	Silent bool
	// Protect stops the message from being forwarded or saved.
	Protect bool
	// ThreadID posts into a forum topic.
	ThreadID int64
	// Keyboard is an inline keyboard: rows of buttons under the message.
	Keyboard [][]InlineButton
}

// InlineButton is a button of an inline keyboard. Set URL to open a link, or
// CallbackData to send a callback query to the bot.
type InlineButton struct {
	Text         string `json:"text"`
	URL          string `json:"url,omitempty"`
	CallbackData string `json:"callback_data,omitempty"`
}

// Button returns an inline button that opens url.
func Button(text, url string) InlineButton {
	return InlineButton{Text: text, URL: url}
}

// NewMessage returns a message with the given text.
func NewMessage(text string) *Message {
	return &Message{Text: text}
}

// Markdown parses the text as MarkdownV2 and returns m.
func (m *Message) Markdown() *Message {
	m.ParseMode = ParseModeMarkdownV2
	return m
}

// HTML parses the text as HTML and returns m.
func (m *Message) HTML() *Message {
	m.ParseMode = ParseModeHTML
	return m
}

// NoPreview disables the link preview and returns m.
func (m *Message) NoPreview() *Message {
	m.DisablePreview = true
	return m
}

// Quiet delivers the message without a sound and returns m.
func (m *Message) Quiet() *Message {
	m.Silent = true
	return m
}

// Protected stops the message from being forwarded or saved and returns m.
func (m *Message) Protected() *Message {
	m.Protect = true
	return m
}

// InThread posts the message into the forum topic id and returns m.
func (m *Message) InThread(id int64) *Message {
	m.ThreadID = id
	return m
}

// Row appends a row of buttons to the inline keyboard and returns m.
func (m *Message) Row(buttons ...InlineButton) *Message {
	if len(buttons) > 0 {
		m.Keyboard = append(m.Keyboard, buttons)
	}
	return m
}

// APIError is an error reported by the Bot API in an "ok": false response.
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d: %s", e.Code, e.Description)
}

// Channel is the Telegram [notification.Channel].
type Channel struct {
	token   string
	baseURL string
	client  *http.Client
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(cl *http.Client) Option {
	return func(c *Channel) { c.client = cl }
}

// WithBaseURL overrides the Bot API endpoint, for tests or a local Bot API
// server.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// NewChannel returns a Telegram channel for the bot with the given token.
func NewChannel(botToken string, opts ...Option) *Channel {
	c := &Channel{token: botToken, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

type replyMarkup struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

type sendMessage struct {
	ChatID              string       `json:"chat_id"`
	Text                string       `json:"text"`
	ParseMode           string       `json:"parse_mode,omitempty"`
	MessageThreadID     int64        `json:"message_thread_id,omitempty"`
	LinkPreviewOptions  *linkPreview `json:"link_preview_options,omitempty"`
	DisableNotification bool         `json:"disable_notification,omitempty"`
	ProtectContent      bool         `json:"protect_content,omitempty"`
	ReplyMarkup         *replyMarkup `json:"reply_markup,omitempty"`
}

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	chatID := strings.TrimSpace(d.Route())
	if chatID == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement telegram.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToTelegram(d.To)
	if err != nil {
		return err
	}
	if msg == nil || strings.TrimSpace(msg.Text) == "" {
		return notification.Permanent(errors.New("telegram: message has no text"))
	}
	if c.token == "" {
		return notification.Permanent(errors.New("telegram: no bot token configured"))
	}

	req := sendMessage{
		ChatID:              chatID,
		Text:                msg.Text,
		ParseMode:           msg.ParseMode,
		MessageThreadID:     msg.ThreadID,
		DisableNotification: msg.Silent,
		ProtectContent:      msg.Protect,
	}
	if msg.DisablePreview {
		req.LinkPreviewOptions = &linkPreview{IsDisabled: true}
	}
	if len(msg.Keyboard) > 0 {
		req.ReplyMarkup = &replyMarkup{InlineKeyboard: msg.Keyboard}
	}

	var resp struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	endpoint := c.baseURL + "/bot" + c.token + "/sendMessage"
	if err := httpx.JSON(ctx, c.client, "telegram", http.MethodPost, endpoint, nil, req, &resp); err != nil {
		return c.redact(err)
	}
	if !resp.OK {
		return notification.Permanent(&APIError{Code: resp.ErrorCode, Description: resp.Description})
	}
	return nil
}

// redact removes the bot token from transport errors, whose messages include
// the request URL. The wrapping error's message is fixed when it is created,
// so the error is rebuilt rather than patched.
func (c *Channel) redact(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	clean := &url.Error{Op: ue.Op, URL: strings.ReplaceAll(ue.URL, c.token, "<redacted>"), Err: ue.Err}
	return fmt.Errorf("telegram: %w", clean)
}
