package smtp_test

import (
	"bufio"
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	notification "github.com/zahansafallwa1511/go-notification"
	"github.com/zahansafallwa1511/go-notification/mail"
	"github.com/zahansafallwa1511/go-notification/mail/smtp"
)

// fakeServer speaks just enough SMTP for a client to deliver a message, and
// records what it received.
type fakeServer struct {
	ln       net.Listener
	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	rejectTo string // RCPT TO for this address gets a 550
	tempTo   string // RCPT TO for this address gets a 451
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeServer) port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *fakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 localhost ESMTP fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			reply("250-localhost")
			reply("250-8BITMIME")
			reply("250 SMTPUTF8")
		case strings.HasPrefix(cmd, "HELO"):
			reply("250 localhost")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			s.mu.Lock()
			s.from = addr(line)
			s.mu.Unlock()
			reply("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			a := addr(line)
			switch a {
			case s.rejectTo:
				reply("550 5.1.1 No such user")
				continue
			case s.tempTo:
				reply("451 4.3.0 Try again later")
				continue
			}
			s.mu.Lock()
			s.rcpts = append(s.rcpts, a)
			s.mu.Unlock()
			reply("250 OK")
		case cmd == "DATA":
			reply("354 End data with <CR><LF>.<CR><LF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			reply("250 OK queued")
		case cmd == "QUIT":
			reply("221 Bye")
			return
		default: // RSET, NOOP, ...
			reply("250 OK")
		}
	}
}

func addr(line string) string {
	i, j := strings.Index(line, "<"), strings.Index(line, ">")
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}

