// Package output turns a decoded JSON reply into something on screen.
//
// The default is a table for people; -o json is for pipes. The relationship
// between them is stated in --output's help: tables may be restyled at any
// time, JSON will not.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

type Format string

const (
	FormatTable Format = "table"

	FormatJSON Format = "json"

	FormatYAML Format = "yaml"
)

var Formats = []string{
	string(FormatTable),
	string(FormatJSON),
	string(FormatYAML),
	"custom-columns=NAME:.field,...",
}

var (
	ErrUnknownFormat = errors.New("unknown output format")

	ErrNotRenderable = errors.New("value cannot be rendered")
)

const customColumnsPrefix = "custom-columns="

// ParseFormat reads the value of --output, which is either a plain format name
// or `custom-columns=HEADER:.path,...`.
func ParseFormat(value string) (Format, []Column, error) {
	if columns, found := strings.CutPrefix(value, customColumnsPrefix); found {
		parsed, err := ParseColumns(columns)
		if err != nil {
			return "", nil, err
		}

		return FormatTable, parsed, nil
	}

	switch Format(value) {
	case FormatTable, FormatJSON, FormatYAML:
		return Format(value), nil, nil
	default:
		return "", nil, fmt.Errorf("%w %q: use one of %s", ErrUnknownFormat, value, strings.Join(Formats, ", "))
	}
}

type Printer struct {
	Format Format

	// Columns, when set, replaces the automatic column choice for table output.
	Columns []Column

	Out io.Writer

	// Width is the terminal width used to decide how many columns fit. Zero
	// means unknown, and then a fixed cap applies instead.
	Width int

	// Notice receives a line about anything hidden, on stderr, so that stdout
	// stays exactly the table.
	Notice io.Writer
}

// maxColumnsUnknownWidth applies when the output is not a terminal or its width
// cannot be read. A wide reply has thirty columns and printing all of them
// wraps every row into an unreadable block.
const maxColumnsUnknownWidth = 8

// Print renders one value in the selected format.
//
// Everything is normalised first, so all three formats describe the same value
// and convert into each other losslessly. Without it, a Go struct would render
// under its json tags via -o json and its yaml tags via -o yaml, giving two
// different field names for one field.
func (p *Printer) Print(value any) error {
	normalized, err := Normalize(value)
	if err != nil {
		return err
	}

	switch p.Format {
	case FormatJSON:
		return p.printJSON(normalized)
	case FormatYAML:
		return p.printYAML(normalized)
	default:
		return p.printTable(normalized)
	}
}

// Normalize converts any value into the JSON data model — map[string]any,
// []any, string, float64, bool, nil.
//
// Replies decoded from a service are already in it. Commands that build their
// own output (auth status, project list, spec list) hand over Go structs, and
// this is where they join the same pipeline, so -o json works for them exactly
// as it does for a passthrough call.
func Normalize(value any) (any, error) {
	switch value.(type) {
	case nil, bool, string, float64, map[string]any, []any:
		return value, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRenderable, err)
	}

	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRenderable, err)
	}

	return normalized, nil
}

func (p *Printer) printJSON(value any) error {
	return writeJSON(p.Out, value, "  ")
}

func (p *Printer) printYAML(value any) error {
	node, err := yamlNode(value)
	if err != nil {
		return err
	}

	encoder := yaml.NewEncoder(p.Out)
	encoder.SetIndent(2)

	if err := encoder.Encode(node); err != nil {
		return err
	}

	return encoder.Close()
}

// printTable falls back to JSON for shapes that do not tabulate. Refusing to
// print would help nobody: the user wants to see the content.
func (p *Printer) printTable(value any) error {
	if value == nil {
		return nil
	}

	if rows, ok := tableRows(value); ok {
		if len(rows) == 0 {
			_, err := fmt.Fprintln(p.Out, "(empty)")

			return err
		}

		if len(p.Columns) > 0 {
			return p.writeCustomColumns(rows)
		}

		return p.writeRows(flattenRows(rows))
	}

	if object, ok := value.(map[string]any); ok {
		// Custom columns work on a single object too, so that `disk get` and
		// `disk list` can be given the same --output and produce the same shape.
		if len(p.Columns) > 0 {
			return p.writeCustomColumns([]map[string]any{object})
		}

		return p.writeObject(object)
	}

	_, err := fmt.Fprintln(p.Out, cell(value))

	return err
}

// tableRows finds the list inside a reply. List responses are uniformly
// {"items": [...]} on this platform, but keying on "items" would degrade to raw
// JSON the day one is called something else; two array fields are genuinely
// ambiguous, so those fall back too.
func tableRows(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []any:
		return objectRows(typed)
	case map[string]any:
		var found []any

		count := 0

		for _, key := range orderedKeys(typed) {
			if list, ok := typed[key].([]any); ok {
				count++
				found = list
			}
		}

		if count != 1 {
			return nil, false
		}

		return objectRows(found)
	}

	return nil, false
}

func objectRows(list []any) ([]map[string]any, bool) {
	rows := make([]map[string]any, 0, len(list))

	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}

		rows = append(rows, object)
	}

	return rows, true
}

