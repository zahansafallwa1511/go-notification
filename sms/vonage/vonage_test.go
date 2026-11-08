package vonage_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/sms"
	"github.com/zahansafallwa1511/go-notification/sms/vonage"
)

func server(t *testing.T, reply string, got *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sms/json" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Error(err)
			}
		}
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSend(t *testing.T) {
	var got map[string]string
	srv := server(t, `{"message-count":"1","messages":[{"status":"0","message-id":"m1"}]}`, &got)
	p := vonage.New("key", "secret", vonage.WithBaseURL(srv.URL))

	if err := p.Send(t.Context(), sms.SMS{To: "+447700900000", From: "ACME", Body: "héllo", Unicode: true, ID: "abc"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"api_key": "key", "api_secret": "secret", "from": "ACME", "to": "447700900000",
		"text": "héllo", "type": "unicode", "client-ref": "abc",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRejectedMessage(t *testing.T) {
	cases := map[string]struct {
		status    string
		permanent bool
	}{
		"invalid number": {"3", true},
		"throttled":      {"1", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := server(t, `{"messages":[{"status":"`+tc.status+`","error-text":"nope"}]}`, nil)
			err := vonage.New("k", "s", vonage.WithBaseURL(srv.URL)).Send(t.Context(), sms.SMS{To: "1", From: "A", Body: "x"})
			var verr *vonage.Error
			if !errors.As(err, &verr) || verr.Status != tc.status || verr.Text != "nope" {
				t.Fatalf("err = %v", err)
			}
			if notification.IsPermanent(err) != tc.permanent {
				t.Fatalf("IsPermanent = %v, want %v", notification.IsPermanent(err), tc.permanent)
			}
		})
	}
}

func TestMissingSender(t *testing.T) {
	err := vonage.New("k", "s").Send(t.Context(), sms.SMS{To: "1", Body: "x"})
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallbackAndHTTPClient(t *testing.T) {
	var got map[string]string
	srv := server(t, `{"messages":[{"status":"0"}]}`, &got)
	long := strings.Repeat("x", 150)
	p := vonage.New("k", "s", nil, vonage.WithBaseURL(srv.URL+"/"), vonage.WithHTTPClient(srv.Client()), vonage.WithCallback("https://acme.test/dlr"))

	if err := p.Send(t.Context(), sms.SMS{To: "1", From: "A", Body: "x", ID: long}); err != nil {
		t.Fatal(err)
	}
	if got["callback"] != "https://acme.test/dlr" || got["status-report-req"] != "1" {
		t.Errorf("callback = %q, status-report-req = %q", got["callback"], got["status-report-req"])
	}
	if got["client-ref"] != long[:100] {
		t.Errorf("client-ref has %d characters, want 100", len(got["client-ref"]))
	}
	if _, ok := got["type"]; ok {
		t.Error("type must be omitted for non-unicode messages")
	}
}

func TestMissingRecipient(t *testing.T) {
	err := vonage.New("k", "s").Send(t.Context(), sms.SMS{From: "A", Body: "x"})
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyResponse(t *testing.T) {
	srv := server(t, `{"messages":[]}`, nil)
	err := vonage.New("k", "s", vonage.WithBaseURL(srv.URL)).Send(t.Context(), sms.SMS{To: "1", From: "A", Body: "x"})
	if err == nil || notification.IsPermanent(err) {
		t.Fatalf("an empty status list should be a retryable error, got %v", err)
	}
}

func TestHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	err := vonage.New("k", "s", vonage.WithBaseURL(srv.URL)).Send(t.Context(), sms.SMS{To: "1", From: "A", Body: "x"})
	var herr *notification.HTTPError
	if !errors.As(err, &herr) || herr.StatusCode != http.StatusServiceUnavailable || notification.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorString(t *testing.T) {
	err := &vonage.Error{Status: "15", Text: "Illegal Sender Address"}
	if got, want := err.Error(), "vonage: status 15: Illegal Sender Address"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if err.Temporary() {
		t.Error("status 15 should not be temporary")
	}
	if !(&vonage.Error{Status: "5"}).Temporary() {
		t.Error("status 5 should be temporary")
	}
}
