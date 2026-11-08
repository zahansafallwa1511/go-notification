// Package httpx holds the HTTP plumbing shared by the HTTP-based channels and
// transports: JSON and form requests, status checking and error mapping.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	notification "github.com/zahansafallwa1511/go-notification"
)

// maxErrorBody bounds how much of an error response is kept for diagnostics.
const maxErrorBody = 1 << 10

// Client returns c, or http.DefaultClient when c is nil.
func Client(c *http.Client) *http.Client {
	if c == nil {
		return http.DefaultClient
	}
	return c
}

// Request describes one outgoing call.
type Request struct {
	Service string // used in error messages, e.g. "slack"
	Method  string // defaults to POST
	URL     string
	Header  http.Header
	Body    []byte
	// Out, when non-nil, receives the JSON-decoded response body of a 2xx
	// response.
	Out any
	// Prepare, when non-nil, runs on the built request before it is sent,
	// e.g. to sign it.
	Prepare func(*http.Request) error
}

// Do sends r with client and returns a *notification.HTTPError for a non-2xx
// response. Client errors other than 408 and 429 are wrapped with
// notification.Permanent so retrying dispatchers do not repeat them.
func Do(ctx context.Context, client *http.Client, r Request) error {
	method := r.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return notification.Permanent(fmt.Errorf("%s: building request: %w", r.Service, err))
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if r.Prepare != nil {
		if err := r.Prepare(req); err != nil {
			return notification.Permanent(fmt.Errorf("%s: preparing request: %w", r.Service, err))
		}
	}

	resp, err := Client(client).Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", r.Service, Redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		herr := &notification.HTTPError{
			Service:    r.Service,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		}
		if herr.Temporary() {
			return herr
		}
		return notification.Permanent(herr)
	}

	if r.Out != nil {
		if err := json.NewDecoder(resp.Body).Decode(r.Out); err != nil && err != io.EOF {
			return fmt.Errorf("%s: decoding response: %w", r.Service, err)
		}
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// JSON encodes v and sends it with a JSON content type. Header may be nil.
func JSON(ctx context.Context, client *http.Client, service, method, endpoint string, header http.Header, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return notification.Permanent(fmt.Errorf("%s: encoding request: %w", service, err))
	}
	h := http.Header{}
	for k, vs := range header {
		h[k] = vs
	}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	return Do(ctx, client, Request{Service: service, Method: method, URL: endpoint, Header: h, Body: body, Out: out})
}

// Form sends values URL-encoded. Header may be nil.
func Form(ctx context.Context, client *http.Client, service, endpoint string, header http.Header, values url.Values, out any) error {
	h := http.Header{}
	for k, vs := range header {
		h[k] = vs
	}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Accept", "application/json")
	return Do(ctx, client, Request{Service: service, URL: endpoint, Header: h, Body: []byte(values.Encode()), Out: out})
}

// Redact strips the path and query from the URL in a transport error. Webhook
// URLs for Slack, Discord and Teams are credentials, query strings can carry
// API keys, and *url.Error puts the full URL into its message, which would
// leak them into logs. Channels that call an http.Client themselves must pass
// its errors through Redact.
func Redact(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	clean := *ue
	if u, perr := url.Parse(ue.URL); perr == nil {
		clean.URL = u.Scheme + "://" + u.Host + "/<redacted>"
	} else {
		clean.URL = "<redacted>"
	}
	return &clean
}