// columnRank leads with the fields that identify a row. Alphabetical order
// would put availability_zone before name.
var columnRank = map[string]int{
	"id": 0, "name": 1, "code": 2, "status": 3, "state": 4,
	"type": 5, "size_gb": 6, "region_code": 7, "availability_zone": 8,
	"created_at": 90, "updated_at": 91, "deleted_at": 92,
}

func newTabWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
}

func (p *Printer) writeRows(rows []map[string]any) error {
	columns, hidden := p.fit(pickColumns(rows), rows)

	writer := newTabWriter(p.Out)

	header := make([]string, len(columns))
	for i, column := range columns {
		header[i] = strings.ToUpper(column)
	}

	if _, err := fmt.Fprintln(writer, strings.Join(header, "\t")); err != nil {
		return err
	}

	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, column := range columns {
			cells[i] = cell(row[column])
		}

		if _, err := fmt.Fprintln(writer, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		return err
	}

	p.reportHidden(hidden)

	return nil
}

// fit drops the columns that will not fit the terminal.
//
// Trailing columns are dropped rather than truncating cells: a cut-off id is
// worse than an absent one, because it looks copyable and is not. What is
// dropped is named on stderr, so a hidden column is a thing you were told
// about rather than something you have to notice.
func (p *Printer) fit(columns []string, rows []map[string]any) (kept, hidden []string) {
	if len(columns) == 0 {
		return columns, nil
	}

	if p.Width <= 0 {
		if len(columns) <= maxColumnsUnknownWidth {
			return columns, nil
		}

		return columns[:maxColumnsUnknownWidth], columns[maxColumnsUnknownWidth:]
	}

	const gap = 3

	used := 0

	for i, column := range columns {
		width := len(column)

		for _, row := range rows {
			if size := len([]rune(cell(row[column]))); size > width {
				width = size
			}
		}

		next := used + width
		if i > 0 {
			next += gap
		}

		// Always keep the first column: a table with no columns says less than a
		// table that overflows by a little.
		if i > 0 && next > p.Width {
			return columns[:i], columns[i:]
		}

		used = next
	}

	return columns, nil
}

func (p *Printer) reportHidden(hidden []string) {
	if len(hidden) == 0 || p.Notice == nil {
		return
	}

	fmt.Fprintf(p.Notice, "%d more column(s): %s\nuse -o json, or -o custom-columns=NAME:.field\n",
		len(hidden), strings.Join(hidden, ", "))
}

// flattenRows lifts nested scalars up under dotted names when a row has no
// scalar of its own.
//
// Some replies are entirely composite — a project listing is {grant, project}
// per row — and picking only top-level scalars leaves zero columns and prints
// an empty table. Flattening turns that into project.name and grant.owner,
// which is what someone scanning the list wants to see.
//
// Only done when needed: where a row already has scalars, those are the
// identifying fields and adding dotted columns beside them is noise.
func flattenRows(rows []map[string]any) []map[string]any {
	if hasScalar(rows) {
		return rows
	}

	flattened := make([]map[string]any, 0, len(rows))

	for _, row := range rows {
		out := map[string]any{}
		flattenInto(out, "", row)
		flattened = append(flattened, out)
	}

	return flattened
}

func hasScalar(rows []map[string]any) bool {
	for _, row := range rows {
		for _, value := range row {
			if isScalar(value) {
				return true
			}
		}
	}

	return false
}

// flattenInto descends one level at a time. Arrays are left alone: their length
// varies per row, so they cannot become stable columns.
func flattenInto(out map[string]any, prefix string, value map[string]any) {
	for key, item := range value {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}

		if nested, ok := item.(map[string]any); ok {
			flattenInto(out, name, nested)

			continue
		}

		out[name] = item
	}
}

// pickColumns keeps scalars only: nested objects and arrays do not fit a cell
// and wreck the layout. -o json shows them — a table is an overview.
func pickColumns(rows []map[string]any) []string {
	seen := map[string]bool{}

	var columns []string

	for _, row := range rows {
		for key, value := range row {
			if seen[key] || !isScalar(value) {
				continue
			}

			seen[key] = true
			columns = append(columns, key)
		}
	}

	sort.Slice(columns, func(i, j int) bool {
		return lessColumn(columns[i], columns[j])
	})

	return columns
}

// writeObject prints one field per line. Laid out horizontally, a record with
// twenty fields wraps, and once it wraps the headers no longer line up.
func (p *Printer) writeObject(object map[string]any) error {
	writer := newTabWriter(p.Out)

	for _, key := range orderedKeys(object) {
		value := object[key]

		if !isScalar(value) {
			// Compact JSON in one cell. These are usually short, and omitting them
			// would suggest the field does not exist.
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}

			if _, err := fmt.Fprintf(writer, "%s\t%s\n", key, encoded); err != nil {
				return err
			}

			continue
		}

		if _, err := fmt.Fprintf(writer, "%s\t%s\n", key, cell(value)); err != nil {
			return err
		}
	}

	return writer.Flush()
}

func isScalar(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

func cell(value any) string {
	switch typed := value.(type) {
	case nil:
		// "-" rather than blank: blank cannot be told apart from a misaligned row.
		return "-"
	case string:
		if typed == "" {
			return "-"
		}

		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		// Every JSON number decodes to float64; print whole ones as integers.
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}

		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}
