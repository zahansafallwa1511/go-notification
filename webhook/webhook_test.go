package webhook_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/webhook"
)

type shipped struct{ msg *webhook.Message }

func (shipped) Via(notification.Notifiable) []string { return []string{"webhook"} }
func (s shipped) ToWebhook(notification.Notifiable) (*webhook.Message, error) {
	if s.msg != nil {
		return s.msg, nil
	}
	return webhook.NewMessage(map[string]any{"event": "order.shipped", "id": 7}), nil
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"webhook"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "del-1", Channel: "webhook", To: notification.Route("webhook", route), Notification: n, Attempt: 1}
}

type captured struct {
	req  *http.Request
	body []byte
}

func server(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.body, _ = io.ReadAll(r.Body)
		// Keep a copy: the server still uses r after the handler returns, so
		// tests must not read or modify the original.
		c.req = r.Clone(context.WithoutCancel(r.Context()))
		w.WriteHeader(status)
		_, _ = w.Write([]byte("response body"))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestSendJSON(t *testing.T) {
	srv, got := server(t, http.StatusOK)
	ch := webhook.NewChannel(webhook.WithAuth(webhook.BearerToken("tok")), webhook.WithHeader("X-App", "acme"))

	if err := ch.Send(t.Context(), delivery(srv.URL+"/hooks/orders?a=1", shipped{})); err != nil {
		t.Fatal(err)
	}
	r := got.req
	if r.Method != http.MethodPost || r.URL.Path != "/hooks/orders" || r.URL.Query().Get("a") != "1" {
		t.Fatalf("request = %s %s", r.Method, r.URL)
	}
	if string(got.body) != `{"event":"order.shipped","id":7}` {
		t.Fatalf("body = %s", got.body)
	}
	for k, want := range map[string]string{
		"Content-Type":      "application/json",
		"Authorization":     "Bearer tok",
		"X-App":             "acme",
		"X-Notification-Id": "del-1",
		"Idempotency-Key":   "del-1",
	} {
		if v := r.Header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestMessageOverrides(t *testing.T) {
	srv, got := server(t, http.StatusAccepted)
	msg := webhook.NewMessage(nil).UseMethod(http.MethodPut).Raw("text/plain", []byte("hi")).
		Header("X-App", "override").Param("q", "v").Auth(webhook.BasicAuth("u", "p"))
	ch := webhook.NewChannel(webhook.WithAuth(webhook.BearerToken("tok")), webhook.WithHeader("X-App", "acme"),
		webhook.WithIDHeaders(), webhook.WithBaseURL(srv.URL))

	if err := ch.Send(t.Context(), delivery("/relative", shipped{msg: msg})); err != nil {
		t.Fatal(err)
	}
	r := got.req
	if r.Method != http.MethodPut || r.URL.Path != "/relative" || r.URL.Query().Get("q") != "v" {
		t.Fatalf("request = %s %s", r.Method, r.URL)
	}
	if string(got.body) != "hi" || r.Header.Get("Content-Type") != "text/plain" || r.Header.Get("X-App") != "override" {
		t.Fatalf("body=%q headers=%v", got.body, r.Header)
	}
	if user, pass, ok := r.BasicAuth(); !ok || user != "u" || pass != "p" {
		t.Fatalf("basic auth = %q %q %v", user, pass, ok)
	}
	if r.Header.Get("X-Notification-Id") != "" || r.Header.Get("Idempotency-Key") != "" {
		t.Fatal("id headers should be disabled")
	}
}

func TestAPIKeys(t *testing.T) {
	srv, got := server(t, http.StatusOK)
	ch := webhook.NewChannel(webhook.WithAuth(webhook.Chain(webhook.APIKey("X-API-Key", "k1"), webhook.APIKeyQuery("token", "k2"))))
	if err := ch.Send(t.Context(), delivery(srv.URL, shipped{})); err != nil {
		t.Fatal(err)
	}
	if got.req.Header.Get("X-API-Key") != "k1" || got.req.URL.Query().Get("token") != "k2" {
		t.Fatalf("headers=%v query=%v", got.req.Header, got.req.URL.Query())
	}
}

func TestHMACSignatureRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	cases := map[string][]webhook.SignatureOption{
		"default":    {webhook.WithSignatureClock(clock)},
		"github":     {webhook.WithSignatureClock(clock), webhook.WithSignatureHeader("X-Hub-Signature-256"), webhook.WithTimestampHeader(""), webhook.WithSignaturePrefix("sha256=")},
		"sha512 b64": {webhook.WithSignatureClock(clock), webhook.WithSHA512(), webhook.WithBase64()},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			srv, got := server(t, http.StatusOK)
			ch := webhook.NewChannel(webhook.WithAuth(webhook.HMACSignature("s3cret", opts...)))
			if err := ch.Send(t.Context(), delivery(srv.URL, shipped{})); err != nil {
				t.Fatal(err)
			}
			got.req.Body = io.NopCloser(bytes.NewReader(got.body))
			body, err := webhook.VerifyRequest(got.req, "s3cret", 5*time.Minute, opts...)
			if err != nil {
				t.Fatalf("VerifyRequest: %v (headers %v)", err, got.req.Header)
			}
			if !bytes.Equal(body, got.body) {
				t.Fatal("VerifyRequest returned a different body")
			}

			got.req.Body = io.NopCloser(bytes.NewReader(got.body))
			if _, err := webhook.VerifyRequest(got.req, "wrong", 5*time.Minute, opts...); !errors.Is(err, webhook.ErrInvalidSignature) {
				t.Fatalf("wrong secret: %v", err)
			}
		})
	}
}

