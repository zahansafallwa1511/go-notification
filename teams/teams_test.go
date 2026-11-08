package teams_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/teams"
)

type incident struct{}

func (incident) Via(notification.Notifiable) []string { return []string{"teams"} }
func (incident) ToTeams(notification.Notifiable) (*teams.Message, error) {
	return teams.NewMessage("Incident opened").Style(teams.ColorAttention).Content("DB is down").
		Fact("Severity", "high").Action("Open", "https://status.example.test/1").Wide(), nil
}

type raw struct{}

func (raw) Via(notification.Notifiable) []string { return []string{"teams"} }
func (raw) ToTeams(notification.Notifiable) (*teams.Message, error) {
	return teams.RawCard(map[string]any{"type": "AdaptiveCard", "version": "1.5", "body": []any{}})
}

type plain struct{}

func (plain) Via(notification.Notifiable) []string { return []string{"teams"} }

func delivery(route string, n notification.Notification) notification.Delivery {
	return notification.Delivery{ID: "id", Channel: "teams", To: notification.Route("teams", route), Notification: n, Attempt: 1}
}

func capture(t *testing.T) (*httptest.Server, *[]byte, **http.Request) {
	t.Helper()
	var body []byte
	var req *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &req
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

func TestSend(t *testing.T) {
	srv, body, req := capture(t)
	ch := teams.NewChannel(teams.WithBaseURL(srv.URL))
	route := "https://prod-01.westus.logic.azure.com/workflows/abc/triggers/manual/paths/invoke?api-version=2016-06-01&sig=xyz"
	if err := ch.Send(t.Context(), delivery(route, incident{})); err != nil {
		t.Fatal(err)
	}
	if (*req).URL.Path != "/workflows/abc/triggers/manual/paths/invoke" || (*req).URL.Query().Get("sig") != "xyz" {
		t.Fatalf("url = %s", (*req).URL)
	}
	assertJSON(t, *body, `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","contentUrl":null,
		"content":{"$schema":"http://adaptivecards.io/schemas/adaptive-card.json","type":"AdaptiveCard","version":"1.4",
		"body":[{"type":"TextBlock","text":"Incident opened","weight":"Bolder","size":"Medium","wrap":true,"color":"Attention"},
		        {"type":"TextBlock","text":"DB is down","wrap":true},
		        {"type":"FactSet","facts":[{"title":"Severity","value":"high"}]}],
		"actions":[{"type":"Action.OpenUrl","title":"Open","url":"https://status.example.test/1"}],
		"msteams":{"width":"Full"}}}]}`)
}

func TestRawCard(t *testing.T) {
	srv, body, _ := capture(t)
	if err := teams.NewChannel().Send(t.Context(), delivery(srv.URL+"/hook", raw{})); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, *body, `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","contentUrl":null,
		"content":{"type":"AdaptiveCard","version":"1.5","body":[]}}]}`)

	if _, err := teams.RawCard("{not json"); err == nil {
		t.Fatal("invalid JSON should fail")
	}
}

func TestErrors(t *testing.T) {
	ch := teams.NewChannel()
	if err := ch.Send(t.Context(), delivery("", incident{})); !errors.Is(err, notification.ErrNoRoute) {
		t.Fatalf("empty route: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("https://x.test", plain{})); !errors.Is(err, notification.ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	if err := ch.Send(t.Context(), delivery("ftp://x", incident{})); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("bad url: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	err := ch.Send(t.Context(), delivery(srv.URL, incident{}))
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != 502 || notification.IsPermanent(err) {
		t.Fatalf("502: %v", err)
	}
}
