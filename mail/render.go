package mail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"time"
)

//go:embed templates/default.html
var templateFS embed.FS

// defaultTemplate is parsed once; html/template values are safe for
// concurrent execution.
var defaultTemplate = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/default.html"))

// Theme customises the built-in template.
type Theme struct {
	// AppName appears in the header, the default salutation and the footer.
	AppName string
	// AppURL is where the header links to.
	AppURL string
	// LogoURL replaces the header text with an image.
	LogoURL string
	// PrimaryColor is the button colour for LevelInfo. Default #2d3748.
	PrimaryColor string
	// SuccessColor is the button colour for LevelSuccess. Default #48bb78.
	SuccessColor string
	// ErrorColor is the button colour for LevelError. Default #e53e3e.
	ErrorColor string
	// Footer replaces the default copyright line.
	Footer string
}

// config holds the resolved channel and render options.
type config struct {
	from     Address
	replyTo  []Address
	headers  map[string]string
	theme    Theme
	alwaysTo []Address
	layout   *htmltemplate.Template
	now      func() time.Time
}

// Option configures a [Channel] and [Render].
type Option func(*config)

// From sets the default sender. A message can override it with
// [Message.From].
func From(name, email string) Option {
	return func(c *config) { c.from = Address{Name: name, Email: email} }
}

// ReplyTo sets the default Reply-To address. A message can override it with
// [Message.ReplyTo].
func ReplyTo(name, email string) Option {
	return func(c *config) { c.replyTo = []Address{{Name: name, Email: email}} }
}

// WithHeaders adds headers to every email. Message headers win on conflict.
func WithHeaders(h map[string]string) Option {
	return func(c *config) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		maps.Copy(c.headers, h)
	}
}

// AlwaysTo sends every email to addrs instead of the real recipient, and
// drops Cc and Bcc. Use it in development and staging so no real person is
// mailed. The original recipient is kept in an X-Original-To header.
func AlwaysTo(addrs ...Address) Option {
	return func(c *config) { c.alwaysTo = addrs }
}

// WithTheme sets the whole [Theme] at once.
func WithTheme(t Theme) Option {
	return func(c *config) { c.theme = t }
}

// WithAppName sets [Theme.AppName].
func WithAppName(name string) Option {
	return func(c *config) { c.theme.AppName = name }
}

// WithAppURL sets [Theme.AppURL].
func WithAppURL(url string) Option {
	return func(c *config) { c.theme.AppURL = url }
}

// WithLogoURL sets [Theme.LogoURL].
func WithLogoURL(url string) Option {
	return func(c *config) { c.theme.LogoURL = url }
}

// WithPrimaryColor sets [Theme.PrimaryColor], as a CSS hex colour such as
// "#4f46e5".
func WithPrimaryColor(color string) Option {
	return func(c *config) { c.theme.PrimaryColor = color }
}

// WithFooter sets [Theme.Footer].
func WithFooter(text string) Option {
	return func(c *config) { c.theme.Footer = text }
}

// WithLayout replaces the built-in HTML template for builder messages. The
// template is executed with a [LayoutData].
func WithLayout(t *htmltemplate.Template) Option {
	return func(c *config) { c.layout = t }
}

