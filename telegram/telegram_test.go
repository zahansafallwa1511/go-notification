package telegram_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/telegram"
)

type shipped struct{}

func (shipped) Via(notification.Notifiable) []string { return []string{"telegram"} }
func (shipped) ToTelegram(notification.Notifiable) (*telegram.Message, error) {
	return telegram.NewMessage("<b>Shipped</b>").HTML().NoPreview().Quiet().InThread(7).
		Row(telegram.Button("Track", "https://track.example.test/1")), nil
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"telegram"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id", Channel: "telegram", To: notification.Route("telegram", route), Notification: n, Attempt: 1}
}

func TestSend(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	ch := telegram.NewChannel("123:ABC", telegram.WithBaseURL(srv.URL))
	if err := ch.Send(t.Context(), delivery("-100200", shipped{})); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/bot123:ABC/sendMessage" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	var g, w any
	_ = json.Unmarshal(body, &g)
	_ = json.Unmarshal([]byte(`{"chat_id":"-100200","text":"<b>Shipped</b>","parse_mode":"HTML","message_thread_id":7,
		"link_preview_options":{"is_disabled":true},"disable_notification":true,
		"reply_markup":{"inline_keyboard":[[{"text":"Track","url":"https://track.example.test/1"}]]}}`), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("body =\n%s\nwant\n%s", gb, wb)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	err := telegram.NewChannel("123:SECRET", telegram.WithBaseURL(srv.URL)).Send(t.Context(), delivery("1", shipped{}))
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != 400 || !notification.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error message = %q", err)
	}
}

func TestTransportErrorHidesToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused

	err := telegram.NewChannel("123:SECRET", telegram.WithBaseURL(url)).Send(t.Context(), delivery("1", shipped{}))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v, want an error without the token", err)
	}
}

func TestChannelErrors(t *testing.T) {
	ch := telegram.NewChannel("t")
	if err := ch.Send(t.Context(), delivery("", shipped{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("1", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
}
