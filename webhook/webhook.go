// Package webhook delivers notifications as HTTP requests to any URL.
//
// The recipient's route for "webhook" is the URL. The notification decides the
// payload; the channel adds authentication, an id header for idempotency, and
// maps the response status to an error.
//
//	notifier := notification.New(
//		notification.WithChannel("webhook", webhook.NewChannel(
//			webhook.WithAuth(webhook.HMACSignature(os.Getenv("WEBHOOK_SECRET"))),
//		)),
//	)
//
//	func (n OrderShipped) ToWebhook(notification.Notifiable) (*webhook.Message, error) {
//		return webhook.NewMessage(map[string]any{
//			"event":    "order.shipped",
//			"order_id": n.OrderID,
//		}), nil
//	}
//
// Authentication is pluggable through [Authenticator]: [BearerToken],
// [BasicAuth], [APIKey], [APIKeyQuery], [HMACSignature], [Chain], or your own.
// Receivers verify HMAC-signed requests with [VerifyRequest].
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

// Notification is implemented by notifications that can be sent as webhooks.
type Notification interface {
	ToWebhook(to notification.Notifiable) (*Message, error)
}

// Message is one webhook request, built by a notification.
type Message struct {
	// Method is the HTTP method. The default is POST.
	Method string
	// Headers are added to the request.
	Headers http.Header
	// Query parameters are added to the URL.
	Query url.Values
	// Body is encoded as JSON. It is ignored when RawBody is set.
	Body any
	// RawBody is sent as is, with ContentType.
	RawBody []byte
	// ContentType is the Content-Type of RawBody. The default is
	// application/octet-stream.
	ContentType string
	// Authenticator overrides the channel's authenticator for this message.
	Authenticator Authenticator
}

// NewMessage returns a POST message whose body is v, encoded as JSON.
func NewMessage(v any) *Message {
	return &Message{Body: v}
}

// UseMethod sets the HTTP method and returns m.
func (m *Message) UseMethod(method string) *Message {
	m.Method = method
	return m
}

// Header adds a request header and returns m.
func (m *Message) Header(key, value string) *Message {
	if m.Headers == nil {
		m.Headers = http.Header{}
	}
	m.Headers.Add(key, value)
	return m
}

// Param adds a query parameter and returns m.
func (m *Message) Param(key, value string) *Message {
	if m.Query == nil {
		m.Query = url.Values{}
	}
	m.Query.Add(key, value)
	return m
}

// Raw sends body as is with the given content type and returns m.
func (m *Message) Raw(contentType string, body []byte) *Message {
	m.ContentType, m.RawBody = contentType, body
	return m
}

// Auth overrides the channel's authenticator for this message and returns m.
func (m *Message) Auth(a Authenticator) *Message {
	m.Authenticator = a
	return m
}

// Default names of the headers carrying [notification.Delivery.ID].
const (
	HeaderNotificationID = "X-Notification-ID"
	HeaderIdempotencyKey = "Idempotency-Key"
)

// Channel is the webhook [notification.Channel].
type Channel struct {
	client    *http.Client
	baseURL   string
	auth      Authenticator
	headers   http.Header
	idHeaders []string
	success   []int
}

var _ notification.Channel = (*Channel)(nil)

// Option configures a [Channel].
type Option func(*Channel)

// WithHTTPClient sets the HTTP client. The default is http.DefaultClient.
func WithHTTPClient(cl *http.Client) Option {
	return func(c *Channel) { c.client = cl }
}

// WithBaseURL resolves relative routes ("/hooks/orders") against u, and
// replaces the scheme and host of absolute ones. It exists for tests and
// proxies.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithAuth sets the authenticator for every request. A message can override
// it with [Message.Auth].
func WithAuth(a Authenticator) Option {
	return func(c *Channel) { c.auth = a }
}

// WithHeader adds a header to every request.
func WithHeader(key, value string) Option {
	return func(c *Channel) { c.headers.Add(key, value) }
}

