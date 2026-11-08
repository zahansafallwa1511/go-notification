package smtp_test

import (
	"bufio"
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
