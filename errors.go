package notification

import (
	"errors"
	"fmt"
	"net/http"
)

// ChannelError reports that one channel failed to deliver. [Notifier.Send]
// joins one ChannelError per failed channel, so callers can find out which
// channels failed with [errors.As]:
//
//	var ce *notification.ChannelError
//	if errors.As(err, &ce) {
//		log.Printf("%s failed: %v", ce.Channel, ce.Err)
//	}
type ChannelError struct {
	Channel string
	ID      string
	Err     error
}

func (e *ChannelError) Error() string {
	return fmt.Sprintf("notification: channel %q: %v", e.Channel, e.Err)
}

// Unwrap returns the underlying error.
func (e *ChannelError) Unwrap() error { return e.Err }

// HTTPError is returned by HTTP-based channels and transports when the remote
// service answers with a non-2xx status.
type HTTPError struct {
	// Service names the remote service, e.g. "slack" or "twilio".
	Service string
	// StatusCode is the HTTP status the service returned.
	StatusCode int
	// Body holds the start of the response body, for diagnostics.
	Body string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s: unexpected status %d %s", e.Service, e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("%s: unexpected status %d %s: %s", e.Service, e.StatusCode, http.StatusText(e.StatusCode), e.Body)
}

// Temporary reports whether retrying the request may succeed: rate limiting,
// request timeouts and server errors are temporary; other client errors are
// not.
func (e *HTTPError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode >= 500
}

// permanentError marks an error that must not be retried.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so that retrying dispatchers give up immediately instead
// of trying again. Channels should wrap failures that cannot succeed on retry,
// such as an invalid recipient or rejected credentials. A nil err stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err should not be retried: it was wrapped with
// [Permanent], it is [ErrNoRoute], [ErrUnsupported] or [ErrUnknownChannel],
// or it is an [HTTPError] that is not [HTTPError.Temporary].
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	var p *permanentError
	if errors.As(err, &p) {
		return true
	}
	if errors.Is(err, ErrNoRoute) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrUnknownChannel) {
		return true
	}
	var h *HTTPError
	if errors.As(err, &h) {
		return !h.Temporary()
	}
	return false
}