// WithIDHeaders sets the headers that carry the delivery id, so receivers can
// drop duplicates of a retried request. The default is X-Notification-ID and
// Idempotency-Key; calling it with no names sends none.
func WithIDHeaders(names ...string) Option {
	return func(c *Channel) { c.idHeaders = names }
}

// WithSuccessStatus lists the response statuses that count as delivered. By
// default any 2xx status does. Other statuses produce a
// [*notification.HTTPError].
func WithSuccessStatus(codes ...int) Option {
	return func(c *Channel) { c.success = codes }
}

// NewChannel returns a webhook channel.
func NewChannel(opts ...Option) *Channel {
	c := &Channel{
		headers:   http.Header{},
		idHeaders: []string{HeaderNotificationID, HeaderIdempotencyKey},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// maxErrorBody bounds how much of an error response is kept.
const maxErrorBody = 1 << 10

// Send implements [notification.Channel].
func (c *Channel) Send(ctx context.Context, d notification.Delivery) error {
	route := strings.TrimSpace(d.Route())
	if route == "" {
		return notification.ErrNoRoute
	}
	n, ok := d.Notification.(Notification)
	if !ok {
		return fmt.Errorf("%w: %T does not implement webhook.Notification", notification.ErrUnsupported, d.Notification)
	}
	msg, err := n.ToWebhook(d.To)
	if err != nil {
		return err
	}
	if msg == nil {
		return notification.Permanent(errors.New("webhook: nil message"))
	}

	target, err := c.resolve(route, msg.Query)
	if err != nil {
		return err
	}

	var body []byte
	contentType := ""
	switch {
	case msg.RawBody != nil:
		body = msg.RawBody
		contentType = msg.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	case msg.Body != nil:
		body, err = json.Marshal(msg.Body)
		if err != nil {
			return notification.Permanent(fmt.Errorf("webhook: encoding body: %w", err))
		}
		contentType = "application/json"
	}

	method := msg.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return notification.Permanent(fmt.Errorf("webhook: building request: %w", err))
	}
	if body == nil {
		req.Body, req.GetBody, req.ContentLength = http.NoBody, nil, 0
	}
	for k, vs := range c.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for k, vs := range msg.Headers {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, h := range c.idHeaders {
		if d.ID != "" && req.Header.Get(h) == "" {
			req.Header.Set(h, d.ID)
		}
	}

	auth := c.auth
	if msg.Authenticator != nil {
		auth = msg.Authenticator
	}
	if auth != nil {
		if err := auth.Authenticate(req, body); err != nil {
			return notification.Permanent(fmt.Errorf("webhook: authenticating request: %w", err))
		}
	}

	resp, err := httpx.Client(c.client).Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", httpx.Redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if c.delivered(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	herr := &notification.HTTPError{Service: "webhook", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	if herr.Temporary() {
		return herr
	}
	return notification.Permanent(herr)
}

func (c *Channel) delivered(status int) bool {
	if len(c.success) > 0 {
		return slices.Contains(c.success, status)
	}
	return status >= 200 && status <= 299
}

// resolve turns the route into the request URL.
func (c *Channel) resolve(route string, query url.Values) (string, error) {
	u, err := url.Parse(route)
	if err != nil {
		return "", notification.Permanent(fmt.Errorf("webhook: invalid route URL: %w", err))
	}
	if c.baseURL != "" {
		base, err := url.Parse(c.baseURL)
		if err != nil {
			return "", notification.Permanent(fmt.Errorf("webhook: invalid base URL: %w", err))
		}
		if u.IsAbs() {
			u.Scheme, u.Host = base.Scheme, base.Host
		} else {
			u = base.ResolveReference(&url.URL{Path: base.Path + "/" + strings.TrimLeft(u.Path, "/"), RawQuery: u.RawQuery})
		}
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", notification.Permanent(errors.New("webhook: route is not an http(s) URL"))
	}
	if len(query) > 0 {
		q := u.Query()
		for k, vs := range query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
