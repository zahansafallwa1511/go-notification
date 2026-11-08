package mail

import (
	htmltemplate "html/template"
	"maps"
	texttemplate "text/template"
)

// Level sets the tone of a [Message]: it picks the default greeting and the
// colour of the action button.
type Level int

// Message levels.
const (
	// LevelInfo is the default: a neutral message.
	LevelInfo Level = iota
	// LevelSuccess colours the button green.
	LevelSuccess
	// LevelError colours the button red and greets with "Whoops!".
	LevelError
)

// Message is the content of a mail notification, built fluently by a
// notification's ToMail method:
//
//	return mail.NewMessage().
//		Subject("Invoice paid").
//		Greeting("Hi Ada,").
//		Line("Your invoice #1042 has been paid.").
//		Action("View invoice", "https://acme.test/invoices/1042").
//		Line("Thank you for your business!"), nil
//
// Lines added before [Message.Action] appear above the button, lines added
// after it below. The [Channel] renders the message with its built-in
// responsive template. To use your own markup instead, set [Message.View] or
// [Message.HTML]; the builder lines are then only used for the text body
// unless [Message.Text] or [Message.TextView] is set too.
//
// The builder methods modify the message in place and return it.
type Message struct {
	subject    string
	greeting   string
	salutation string
	intro      []string
	outro      []string
	action     *action
	level      Level

	from    *Address
	replyTo []Address
	cc      []Address
	bcc     []Address

	attachments []Attachment
	headers     map[string]string
	tags        []string
	metadata    map[string]string

	html     string
	text     string
	view     *view
	textView *textView
}

type action struct {
	Text string
	URL  string
}

type view struct {
	t    *htmltemplate.Template
	name string
	data any
}

type textView struct {
	t    *texttemplate.Template
	name string
	data any
}

// NewMessage returns an empty message.
func NewMessage() *Message { return &Message{} }

// Subject sets the subject line.
func (m *Message) Subject(s string) *Message { m.subject = s; return m }

// Greeting replaces the default greeting ("Hello!", or "Whoops!" for
// [LevelError]).
func (m *Message) Greeting(s string) *Message { m.greeting = s; return m }

// Salutation replaces the default sign-off ("Regards," and the app name).
func (m *Message) Salutation(s string) *Message { m.salutation = s; return m }

// Line adds a paragraph. Text is escaped; it is never interpreted as HTML.
func (m *Message) Line(s string) *Message {
	if m.action == nil {
		m.intro = append(m.intro, s)
	} else {
		m.outro = append(m.outro, s)
	}
	return m
}

// Lines adds several paragraphs.
func (m *Message) Lines(lines ...string) *Message {
	for _, l := range lines {
		m.Line(l)
	}
	return m
}

// Action adds the call-to-action button. A message has at most one; calling
// Action again replaces the text and URL but keeps the lines in place.
func (m *Message) Action(text, url string) *Message {
	m.action = &action{Text: text, URL: url}
	return m
}

// Level sets the message level.
func (m *Message) Level(l Level) *Message { m.level = l; return m }

// Success is shorthand for Level(LevelSuccess).
func (m *Message) Success() *Message { return m.Level(LevelSuccess) }

// Failure is shorthand for Level(LevelError).
func (m *Message) Failure() *Message { return m.Level(LevelError) }

// From overrides the channel's default sender for this message.
func (m *Message) From(name, email string) *Message {
	m.from = &Address{Name: name, Email: email}
	return m
}

// ReplyTo adds a Reply-To address, replacing the channel's default.
func (m *Message) ReplyTo(name, email string) *Message {
	m.replyTo = append(m.replyTo, Address{Name: name, Email: email})
	return m
}

// Cc adds a carbon-copy recipient.
func (m *Message) Cc(name, email string) *Message {
	m.cc = append(m.cc, Address{Name: name, Email: email})
	return m
}

// Bcc adds a blind carbon-copy recipient.
func (m *Message) Bcc(name, email string) *Message {
	m.bcc = append(m.bcc, Address{Name: name, Email: email})
	return m
}

// Attach adds a file attachment. Its content type is detected from the
// filename; use [Message.AttachFile] to set it explicitly.
func (m *Message) Attach(filename string, data []byte) *Message {
	return m.AttachFile(Attachment{Filename: filename, Data: data})
}

// AttachInline embeds a file for use in the HTML body as
// <img src="cid:FILENAME">.
func (m *Message) AttachInline(filename string, data []byte) *Message {
	return m.AttachFile(Attachment{Filename: filename, Data: data, Inline: true})
}

// AttachFile adds an attachment with full control over its fields.
func (m *Message) AttachFile(a Attachment) *Message {
	m.attachments = append(m.attachments, a)
	return m
}

// Header sets an extra MIME header.
func (m *Message) Header(name, value string) *Message {
	if m.headers == nil {
		m.headers = map[string]string{}
	}
	m.headers[name] = value
	return m
}

// Tag adds a provider tag, for transports that support them.
func (m *Message) Tag(tags ...string) *Message {
	m.tags = append(m.tags, tags...)
	return m
}

// Metadata sets a provider metadata entry, for transports that support it.
func (m *Message) Metadata(key, value string) *Message {
	if m.metadata == nil {
		m.metadata = map[string]string{}
	}
	m.metadata[key] = value
	return m
}

// HTML sets a ready-made HTML body, bypassing the built-in template. It is
// used as is, so it must already be safe.
func (m *Message) HTML(html string) *Message { m.html = html; return m }

// Text sets the plain-text body, replacing the one generated from the lines.
func (m *Message) Text(text string) *Message { m.text = text; return m }

// View renders the HTML body with your own template: t.ExecuteTemplate(w,
// name, data). An empty name executes t itself.
func (m *Message) View(t *htmltemplate.Template, name string, data any) *Message {
	m.view = &view{t: t, name: name, data: data}
	return m
}

// TextView renders the plain-text body with a text/template.
func (m *Message) TextView(t *texttemplate.Template, name string, data any) *Message {
	m.textView = &textView{t: t, name: name, data: data}
	return m
}

// clone returns a copy that shares no slices or maps with m, so rendering
// never mutates what a notification returned.
func (m *Message) clone() *Message {
	c := *m
	c.intro = append([]string(nil), m.intro...)
	c.outro = append([]string(nil), m.outro...)
	c.replyTo = append([]Address(nil), m.replyTo...)
	c.cc = append([]Address(nil), m.cc...)
	c.bcc = append([]Address(nil), m.bcc...)
	c.attachments = append([]Attachment(nil), m.attachments...)
	c.tags = append([]string(nil), m.tags...)
	c.headers = maps.Clone(m.headers)
	c.metadata = maps.Clone(m.metadata)
	return &c
}
