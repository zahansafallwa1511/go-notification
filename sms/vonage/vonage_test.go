package vonage_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