func TestHMACKnownValue(t *testing.T) {
	// HMAC-SHA256("secret", "1700000000.{}") computed independently with openssl.
	clock := func() time.Time { return time.Unix(1_700_000_000, 0) }
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	if err := webhook.HMACSignature("secret", webhook.WithSignatureClock(clock)).Authenticate(req, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if ts := req.Header.Get("X-Timestamp"); ts != "1700000000" {
		t.Fatalf("timestamp = %q", ts)
	}
	sig := req.Header.Get("X-Signature")
	if sig != "b8569b78799ff9e3cbff0fc2d63a33a2b57f3282abd07c37ae5e8e7d79a5f163" {
		t.Fatalf("signature = %q", sig)
	}
	if err := webhook.VerifySignature("secret", sig, "1700000000", []byte("{}"), 0); err != nil {
		t.Fatal(err)
	}
	if err := webhook.VerifySignature("secret", sig, "1700000000", []byte("{ }"), 0); !errors.Is(err, webhook.ErrInvalidSignature) {
		t.Fatalf("tampered body: %v", err)
	}
	later := webhook.WithSignatureClock(func() time.Time { return time.Unix(1_700_000_000+600, 0) })
	if err := webhook.VerifySignature("secret", sig, "1700000000", []byte("{}"), time.Minute, later); !errors.Is(err, webhook.ErrStaleTimestamp) {
		t.Fatalf("stale: %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]struct {
		status    int
		opts      []webhook.Option
		wantErr   bool
		permanent bool
	}{
		"2xx ok":           {status: 204},
		"400 permanent":    {status: 400, wantErr: true, permanent: true},
		"503 temporary":    {status: 503, wantErr: true},
		"custom success":   {status: 302, opts: []webhook.Option{webhook.WithSuccessStatus(302)}},
		"custom rejects 2": {status: 200, opts: []webhook.Option{webhook.WithSuccessStatus(201)}, wantErr: true, permanent: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := server(t, tc.status)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			opts := append([]webhook.Option{webhook.WithHTTPClient(client)}, tc.opts...)
			err := webhook.NewChannel(opts...).Send(t.Context(), delivery(srv.URL, shipped{}))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			var herr *notification.HTTPError
			if !errors.As(err, &herr) || herr.StatusCode != tc.status || herr.Body != "response body" {
				t.Fatalf("err = %v", err)
			}
			if notification.IsPermanent(err) != tc.permanent {
				t.Fatalf("IsPermanent = %v, want %v", notification.IsPermanent(err), tc.permanent)
			}
		})
	}
}

func TestChannelErrors(t *testing.T) {
	ch := webhook.NewChannel()
	if err := ch.Send(t.Context(), delivery("", shipped{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("https://x.test", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("mailto:x@y", shipped{})); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("bad url: %v", err)
	}
	failing := webhook.AuthenticatorFunc(func(*http.Request, []byte) error { return errors.New("no creds") })
	if err := webhook.NewChannel(webhook.WithAuth(failing)).Send(t.Context(), delivery("https://x.test", shipped{})); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("auth failure: %v", err)
	}
}

func TestTransportErrorRedactsURL(t *testing.T) {
	// A closed server makes the request fail at the transport, so the error is
	// a *url.Error, which would otherwise print the full URL.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	ch := webhook.NewChannel(webhook.WithAuth(webhook.APIKeyQuery("token", "s3cret-key")))
	err := ch.Send(t.Context(), delivery(srv.URL+"/hooks/secret-path", shipped{}))
	if err == nil {
		t.Fatal("expected a transport error")
	}
	for _, leak := range []string{"s3cret-key", "secret-path"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}
