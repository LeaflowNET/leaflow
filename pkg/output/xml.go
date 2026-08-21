package output

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// XML renders a reply as tagged text, for a reader that is a model rather than
// a person or a program.
//
// JSON spends its structure on punctuation, and punctuation is what a model
// most easily loses track of: thirty lines into a nested reply, a closing brace
// says only that something ended. A closing tag says which thing ended, and
// that redundancy is exactly what makes the format easy to attend to — and what
// makes a quotation from the middle of one still carry its own name.
//
// Field names are the contract's, unchanged, so a tag here and a property in an
// operation's schema are the same word.
func writeXML(out io.Writer, value any, root string) error {
	var buffer bytes.Buffer

	if err := encodeXML(&buffer, root, value, ""); err != nil {
		return err
	}

	buffer.WriteByte('\n')

	_, err := out.Write(buffer.Bytes())

	return err
}

const xmlIndent = "  "

func encodeXML(buffer *bytes.Buffer, name string, value any, current string) error {
	switch typed := value.(type) {
	case nil:
		// Omitted rather than written as an empty tag. A reply carries a null for
		// every field that does not apply, and a screen of <deleted_at/> is a
		// screen of nothing being said at some length.
		return nil

	case map[string]any:
		return encodeXMLObject(buffer, name, typed, current)

	case []any:
		return encodeXMLArray(buffer, name, typed, current)

	default:
		return encodeXMLScalar(buffer, name, typed, current)
	}
}

func encodeXMLObject(buffer *bytes.Buffer, name string, object map[string]any, current string) error {
	open, close := tags(name)

	if len(object) == 0 {
		fmt.Fprintf(buffer, "%s%s%s\n", current, open, close)

		return nil
	}

	fmt.Fprintf(buffer, "%s%s\n", current, open)

	inner := current + xmlIndent

	// The same key order as every other format: identity first, timestamps last.
	// A model reading two formats of one reply should not have to work out that
	// they are the same reply.
	for _, key := range sortKeys(object) {
		if err := encodeXML(buffer, key, object[key], inner); err != nil {
			return err
		}
	}

	fmt.Fprintf(buffer, "%s%s\n", current, close)

	return nil
}

// encodeXMLArray names each element <item>, whatever the list is called.
//
// Singularising the parent would read better — <disks><disk/></disks> — but it
// would be a guess about English applied to names that come from a contract,
// and a wrong guess is a tag that does not match the schema anywhere.
func encodeXMLArray(buffer *bytes.Buffer, name string, list []any, current string) error {
	open, close := tags(name)

	if len(list) == 0 {
		fmt.Fprintf(buffer, "%s%s%s\n", current, open, close)

		return nil
	}

	fmt.Fprintf(buffer, "%s%s\n", current, open)

	inner := current + xmlIndent

	for _, item := range list {
		// Written even when nil, unlike a field: a list's elements are positional,
		// and dropping one silently renumbers the rest.
		if item == nil {
			fmt.Fprintf(buffer, "%s<item/>\n", inner)

			continue
		}

		if err := encodeXML(buffer, "item", item, inner); err != nil {
			return err
		}
	}

	fmt.Fprintf(buffer, "%s%s\n", current, close)

	return nil
}

func encodeXMLScalar(buffer *bytes.Buffer, name string, value any, current string) error {
	open, close := tags(name)

	text := renderCell(value)
	if _, ok := value.(string); !ok {
		// renderCell renders an absent value as "-" for a table, where a blank cell
		// cannot be told from a misaligned row. Here the tag already says the
		// field is present, so the dash would be mistaken for its value.
		text = fmt.Sprint(value)
	}

	fmt.Fprintf(buffer, "%s%s", current, open)

	if err := xml.EscapeText(buffer, []byte(text)); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRenderable, err)
	}

	fmt.Fprintf(buffer, "%s\n", close)

	return nil
}

// tags renders a field name as an opening and closing tag.
//
// A contract's field names are already valid XML names — they are Go and
// TypeScript identifiers too — but nothing guarantees that of a reply's map
// keys, and a key with a space in it would produce a document nothing can
// parse. Those keep their name as an attribute rather than being mangled into
// one that no longer matches the schema.
func tags(name string) (open, close string) {
	if isXMLName(name) {
		return "<" + name + ">", "</" + name + ">"
	}

	return `<field name="` + escapeAttribute(name) + `">`, "</field>"
}

// escapeAttribute quotes a name for an attribute value.
//
// Written out rather than borrowing xml.EscapeText, which writes to an
// io.Writer and so returns an error that cannot happen against a buffer —
// leaving a caller to either ignore it or thread it through a function whose
// only job is to build two strings.
var escapeAttribute = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
).Replace

func isXMLName(name string) bool {
	if name == "" {
		return false
	}

	for i, r := range name {
		if unicode.IsLetter(r) || r == '_' {
			continue
		}

		// Digits, hyphens and dots are allowed, but not as the first character.
		if i > 0 && (unicode.IsDigit(r) || r == '-' || r == '.') {
			continue
		}

		return false
	}

	// Reserved by the spec in every combination of case.
	return !strings.EqualFold(name[:min(3, len(name))], "xml")
}
