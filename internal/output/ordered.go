package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// One key order, used by every format.
//
// Go serialises a map[string]any with its keys sorted alphabetically, which put
// availability_zone before id in JSON while the table led with id. Same value,
// two different readings of it — and the identifying field buried in the middle
// is the one people look for first.
//
// So the order is imposed here instead: identity first, timestamps last, the
// rest alphabetical, in JSON and YAML and tables alike. Note that this is a
// presentation choice and a safe one — a JSON object is unordered by definition
// (RFC 8259), so no consumer can be relying on the previous order.

func orderedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool { return lessColumn(keys[i], keys[j]) })

	return keys
}

// writeJSON encodes with keys in canonical order.
//
// Hand-rolled because encoding/json offers no hook for map key order; it always
// sorts. The pieces are still encoded by encoding/json, so escaping and number
// formatting stay exactly as they were.
func writeJSON(out io.Writer, value any, indent string) error {
	var buffer bytes.Buffer

	if err := encodeJSON(&buffer, value, indent, ""); err != nil {
		return err
	}

	buffer.WriteByte('\n')

	_, err := out.Write(buffer.Bytes())

	return err
}

func encodeJSON(buffer *bytes.Buffer, value any, indent, current string) error {
	switch typed := value.(type) {
	case map[string]any:
		return encodeJSONObject(buffer, typed, indent, current)
	case []any:
		return encodeJSONArray(buffer, typed, indent, current)
	default:
		return encodeJSONScalar(buffer, value)
	}
}

func encodeJSONObject(buffer *bytes.Buffer, object map[string]any, indent, current string) error {
	if len(object) == 0 {
		buffer.WriteString("{}")

		return nil
	}

	inner := current + indent

	buffer.WriteString("{\n")

	for i, key := range orderedKeys(object) {
		if i > 0 {
			buffer.WriteString(",\n")
		}

		buffer.WriteString(inner)

		if err := encodeJSONScalar(buffer, key); err != nil {
			return err
		}

		buffer.WriteString(": ")

		if err := encodeJSON(buffer, object[key], indent, inner); err != nil {
			return err
		}
	}

	buffer.WriteString("\n")
	buffer.WriteString(current)
	buffer.WriteString("}")

	return nil
}

func encodeJSONArray(buffer *bytes.Buffer, list []any, indent, current string) error {
	if len(list) == 0 {
		buffer.WriteString("[]")

		return nil
	}

	inner := current + indent

	buffer.WriteString("[\n")

	for i, item := range list {
		if i > 0 {
			buffer.WriteString(",\n")
		}

		buffer.WriteString(inner)

		if err := encodeJSON(buffer, item, indent, inner); err != nil {
			return err
		}
	}

	buffer.WriteString("\n")
	buffer.WriteString(current)
	buffer.WriteString("]")

	return nil
}

func encodeJSONScalar(buffer *bytes.Buffer, value any) error {
	encoder := json.NewEncoder(buffer)

	// Descriptions and URLs contain <, > and & often enough that escaping them
	// makes output unreadable and forces jq to unescape.
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRenderable, err)
	}

	// Encode appends a newline that has no place mid-document.
	trimmed := bytes.TrimRight(buffer.Bytes(), "\n")
	buffer.Truncate(len(trimmed))

	return nil
}

// yamlNode builds a node tree so YAML follows the same order. yaml.v3 sorts map
// keys on its own, and only an explicit node tree overrides that.
func yamlNode(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

		for _, key := range orderedKeys(typed) {
			child, err := yamlNode(typed[key])
			if err != nil {
				return nil, err
			}

			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				child)
		}

		return node, nil

	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

		for _, item := range typed {
			child, err := yamlNode(item)
			if err != nil {
				return nil, err
			}

			node.Content = append(node.Content, child)
		}

		return node, nil

	default:
		node := &yaml.Node{}
		if err := node.Encode(value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotRenderable, err)
		}

		return node, nil
	}
}

// lessColumn ranks identifying fields first and timestamps last, with
// everything else alphabetical in between.
//
// Three bands rather than two: a ranked field is not automatically more
// important than an unranked one. created_at is ranked precisely so it sinks,
// and a two-band comparison floated it above every ordinary field instead.
//
// Sinking timestamps also improves the narrow-terminal case, where the columns
// dropped first should be the ones nobody scans for.
func lessColumn(a, b string) bool {
	ba, bb := band(a), band(b)
	if ba != bb {
		return ba < bb
	}

	if ba != bandMiddle {
		ra := columnRank[lastSegment(a)]
		rb := columnRank[lastSegment(b)]

		if ra != rb {
			return ra < rb
		}
	}

	return a < b
}

const (
	bandLeading = iota
	bandMiddle
	bandTrailing
)

// trailingRank is where the "put this at the end" half of columnRank starts.
const trailingRank = 50

func band(name string) int {
	rank, ok := columnRank[lastSegment(name)]

	switch {
	case !ok:
		return bandMiddle
	case rank >= trailingRank:
		return bandTrailing
	default:
		return bandLeading
	}
}

func lastSegment(name string) string {
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		return name[index+1:]
	}

	return name
}
