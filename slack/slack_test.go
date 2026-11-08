package slack_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/slack"
)

type paid struct{}

func (paid) Via(notification.Notifiable) []string { return []string{"slack"} }
func (paid) ToSlack(notification.Notifiable) (*slack.Message, error) {
	return slack.NewMessage("Invoice paid").
		Header("Invoice paid").
		Section("*#42* was paid").
		Fields("*Amount*\n$10", "*By*\nAda").
		Divider().
		Context("sent by billing").
		Image("https://img.example.test/a.png", "chart").
		Actions(slack.Button("View", "https://app.example.test/42").Primary(), slack.Button("Refund", "https://app.example.test/r").Danger().WithActionID("refund")).
		Block(slack.RawBlock(`{"type":"divider","block_id":"raw"}`)).
		As("Billing").Emoji(":moneybag:").InThread("123.456").Unfurl(false), nil
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"slack"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id", Channel: "slack", To: notification.Route("slack", route), Notification: n, Attempt: 1}
}

const wantBody = `{
  "text": "Invoice paid",
  "blocks": [
    {"type":"header","text":{"type":"plain_text","text":"Invoice paid","emoji":true}},
    {"type":"section","text":{"type":"mrkdwn","text":"*#42* was paid"}},
    {"type":"section","fields":[{"type":"mrkdwn","text":"*Amount*\n$10"},{"type":"mrkdwn","text":"*By*\nAda"}]},
    {"type":"divider"},
    {"type":"context","elements":[{"type":"mrkdwn","text":"sent by billing"}]},
    {"type":"image","image_url":"https://img.example.test/a.png","alt_text":"chart"},
    {"type":"actions","elements":[
      {"type":"button","text":{"type":"plain_text","text":"View","emoji":true},"url":"https://app.example.test/42","style":"primary"},
      {"type":"button","text":{"type":"plain_text","text":"Refund","emoji":true},"url":"https://app.example.test/r","action_id":"refund","style":"danger"}
    ]},
    {"type":"divider","block_id":"raw"}
  ],
  %s
  "username": "Billing",
  "icon_emoji": ":moneybag:",
  "thread_ts": "123.456",
  "unfurl_links": false,
  "unfurl_media": false
}`

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decoding body %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decoding want: %v", err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("body =\n%s\nwant\n%s", gb, wb)
	}
}

func TestWebhook(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/services/T/B/X" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("webhooks must not send a token")
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ch := slack.NewChannel(slack.WithToken("xoxb-secret"))
	if err := ch.Send(t.Context(), delivery(srv.URL+"/services/T/B/X", paid{})); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, fmt.Sprintf(wantBody, ""))
}

func TestPostMessage(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer xoxb-secret" {
			t.Errorf("Authorization = %q", auth)
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1.2"}`))
	}))
	defer srv.Close()

	ch := slack.NewChannel(slack.WithToken("xoxb-secret"), slack.WithBaseURL(srv.URL))
	if err := ch.Send(t.Context(), delivery("C123", paid{})); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, fmt.Sprintf(wantBody, `"channel": "C123",`))
}

func TestAPIErrors(t *testing.T) {
	cases := map[string]bool{"channel_not_found": true, "invalid_auth": true, "ratelimited": false}
	for code, permanent := range cases {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"ok":false,"error":"` + code + `"}`))
			}))
			defer srv.Close()
			ch := slack.NewChannel(slack.WithToken("t"), slack.WithBaseURL(srv.URL))
			err := ch.Send(t.Context(), delivery("C1", paid{}))
			var apiErr *slack.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != code {
				t.Fatalf("err = %v", err)
			}
			if notification.IsPermanent(err) != permanent {
				t.Fatalf("IsPermanent = %v, want %v", notification.IsPermanent(err), permanent)
			}
		})
	}
}

func TestWebhookHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no_service"))
	}))
	defer srv.Close()
	err := slack.NewChannel().Send(t.Context(), delivery(srv.URL, paid{}))
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != 404 || herr.Body != "no_service" || !notification.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestChannelErrors(t *testing.T) {
	ch := slack.NewChannel()
	if err := ch.Send(t.Context(), delivery("", paid{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("C1", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("C1", paid{})); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("channel route without token: %v", err)
	}
}
