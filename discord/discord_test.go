package discord_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/discord"
)

type deployed struct{}

func (deployed) Via(notification.Notifiable) []string { return []string{"discord"} }
func (deployed) ToDiscord(notification.Notifiable) (*discord.Message, error) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return discord.NewMessage("Deploy finished").As("CI").Avatar("https://img.example.test/ci.png").NoMentions().InThread("99").
		Embed(discord.Embed{
			Title:     "v1.4.2",
			URL:       "https://example.test/r",
			Color:     discord.ColorGreen,
			Timestamp: &ts,
			Footer:    &discord.EmbedFooter{Text: "ci"},
		}.Field("Duration", "3m", true)), nil
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"discord"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id", Channel: "discord", To: notification.Route("discord", route), Notification: n, Attempt: 1}
}

func TestSend(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ch := discord.NewChannel(discord.WithBaseURL(srv.URL), discord.WithWait(true))
	if err := ch.Send(t.Context(), delivery("https://discord.com/api/webhooks/1/abc", deployed{})); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/webhooks/1/abc" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	if q := got.URL.Query(); q.Get("wait") != "true" || q.Get("thread_id") != "99" {
		t.Fatalf("query = %v", q)
	}
	want := `{"content":"Deploy finished","username":"CI","avatar_url":"https://img.example.test/ci.png",
		"embeds":[{"title":"v1.4.2","url":"https://example.test/r","color":3066993,"timestamp":"2026-01-02T03:04:05Z",
		"fields":[{"name":"Duration","value":"3m","inline":true}],"footer":{"text":"ci"}}],
		"allowed_mentions":{"parse":[]}}`
	assertJSON(t, body, want)
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decoding %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("body =\n%s\nwant\n%s", gb, wb)
	}
}

func TestErrors(t *testing.T) {
	ch := discord.NewChannel()
	if err := ch.Send(t.Context(), delivery("", deployed{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("https://x.test", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("not a url", deployed{})); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("bad url: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	err := ch.Send(t.Context(), delivery(srv.URL+"/api/webhooks/1/a", deployed{}))
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != 429 || notification.IsPermanent(err) {
		t.Fatalf("429: %v", err)
	}
}