func newConfig(opts []Option) *config {
	c := &config{now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.layout == nil {
		c.layout = defaultTemplate
	}
	return c
}

// ActionData is the call-to-action of a [LayoutData].
type ActionData struct {
	Text string
	URL  string
}

// LayoutData is what the HTML layout template is executed with. Use it when
// writing your own layout for [WithLayout].
type LayoutData struct {
	Subject     string
	Preheader   string
	Greeting    string
	Intro       []string
	Action      *ActionData
	Outro       []string
	Salutation  []string
	Level       Level
	ButtonColor string
	Theme       Theme
	Year        int
}

var hexColor = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}){1,2}$`)

func pickColor(custom, fallback string) string {
	if hexColor.MatchString(custom) {
		return custom
	}
	return fallback
}

// Render turns a [Message] into an [Email] the way the [Channel] does, minus
// the recipient. Use it to preview notifications or to snapshot-test them:
//
//	e, err := mail.Render(msg, mail.From("Acme", "hello@acme.test"), mail.WithAppName("Acme"))
//	os.WriteFile("preview.html", []byte(e.HTML), 0o644)
func Render(msg *Message, opts ...Option) (*Email, error) {
	return render(msg, newConfig(opts))
}

func render(msg *Message, c *config) (*Email, error) {
	if msg == nil {
		return nil, fmt.Errorf("mail: nil message")
	}
	m := msg.clone()

	e := &Email{
		From:        c.from,
		ReplyTo:     append([]Address(nil), c.replyTo...),
		Cc:          m.cc,
		Bcc:         m.bcc,
		Subject:     m.subject,
		Attachments: m.attachments,
		Tags:        m.tags,
		Metadata:    m.metadata,
	}
	if m.from != nil {
		e.From = *m.from
	}
	if len(m.replyTo) > 0 {
		e.ReplyTo = m.replyTo
	}
	if len(c.headers) > 0 || len(m.headers) > 0 {
		e.Headers = map[string]string{}
		maps.Copy(e.Headers, c.headers)
		maps.Copy(e.Headers, m.headers)
	}

	if m.action != nil {
		if err := checkActionURL(m.action.URL); err != nil {
			return nil, err
		}
	}
	data := layoutData(m, c)
	hasBuilderContent := len(m.intro) > 0 || len(m.outro) > 0 || m.action != nil

	// HTML body: View, then raw HTML, then the layout.
	switch {
	case m.view != nil:
		html, err := executeHTML(m.view.t, m.view.name, m.view.data)
		if err != nil {
			return nil, err
		}
		e.HTML = html
	case m.html != "":
		e.HTML = m.html
	default:
		html, err := executeHTML(c.layout, "", data)
		if err != nil {
			return nil, err
		}
		e.HTML = html
	}

	// Text body: TextView, then raw text, then generated from the lines.
	switch {
	case m.textView != nil:
		var b bytes.Buffer
		var err error
		if m.textView.name == "" {
			err = m.textView.t.Execute(&b, m.textView.data)
		} else {
			err = m.textView.t.ExecuteTemplate(&b, m.textView.name, m.textView.data)
		}
		if err != nil {
			return nil, fmt.Errorf("mail: rendering text view: %w", err)
		}
		e.Text = b.String()
	case m.text != "":
		e.Text = m.text
	case hasBuilderContent || (m.view == nil && m.html == ""):
		e.Text = plainText(data)
	}
	return e, nil
}

// checkActionURL rejects button links that are not absolute http(s) or
// mailto URLs. Email clients cannot resolve relative links, and other schemes
// (javascript:, data:) are never safe in mail.
func checkActionURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mail: invalid action URL %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return fmt.Errorf("mail: action URL %q has no host", raw)
		}
		return nil
	case "mailto":
		return nil
	default:
		return fmt.Errorf("mail: action URL %q must be an absolute http, https or mailto URL", raw)
	}
}

func executeHTML(t *htmltemplate.Template, name string, data any) (string, error) {
	if t == nil {
		return "", fmt.Errorf("mail: nil template")
	}
	var b bytes.Buffer
	var err error
	if name == "" {
		err = t.Execute(&b, data)
	} else {
		err = t.ExecuteTemplate(&b, name, data)
	}
	if err != nil {
		return "", fmt.Errorf("mail: rendering html: %w", err)
	}
	return b.String(), nil
}

func layoutData(m *Message, c *config) LayoutData {
	d := LayoutData{
		Subject:   m.subject,
		Preheader: firstOr(m.intro, ""),
		Greeting:  m.greeting,
		Intro:     m.intro,
		Outro:     m.outro,
		Level:     m.level,
		Theme:     c.theme,
		Year:      c.now().Year(),
	}
	if d.Greeting == "" {
		if m.level == LevelError {
			d.Greeting = "Whoops!"
		} else {
			d.Greeting = "Hello!"
		}
	}

	salutation := m.salutation
	if salutation == "" {
		salutation = "Regards,"
		if c.theme.AppName != "" {
			salutation += "\n" + c.theme.AppName
		}
	}
	d.Salutation = strings.Split(salutation, "\n")

	if m.action != nil {
		d.Action = &ActionData{Text: m.action.Text, URL: m.action.URL}
	}
	switch m.level {
	case LevelSuccess:
		d.ButtonColor = pickColor(c.theme.SuccessColor, "#48bb78")
	case LevelError:
		d.ButtonColor = pickColor(c.theme.ErrorColor, "#e53e3e")
	default:
		d.ButtonColor = pickColor(c.theme.PrimaryColor, "#2d3748")
	}
	return d
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// plainText renders the text alternative from the same parts as the HTML.
func plainText(d LayoutData) string {
	var parts []string
	if d.Greeting != "" {
		parts = append(parts, d.Greeting)
	}
	parts = append(parts, d.Intro...)
	if d.Action != nil {
		parts = append(parts, d.Action.Text+": "+d.Action.URL)
	}
	parts = append(parts, d.Outro...)
	if len(d.Salutation) > 0 {
		parts = append(parts, strings.Join(d.Salutation, "\n"))
	}
	return strings.Join(parts, "\n\n") + "\n"
}
