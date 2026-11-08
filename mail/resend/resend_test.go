package resend_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
	"github.com/zahansafallwa1511/go-notification/mail/resend"
)

func email() *mail.Email {
	return &mail.Email{
		From:     mail.Addr("Acme", "hello@acme.test"),
		To:       []mail.Address{mail.Addr("", "ada@example.test")},
		Cc:       []mail.Address{mail.Addr("", "cc@example.test")},
		ReplyTo:  []mail.Address{mail.Addr("", "support@acme.test")},
		Subject:  "Hi",
		HTML:     "<p>Hi</p>",
		Text:     "Hi",
		Headers:  map[string]string{mail.HeaderNotificationID: "n-1", "X-App": "acme"},
		Tags:     []string{"billing"},
		Metadata: map[string]string{"invoice id": "1042"},
		Attachments: []mail.Attachment{
			{Filename: "a.pdf", Data: []byte("%PDF")},
			{Filename: "logo.png", Data: []byte("png"), Inline: true},
		},
	}
}

func TestSend(t *testing.T) {
	var got map[string]any
	var header http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header, path = r.Header, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_123"}`))
	}))
	defer srv.Close()

	tr := resend.New("re_key", resend.WithBaseURL(srv.URL+"/"), resend.WithHTTPClient(srv.Client()))
	if err := tr.Send(t.Context(), email()); err != nil {
		t.Fatal(err)
	}

	if path != "/emails" {
		t.Errorf("path = %q", path)
	}
	if header.Get("Authorization") != "Bearer re_key" || header.Get("Idempotency-Key") != "n-1" {
		t.Errorf("headers = %v", header)
	}
	if got["from"] != `"Acme" <hello@acme.test>` || got["subject"] != "Hi" || got["html"] != "<p>Hi</p>" {
		t.Errorf("payload = %v", got)
	}
	if to := got["to"].([]any); len(to) != 1 || to[0] != "ada@example.test" {
		t.Errorf("to = %v", to)
	}
	if got["reply_to"].([]any)[0] != "support@acme.test" {
		t.Errorf("reply_to = %v", got["reply_to"])
	}
	atts := got["attachments"].([]any)
	first := atts[0].(map[string]any)
	if first["filename"] != "a.pdf" || first["content"] != base64.StdEncoding.EncodeToString([]byte("%PDF")) || first["content_type"] != "application/pdf" {
		t.Errorf("attachment = %v", first)
	}
	if atts[1].(map[string]any)["content_id"] != "logo.png" {
		t.Errorf("inline attachment = %v", atts[1])
	}
	tags := got["tags"].([]any)
	if len(tags) != 2 || tags[1].(map[string]any)["name"] != "invoice_id" {
		t.Errorf("tags = %v", tags)
	}
}

func TestSendErrors(t *testing.T) {
	cases := map[string]struct {
		status    int
		permanent bool
	}{
		"validation": {http.StatusUnprocessableEntity, true},
		"auth":       {http.StatusUnauthorized, true},
		"rate limit": {http.StatusTooManyRequests, false},
		"server":     {http.StatusInternalServerError, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"nope"}`))
			}))
			defer srv.Close()

			err := resend.New("k", resend.WithBaseURL(srv.URL)).Send(t.Context(), email())
			var herr *notification.HTTPError
			if !errors.As(err, &herr) || herr.StatusCode != tc.status || herr.Service != "resend" {
				t.Fatalf("err = %v", err)
			}
			if notification.IsPermanent(err) != tc.permanent {
				t.Fatalf("IsPermanent = %v, want %v", notification.IsPermanent(err), tc.permanent)
			}
		})
	}
}
