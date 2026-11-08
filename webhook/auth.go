package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Authenticator adds credentials to an outgoing webhook request. It runs after
// every header is set, with the exact body bytes that will be sent, so it can
// sign the request.
type Authenticator interface {
	Authenticate(req *http.Request, body []byte) error
}

// AuthenticatorFunc adapts a function to the [Authenticator] interface.
type AuthenticatorFunc func(req *http.Request, body []byte) error

// Authenticate implements [Authenticator].
func (f AuthenticatorFunc) Authenticate(req *http.Request, body []byte) error { return f(req, body) }

// BearerToken sends "Authorization: Bearer <token>".
func BearerToken(token string) Authenticator {
	return AuthenticatorFunc(func(req *http.Request, _ []byte) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
}

// BasicAuth sends HTTP basic authentication.
func BasicAuth(username, password string) Authenticator {
	return AuthenticatorFunc(func(req *http.Request, _ []byte) error {
		req.SetBasicAuth(username, password)
		return nil
	})
}

// APIKey sends a static key in a header, e.g. APIKey("X-API-Key", key).
func APIKey(header, value string) Authenticator {
	return AuthenticatorFunc(func(req *http.Request, _ []byte) error {
		req.Header.Set(header, value)
		return nil
	})
}

// APIKeyQuery sends a static key as a query parameter, e.g.
// APIKeyQuery("token", key). Prefer a header where the receiver supports
// one: query strings end up in access logs.
func APIKeyQuery(param, value string) Authenticator {
	return AuthenticatorFunc(func(req *http.Request, _ []byte) error {
		q := req.URL.Query()
		q.Set(param, value)
		req.URL.RawQuery = q.Encode()
		return nil
	})
}

// Chain runs several authenticators in order, e.g. an API key plus a
// signature.
func Chain(auths ...Authenticator) Authenticator {
	return AuthenticatorFunc(func(req *http.Request, body []byte) error {
		for _, a := range auths {
			if a == nil {
				continue
			}
			if err := a.Authenticate(req, body); err != nil {
				return err
			}
		}
		return nil
	})
}

// Default header names used by [HMACSignature].
const (
	DefaultSignatureHeader = "X-Signature"
	DefaultTimestampHeader = "X-Timestamp"
)

// SignatureOption configures [HMACSignature], [VerifySignature] and
// [VerifyRequest]. Sender and receiver must use the same options.
type SignatureOption func(*signatureConfig)

type signatureConfig struct {
	header   string
	tsHeader string
	prefix   string
	newHash  func() hash.Hash
	encoding string
	now      func() time.Time
}

func newSignatureConfig(opts []SignatureOption) signatureConfig {
	c := signatureConfig{
		header:   DefaultSignatureHeader,
		tsHeader: DefaultTimestampHeader,
		newHash:  sha256.New,
		encoding: "hex",
		now:      time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	return c
}

// WithSignatureHeader sets the header carrying the signature. The default is
// "X-Signature".
func WithSignatureHeader(name string) SignatureOption {
	return func(c *signatureConfig) { c.header = name }
}

// WithTimestampHeader sets the header carrying the Unix timestamp. The default
// is "X-Timestamp". An empty name sends no timestamp and signs the body alone,
// which forgoes replay protection.
func WithTimestampHeader(name string) SignatureOption {
	return func(c *signatureConfig) { c.tsHeader = name }
}

// WithSHA512 signs with HMAC-SHA512 instead of HMAC-SHA256.
func WithSHA512() SignatureOption {
	return func(c *signatureConfig) { c.newHash = sha512.New }
}

// WithSignaturePrefix prefixes the signature, e.g. "sha256=" as GitHub does.
func WithSignaturePrefix(prefix string) SignatureOption {
	return func(c *signatureConfig) { c.prefix = prefix }
}

// WithBase64 encodes the signature as standard base64 instead of hex.
func WithBase64() SignatureOption {
	return func(c *signatureConfig) { c.encoding = "base64" }
}

// WithSignatureClock replaces time.Now, for tests.
func WithSignatureClock(now func() time.Time) SignatureOption {
	return func(c *signatureConfig) { c.now = now }
}

// HMACSignature signs requests with a shared secret. It sets the timestamp
// header to the current Unix time and the signature header to the HMAC of
// "<timestamp>.<body>" (or of the body alone when the timestamp header is
// disabled), hex-encoded and optionally prefixed.
//
// Receivers check requests with [VerifyRequest] or [VerifySignature], or with
// their own code following the same recipe.
func HMACSignature(secret string, opts ...SignatureOption) Authenticator {
	cfg := newSignatureConfig(opts)
	key := []byte(secret)
	return AuthenticatorFunc(func(req *http.Request, body []byte) error {
		if len(key) == 0 {
			return errors.New("webhook: empty signing secret")
		}
		ts := ""
		if cfg.tsHeader != "" {
			ts = strconv.FormatInt(cfg.now().Unix(), 10)
			req.Header.Set(cfg.tsHeader, ts)
		}
		req.Header.Set(cfg.header, cfg.prefix+cfg.sign(key, ts, body))
		return nil
	})
}

func (c signatureConfig) sign(key []byte, ts string, body []byte) string {
	mac := hmac.New(c.newHash, key)
	if c.tsHeader != "" {
		mac.Write([]byte(ts))
		mac.Write([]byte("."))
	}
	mac.Write(body)
	sum := mac.Sum(nil)
	if c.encoding == "base64" {
		return base64.StdEncoding.EncodeToString(sum)
	}
	return hex.EncodeToString(sum)
}

// Errors returned by signature verification.
var (
	ErrInvalidSignature = errors.New("webhook: invalid signature")
	ErrStaleTimestamp   = errors.New("webhook: timestamp outside tolerance")
)

// VerifySignature checks a signature produced by [HMACSignature] with the
// same options. timestamp is the value of the timestamp header (ignored when
// the timestamp header is disabled). A tolerance above zero rejects
// timestamps further than that from now, which stops replayed requests.
func VerifySignature(secret, signature, timestamp string, body []byte, tolerance time.Duration, opts ...SignatureOption) error {
	cfg := newSignatureConfig(opts)
	if cfg.tsHeader != "" {
		sec, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: bad timestamp", ErrInvalidSignature)
		}
		if tolerance > 0 {
			skew := cfg.now().Sub(time.Unix(sec, 0))
			if skew < 0 {
				skew = -skew
			}
			if skew > tolerance {
				return ErrStaleTimestamp
			}
		}
	}
	if !strings.HasPrefix(signature, cfg.prefix) {
		return ErrInvalidSignature
	}
	want := cfg.sign([]byte(secret), timestamp, body)
	if !hmac.Equal([]byte(strings.TrimPrefix(signature, cfg.prefix)), []byte(want)) {
		return ErrInvalidSignature
	}
	return nil
}

// VerifyRequest reads r's body and checks its signature, using the header
// names from opts. It returns the body, which has been consumed from r.
//
//	body, err := webhook.VerifyRequest(r, secret, 5*time.Minute)
//	if err != nil {
//		http.Error(w, "bad signature", http.StatusUnauthorized)
//		return
//	}
func VerifyRequest(r *http.Request, secret string, tolerance time.Duration, opts ...SignatureOption) ([]byte, error) {
	cfg := newSignatureConfig(opts)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("webhook: reading body: %w", err)
	}
	ts := ""
	if cfg.tsHeader != "" {
		ts = r.Header.Get(cfg.tsHeader)
	}
	if err := VerifySignature(secret, r.Header.Get(cfg.header), ts, body, tolerance, opts...); err != nil {
		return nil, err
	}
	return body, nil
}