func transport(t *testing.T, s *fakeServer) *smtp.Transport {
	t.Helper()
	tr, err := smtp.New("127.0.0.1", smtp.WithPort(s.port()), smtp.WithTLS(smtp.TLSNone), smtp.WithTimeout(5*time.Second), smtp.WithHELO("test.local"))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func email() *mail.Email {
	return &mail.Email{
		From:    mail.Addr("Acme", "hello@acme.test"),
		To:      []mail.Address{mail.Addr("Ada", "ada@example.test")},
		Cc:      []mail.Address{mail.Addr("", "cc@example.test")},
		Bcc:     []mail.Address{mail.Addr("", "bcc@example.test")},
		ReplyTo: []mail.Address{mail.Addr("", "support@acme.test")},
		Subject: "Invoice paid",
		HTML:    "<p>Paid</p>",
		Text:    "Paid",
		Headers: map[string]string{mail.HeaderNotificationID: "n-1"},
		Attachments: []mail.Attachment{
			{Filename: "invoice.pdf", Data: []byte("%PDF-1.4")},
			{Filename: "logo.png", Data: []byte("\x89PNG\r\n\x1a\n"), Inline: true},
		},
	}
}

func TestSend(t *testing.T) {
	s := newFakeServer(t)
	if err := transport(t, s).Send(t.Context(), email()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.from != "hello@acme.test" {
		t.Errorf("MAIL FROM = %q", s.from)
	}
	if got := strings.Join(s.rcpts, ","); got != "ada@example.test,cc@example.test,bcc@example.test" {
		t.Errorf("RCPT TO = %s", got)
	}
	for _, want := range []string{
		"Subject: Invoice paid",
		"Reply-To: <support@acme.test>",
		"X-Notification-ID: n-1",
		"multipart/alternative",
		"text/plain",
		"text/html",
		`filename="invoice.pdf"`,
		"Content-Id: <logo.png>",
	} {
		if !strings.Contains(s.data, want) {
			t.Errorf("message missing %q", want)
		}
	}
	if strings.Contains(s.data, "bcc@example.test") {
		t.Error("Bcc must not appear in the headers")
	}
}

func TestSendRejected(t *testing.T) {
	s := newFakeServer(t)
	s.rejectTo = "ada@example.test"
	e := email()
	e.Cc, e.Bcc = nil, nil
	err := transport(t, s).Send(t.Context(), e)
	if err == nil || !notification.IsPermanent(err) {
		t.Fatalf("a 550 should be permanent, got %v", err)
	}
}

func TestSendTemporaryFailure(t *testing.T) {
	s := newFakeServer(t)
	s.tempTo = "ada@example.test"
	e := email()
	e.Cc, e.Bcc = nil, nil
	err := transport(t, s).Send(t.Context(), e)
	if err == nil || notification.IsPermanent(err) {
		t.Fatalf("a 451 should be retryable, got %v", err)
	}
}

func TestSendConnectionRefused(t *testing.T) {
	ln, _ := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	tr, err := smtp.New("127.0.0.1", smtp.WithPort(port), smtp.WithTLS(smtp.TLSNone), smtp.WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = tr.Send(t.Context(), email())
	if err == nil || notification.IsPermanent(err) {
		t.Fatalf("connection failure should be retryable, got %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	if _, err := smtp.New("smtp.example.test", smtp.WithTLS(smtp.TLSImplicit), smtp.WithAuth("u", "p")); err != nil {
		t.Fatal(err)
	}
	if _, err := smtp.New("smtp.example.test", smtp.WithTLS(smtp.TLSPolicy(99))); err == nil {
		t.Fatal("unknown TLS policy should fail")
	}
}

func TestThroughChannel(t *testing.T) {
	s := newFakeServer(t)
	ch := mail.NewChannel(transport(t, s), mail.From("Acme", "hello@acme.test"))
	n := notification.New(notification.WithChannel("mail", ch))
	to := notification.Route("mail", "Ada <ada@example.test>")

	if err := n.Send(t.Context(), to, welcome{}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(s.data, "Welcome aboard") {
		t.Errorf("rendered body not delivered:\n%s", s.data)
	}
}

type welcome struct{}

func (welcome) Via(notification.Notifiable) []string { return []string{"mail"} }
func (welcome) ToMail(notification.Notifiable) (*mail.Message, error) {
	return mail.NewMessage().Subject("Welcome").Line("Welcome aboard"), nil
}

func TestOptions(t *testing.T) {
	for name, opts := range map[string][]smtp.Option{
		"opportunistic with TLS config": {smtp.WithTLS(smtp.TLSOpportunistic), smtp.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})},
		"mandatory with server name":    {smtp.WithTLSConfig(&tls.Config{ServerName: "mx.example.test", MinVersion: tls.VersionTLS12})},
		"forced mechanism":              {smtp.WithAuth("u", "p"), smtp.WithAuthMechanism(smtp.AuthPlain)},
		"nil option":                    {nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := smtp.New("smtp.example.test", opts...); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := smtp.New(""); err == nil {
		t.Error("an empty host should fail")
	}
}

func TestSendOpportunisticWithoutSTARTTLS(t *testing.T) {
	s := newFakeServer(t)
	tr, err := smtp.New("127.0.0.1", smtp.WithPort(s.port()), smtp.WithTLS(smtp.TLSOpportunistic),
		smtp.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}), smtp.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	e := email()
	e.Cc, e.Bcc, e.ReplyTo, e.Attachments = nil, nil, nil, nil
	if err := tr.Send(t.Context(), e); err != nil {
		t.Fatalf("opportunistic TLS should fall back to plain text: %v", err)
	}
}

func TestSendMandatoryTLSWithoutSTARTTLS(t *testing.T) {
	s := newFakeServer(t)
	tr, err := smtp.New("127.0.0.1", smtp.WithPort(s.port()), smtp.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Send(t.Context(), email()); err == nil {
		t.Fatal("mandatory TLS must refuse a server without STARTTLS")
	}
}

func TestSendSingleBody(t *testing.T) {
	for name, tc := range map[string]struct {
		html, text, want, notWant string
	}{
		"html only": {html: "<p>Hi</p>", want: "text/html", notWant: "multipart/alternative"},
		"text only": {text: "Hi", want: "text/plain", notWant: "text/html"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newFakeServer(t)
			e := email()
			e.Cc, e.Bcc, e.ReplyTo, e.Attachments = nil, nil, nil, nil
			e.HTML, e.Text = tc.html, tc.text
			if err := transport(t, s).Send(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if !strings.Contains(s.data, tc.want) || strings.Contains(s.data, tc.notWant) {
				t.Errorf("message should contain %q and not %q:\n%s", tc.want, tc.notWant, s.data)
			}
		})
	}
}

func TestSendInvalidAddresses(t *testing.T) {
	bad := mail.Address{Email: "not an address"}
	for name, mutate := range map[string]func(*mail.Email){
		"from":     func(e *mail.Email) { e.From = bad },
		"to":       func(e *mail.Email) { e.To = []mail.Address{bad} },
		"cc":       func(e *mail.Email) { e.Cc = []mail.Address{bad} },
		"bcc":      func(e *mail.Email) { e.Bcc = []mail.Address{bad} },
		"reply-to": func(e *mail.Email) { e.ReplyTo = []mail.Address{bad} },
	} {
		t.Run(name, func(t *testing.T) {
			e := email()
			mutate(e)
			// No server: the message must be rejected before connecting.
			tr, err := smtp.New("127.0.0.1", smtp.WithPort(1), smtp.WithTLS(smtp.TLSNone))
			if err != nil {
				t.Fatal(err)
			}
			err = tr.Send(t.Context(), e)
			if err == nil || !notification.IsPermanent(err) || !strings.Contains(err.Error(), "smtp: "+name) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
