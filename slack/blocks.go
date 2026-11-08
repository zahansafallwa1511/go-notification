package slack

import (
	"encoding/json"
)

// Block is one Block Kit layout block. The concrete types in this package —
// [HeaderBlock], [SectionBlock], [DividerBlock], [ContextBlock], [ImageBlock],
// [ActionsBlock] — cover the common layouts; [RawBlock] carries any other
// block as raw JSON.
type Block interface {
	slackBlock()
}

// Text is a Block Kit text object.
type Text struct {
	// Type is "mrkdwn" or "plain_text".
	Type string `json:"type"`
	// Text is the content.
	Text string `json:"text"`
	// Emoji converts emoji shortcodes in plain_text objects.
	Emoji bool `json:"emoji,omitempty"`
}

// Markdown returns a mrkdwn text object.
func Markdown(s string) Text { return Text{Type: "mrkdwn", Text: s} }

// Plain returns a plain_text text object with emoji shortcodes enabled.
func Plain(s string) Text { return Text{Type: "plain_text", Text: s, Emoji: true} }

// HeaderBlock is a large, bold heading.
type HeaderBlock struct {
	Text Text
}

func (HeaderBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b HeaderBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		Text Text   `json:"text"`
	}{"header", b.Text})
}

// SectionBlock is a block of text, optionally with a two-column set of fields
// and an accessory element such as a button or image.
type SectionBlock struct {
	Text      *Text
	Fields    []Text
	Accessory any
}

func (SectionBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b SectionBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type      string `json:"type"`
		Text      *Text  `json:"text,omitempty"`
		Fields    []Text `json:"fields,omitempty"`
		Accessory any    `json:"accessory,omitempty"`
	}{"section", b.Text, b.Fields, b.Accessory})
}

// DividerBlock is a horizontal rule.
type DividerBlock struct{}

func (DividerBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (DividerBlock) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"divider"}`), nil
}

// ContextBlock is a line of small, muted text and images.
type ContextBlock struct {
	// Elements are Text values or [ImageElement] values.
	Elements []any
}

func (ContextBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b ContextBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Elements []any  `json:"elements"`
	}{"context", b.Elements})
}

// ImageBlock is a full-width image.
type ImageBlock struct {
	URL   string
	Alt   string
	Title *Text
}

func (ImageBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b ImageBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type  string `json:"type"`
		URL   string `json:"image_url"`
		Alt   string `json:"alt_text"`
		Title *Text  `json:"title,omitempty"`
	}{"image", b.URL, b.Alt, b.Title})
}

// ImageElement is a small image used inside a [ContextBlock] or as a section
// accessory.
type ImageElement struct {
	URL string
	Alt string
}

// MarshalJSON implements [json.Marshaler].
func (e ImageElement) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		URL  string `json:"image_url"`
		Alt  string `json:"alt_text"`
	}{"image", e.URL, e.Alt})
}

// ActionsBlock holds interactive elements, typically buttons.
type ActionsBlock struct {
	Elements []any
}

func (ActionsBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b ActionsBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Elements []any  `json:"elements"`
	}{"actions", b.Elements})
}

// ButtonElement is a button. A button with a URL opens it; one without
// sends an interaction payload to your app.
type ButtonElement struct {
	Text     Text
	URL      string
	Value    string
	ActionID string
	// Style is "", "primary" or "danger".
	Style string
}

// Button returns a button labelled text that opens url.
func Button(text, url string) *ButtonElement {
	return &ButtonElement{Text: Plain(text), URL: url}
}

// Primary gives the button the primary (green) style and returns b.
func (b *ButtonElement) Primary() *ButtonElement {
	b.Style = "primary"
	return b
}

// Danger gives the button the danger (red) style and returns b.
func (b *ButtonElement) Danger() *ButtonElement {
	b.Style = "danger"
	return b
}

// WithActionID sets the action id reported in interaction payloads and
// returns b.
func (b *ButtonElement) WithActionID(id string) *ButtonElement {
	b.ActionID = id
	return b
}

// WithValue sets the value reported in interaction payloads and returns b.
func (b *ButtonElement) WithValue(v string) *ButtonElement {
	b.Value = v
	return b
}

// MarshalJSON implements [json.Marshaler].
func (b ButtonElement) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Text     Text   `json:"text"`
		URL      string `json:"url,omitempty"`
		Value    string `json:"value,omitempty"`
		ActionID string `json:"action_id,omitempty"`
		Style    string `json:"style,omitempty"`
	}{"button", b.Text, b.URL, b.Value, b.ActionID, b.Style})
}

// RawBlock is a block given as raw JSON, for block types this package has no
// helper for. It must be a complete block object, including "type".
type RawBlock json.RawMessage

func (RawBlock) slackBlock() {}

// MarshalJSON implements [json.Marshaler].
func (b RawBlock) MarshalJSON() ([]byte, error) {
	if len(b) == 0 {
		return []byte("null"), nil
	}
	return b, nil
}
