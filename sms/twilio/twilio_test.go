package twilio_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/sms"
	"github.com/zahansafallwa1511/go-notification/sms/twilio"
)

func TestSend(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		got = r
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM1"}`))
	}))
	defer srv.Close()

	p := twilio.New("AC123", "secret", twilio.WithBaseURL(srv.URL), twilio.WithStatusCallback("https://cb.example.test"))
	err := p.Send(t.Context(), sms.SMS{To: "+15550100", From: "+15550000", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/2010-04-01/Accounts/AC123/Messages.json" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	if auth := got.Header.Get("Authorization"); auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("AC123:secret")) {
		t.Fatalf("Authorization = %q", auth)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", ct)
	}
	for k, want := range map[string]string{"To": "+15550100", "From": "+15550000", "Body": "hello", "StatusCallback": "https://cb.example.test"} {
		if v := got.PostForm.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestMessagingServiceAndAPIKey(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p := twilio.New("AC123", "", twilio.WithBaseURL(srv.URL), twilio.WithMessagingServiceSID("MG1"), twilio.WithAPIKey("SK1", "keysecret"))
	if err := p.Send(t.Context(), sms.SMS{To: "+15550100", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if v := got.PostForm.Get("MessagingServiceSid"); v != "MG1" {
		t.Fatalf("MessagingServiceSid = %q", v)
	}
	if _, ok := got.PostForm["From"]; ok {
		t.Fatal("From should be omitted")
	}
	if auth := got.Header.Get("Authorization"); auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("SK1:keysecret")) {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":21211,"message":"invalid To"}`))
	}))
	defer srv.Close()

	p := twilio.New("AC1", "t", twilio.WithBaseURL(srv.URL))
	err := p.Send(t.Context(), sms.SMS{To: "+1", From: "+2", Body: "x"})
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != 400 || !notification.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent HTTPError 400", err)
	}
	if err := p.Send(t.Context(), sms.SMS{To: "+1", Body: "x"}); err == nil || !notification.IsPermanent(err) {
		t.Fatalf("missing sender: %v", err)
	}
}
