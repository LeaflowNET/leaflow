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

	// FormatXML is for a model reading the reply. See xml.go for why tags beat
	// braces for that reader.
	FormatXML Format = "xml"
)

var Formats = []string{
	string(FormatTable),
	string(FormatJSON),
	string(FormatYAML),
	string(FormatXML),
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
	case FormatTable, FormatJSON, FormatYAML, FormatXML:
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

	// Root names the outermost XML element. A reply is an unnamed value and XML
	// has no way to write one, so something has to be chosen; a caller that knows
	// what it asked for can say so instead of taking "result".
	Root string
}

func (p *Printer) rootTag() string {
	if p.Root != "" {
		return p.Root
	}

	return "result"
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
	normalized, err := jsonValue(value)
	if err != nil {
		return err
	}

	switch p.Format {
	case FormatJSON:
		return p.printJSON(normalized)
	case FormatYAML:
		return p.printYAML(normalized)
	case FormatXML:
		return writeXML(p.Out, normalized, p.rootTag())
	default:
		return p.printTable(normalized)
	}
}

// jsonValue returns the value as JSON decoding would have produced it: nothing
// but map[string]any, []any, string, float64, bool and nil.
//
// That is what lets every format describe one value. A Go struct carries both
// json and yaml tags, so rendering it directly would give a field one name
// under -o json and another under -o yaml — the same value read two ways.
// Sending it through JSON first settles which name it has, once, for all of
// them.
//
// Replies decoded from a service arrive in that shape already. Commands that
// build their own output — auth status, project list — hand over structs, and
// this is where they join the same pipeline.
func jsonValue(value any) (any, error) {
	if isJSONValue(value) {
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

// isJSONValue reports whether a value is in that shape already, all the way
// down.
//
// All the way down, because a decoded reply is, but a value built by hand is
// only so at the top: map[string]any{"required": []string{...}} passes a
// shallow check and then reaches the renderers carrying a []string none of them
// know. JSON survives that — encoding/json will marshal anything — but the XML
// writer printed it as Go's own `[diskId]`.
//
// Checked rather than always marshalling, because the common case is a decoded
// reply, and a round trip through JSON to discover it was already JSON is a
// cost that shows up on exactly the largest listings.
func isJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil, bool, string, float64:
		return true

	case map[string]any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}

		return true

	case []any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}

		return true
	}

	return false
}

func (p *Printer) printJSON(value any) error {
	return writeJSON(p.Out, value, "  ")
}

func (p *Printer) printYAML(value any) error {
	node, err := buildYAMLNode(value)
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

	if rows, ok := findTableRows(value); ok {
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

	_, err := fmt.Fprintln(p.Out, renderCell(value))

	return err
}

// findTableRows finds the list inside a reply. List responses are uniformly
// {"items": [...]} on this platform, but keying on "items" would degrade to raw
// JSON the day one is called something else; two array fields are genuinely
// ambiguous, so those fall back too.
func findTableRows(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []any:
		return objectRows(typed)
	case map[string]any:
		var found []any

		count := 0

		for _, key := range sortKeys(typed) {
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
//
// The audit fields sink for the same reason the timestamps do, and created_by
// costs more than most: it is a UUID, so it takes thirty-six characters to say
// something nobody reading a list is scanning for — and takes them from the id
// beside it, which is the one people copy.
var columnRank = map[string]int{
	"id":                0,
	"name":              1,
	"code":              2,
	"status":            3,
	"state":             4,
	"type":              5,
	"size_gb":           6,
	"region_code":       7,
	"availability_zone": 8,
	"created_by":        80,
	"updated_by":        81,
	"deleted_by":        82,
	"created_at":        90,
	"updated_at":        91,
	"deleted_at":        92,
}

func newTabWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
}

func (p *Printer) writeRows(rows []map[string]any) error {
	columns := pickColumns(rows)
	headings := shorten(columns)

	kept := p.fit(columns, headings, rows)
	hidden := columns[kept:]
	columns, headings = columns[:kept], headings[:kept]

	writer := newTabWriter(p.Out)

	header := make([]string, len(headings))
	for i, heading := range headings {
		header[i] = strings.ToUpper(heading)
	}

	if _, err := fmt.Fprintln(writer, strings.Join(header, "\t")); err != nil {
		return err
	}

	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, column := range columns {
			cells[i] = renderCell(row[column])
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

// fit reports how many leading columns fit the terminal.
//
// Trailing columns are dropped rather than truncating cells: a cut-off id is
// worse than an absent one, because it looks copyable and is not. What is
// dropped is named on stderr, so a hidden column is a thing you were told
// about rather than something you have to notice.
func (p *Printer) fit(columns, headings []string, rows []map[string]any) int {
	if len(columns) == 0 {
		return 0
	}

	if p.Width <= 0 {
		return min(len(columns), maxColumnsUnknownWidth)
	}

	const gap = 3

	used := 0

	for i, column := range columns {
		width := len([]rune(headings[i]))

		for _, row := range rows {
			if size := len([]rune(renderCell(row[column]))); size > width {
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
			return i
		}

		used = next
	}

	return len(columns)
}

// shorten heads a flattened column with its last segment where that is still
// unambiguous: project.id becomes ID, grant.owner becomes OWNER.
//
// The prefix is repeated on every column of a composite reply and says the same
// thing each time, while costing eight characters of a width that the id it
// sits above actually needs. Where two branches share a leaf — project.id
// beside grant.id — both keep their prefix, because there the prefix is the
// whole of the difference.
func shorten(columns []string) []string {
	leaves := map[string]int{}
	for _, column := range columns {
		leaves[lastSegment(column)]++
	}

	headings := make([]string, len(columns))

	for i, column := range columns {
		if leaf := lastSegment(column); leaves[leaf] == 1 {
			headings[i] = leaf

			continue
		}

		headings[i] = column
	}

	return headings
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

	columns = dropEmptyColumns(columns, rows)

	sort.Slice(columns, func(i, j int) bool {
		return sortsBefore(columns[i], columns[j])
	})

	return columns
}

// dropEmptyColumns drops the columns that are empty in every row.
//
// A column of nothing but "-" is not data, and here it is expensive: it holds
// width that a column with something in it then cannot have, so ban_reason
// being null everywhere is what pushes created_at off the screen.
//
// Dropped silently, unlike a column trimmed for width. The rule this CLI
// follows is that you are told when you might be missing something, and there
// is nothing to miss: -o json shows the same field as null. Naming these on
// stderr would report an absence as if it were a loss.
func dropEmptyColumns(columns []string, rows []map[string]any) []string {
	kept := make([]string, 0, len(columns))

	for _, column := range columns {
		for _, row := range rows {
			if !blank(row[column]) {
				kept = append(kept, column)

				break
			}
		}
	}

	// Every column empty is a reply with no scalars in it at all. Printing no
	// header would look like a broken table rather than an empty one.
	if len(kept) == 0 {
		return columns
	}

	return kept
}

func blank(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	default:
		return false
	}
}

// writeObject prints one field per line. Laid out horizontally, a record with
// twenty fields wraps, and once it wraps the headers no longer line up.
func (p *Printer) writeObject(object map[string]any) error {
	writer := newTabWriter(p.Out)

	for _, key := range sortKeys(object) {
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

		if _, err := fmt.Fprintf(writer, "%s\t%s\n", key, renderCell(value)); err != nil {
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

func renderCell(value any) string {
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
