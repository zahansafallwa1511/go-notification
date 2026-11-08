package httpx_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/internal/httpx"
)

func TestClient(t *testing.T) {
	if httpx.Client(nil) != http.DefaultClient {
		t.Error("nil client should fall back to http.DefaultClient")
	}
	c := &http.Client{}
	if httpx.Client(c) != c {
		t.Error("non-nil client should be returned as is")
	}
}

func TestDoDefaultsAndPrepare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Values("X-Multi"); len(got) != 2 {
			t.Errorf("X-Multi = %v, want two values", got)
		}
		if r.Header.Get("X-Signed") != "yes" {
			t.Error("Prepare did not run")
		}
		_, _ = w.Write([]byte("ignored body"))
	}))
	t.Cleanup(srv.Close)

	err := httpx.Do(t.Context(), srv.Client(), httpx.Request{
		Service: "svc",
		URL:     srv.URL,
		Header:  http.Header{"X-Multi": {"a", "b"}},
		Prepare: func(r *http.Request) error { r.Header.Set("X-Signed", "yes"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDoStatusMapping(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusNotFound, true},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "  reason  ", tc.status)
			}))
			t.Cleanup(srv.Close)

			err := httpx.Do(t.Context(), srv.Client(), httpx.Request{Service: "svc", URL: srv.URL})
			var herr *notification.HTTPError
			if !errors.As(err, &herr) {
				t.Fatalf("err = %v, want *notification.HTTPError", err)
			}
			if herr.Service != "svc" || herr.StatusCode != tc.status || herr.Body != "reason" {
				t.Errorf("HTTPError = %+v", herr)
			}
			if notification.IsPermanent(err) != tc.permanent {
				t.Errorf("IsPermanent = %v, want %v", notification.IsPermanent(err), tc.permanent)
			}
		})
	}
}

func TestDoDecoding(t *testing.T) {
	cases := map[string]struct {
		body    string
		wantErr bool
	}{
		"json":    {`{"ok":true}`, false},
		"empty":   {``, false},
		"invalid": {`{not json`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			var out struct{ OK bool }
			err := httpx.Do(t.Context(), srv.Client(), httpx.Request{Service: "svc", Method: http.MethodGet, URL: srv.URL, Out: &out})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && notification.IsPermanent(err) {
				t.Error("decode failures should be retryable")
			}
			if name == "json" && !out.OK {
				t.Error("response was not decoded")
			}
		})
	}
}

func TestDoRequestErrors(t *testing.T) {
	err := httpx.Do(t.Context(), nil, httpx.Request{Service: "svc", URL: "://bad"})
	if err == nil || !notification.IsPermanent(err) || !strings.Contains(err.Error(), "building request") {
		t.Errorf("bad URL: err = %v", err)
	}

	boom := errors.New("boom")
	err = httpx.Do(t.Context(), nil, httpx.Request{Service: "svc", URL: "http://127.0.0.1", Prepare: func(*http.Request) error { return boom }})
	if !errors.Is(err, boom) || !notification.IsPermanent(err) {
		t.Errorf("Prepare failure: err = %v", err)
	}
}

func TestDoRedactsTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL + "/services/T000/B000/secret-token?key=hunter2"
	srv.Close() // connection refused from here on

	err := httpx.Do(t.Context(), nil, httpx.Request{Service: "slack", URL: endpoint})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if notification.IsPermanent(err) {
		t.Error("transport errors should be retryable")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "hunter2") {
		t.Errorf("error leaks the URL: %s", msg)
	}
	if !strings.HasPrefix(msg, "slack: ") || !strings.Contains(msg, "<redacted>") {
		t.Errorf("error = %s", msg)
	}
}

func TestJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Error("caller header dropped")
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"a":1}` {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"id":"x"}`)
	}))
	t.Cleanup(srv.Close)

	var out struct{ ID string }
	err := httpx.JSON(t.Context(), srv.Client(), "svc", http.MethodPut, srv.URL, http.Header{"Authorization": {"Bearer t"}}, map[string]int{"a": 1}, &out)
	if err != nil || out.ID != "x" {
		t.Fatalf("err = %v, out = %+v", err, out)
	}

	err = httpx.JSON(t.Context(), nil, "svc", http.MethodPost, srv.URL, nil, make(chan int), nil)
	if err == nil || !notification.IsPermanent(err) || !strings.Contains(err.Error(), "encoding request") {
		t.Errorf("unencodable body: err = %v", err)
	}
}

func TestForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Key") != "k" {
			t.Error("caller header dropped")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("To") != "+1" {
			t.Errorf("form = %v", r.PostForm)
		}
		_, _ = io.WriteString(w, `{"sid":"s"}`)
	}))
	t.Cleanup(srv.Close)

	var out struct{ SID string }
	err := httpx.Form(t.Context(), srv.Client(), "svc", srv.URL, http.Header{"X-Key": {"k"}}, url.Values{"To": {"+1"}}, &out)
	if err != nil || out.SID != "s" {
		t.Fatalf("err = %v, out = %+v", err, out)
	}
}

func TestRedact(t *testing.T) {
	plain := errors.New("plain")
	if httpx.Redact(plain) != plain {
		t.Error("non-URL errors should pass through unchanged")
	}

	inner := errors.New("dial failed")
	err := httpx.Redact(&url.Error{Op: "Post", URL: "https://hooks.example/abc?token=t", Err: inner})
	var ue *url.Error
	if !errors.As(err, &ue) || ue.URL != "https://hooks.example/<redacted>" || !errors.Is(err, inner) {
		t.Errorf("redacted = %v", err)
	}

	err = httpx.Redact(&url.Error{Op: "Post", URL: "http://[::1", Err: inner})
	if !errors.As(err, &ue) || ue.URL != "<redacted>" {
		t.Errorf("unparseable URL: redacted = %v", err)
	}
}
