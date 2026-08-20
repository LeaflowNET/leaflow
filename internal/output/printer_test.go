package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/LeaflowNET/leaflow/internal/output"
)

func render(t *testing.T, format output.Format, columns []output.Column, value any) string {
	t.Helper()

	var buffer bytes.Buffer

	printer := &output.Printer{Format: format, Columns: columns, Out: &buffer}
	if err := printer.Print(value); err != nil {
		t.Fatalf("printing: %v", err)
	}

	return buffer.String()
}

// A Go struct carries both json and yaml tags, and marshalling it directly
// would render different field names per format. Normalising first is what
// makes the formats describe one value.
func TestFormatsAgreeOnFieldNames(t *testing.T) {
	type status struct {
		LoggedIn bool   `json:"logged_in" yaml:"loggedIn"`
		Account  string `json:"account"   yaml:"acct"`
	}

	value := status{LoggedIn: true, Account: "someone@example.com"}

	var fromJSON map[string]any
	if err := json.Unmarshal([]byte(render(t, output.FormatJSON, nil, value)), &fromJSON); err != nil {
		t.Fatalf("json output is not valid json: %v", err)
	}

	var fromYAML map[string]any
	if err := yaml.Unmarshal([]byte(render(t, output.FormatYAML, nil, value)), &fromYAML); err != nil {
		t.Fatalf("yaml output is not valid yaml: %v", err)
	}

	for _, key := range []string{"logged_in", "account"} {
		if _, ok := fromJSON[key]; !ok {
			t.Errorf("json output is missing %q: %v", key, fromJSON)
		}

		if _, ok := fromYAML[key]; !ok {
			t.Errorf("yaml output is missing %q: %v", key, fromYAML)
		}
	}
}

func TestTableFindsTheListInAReply(t *testing.T) {
	reply := map[string]any{
		"items": []any{
			map[string]any{"id": "d-1", "name": "data", "size_gb": float64(20)},
			map[string]any{"id": "d-2", "name": "logs", "size_gb": float64(40)},
		},
	}

	got := render(t, output.FormatTable, nil, reply)

	if !strings.Contains(got, "ID") || !strings.Contains(got, "NAME") {
		t.Errorf("headers missing:\n%s", got)
	}

	if !strings.Contains(got, "d-1") || !strings.Contains(got, "logs") {
		t.Errorf("rows missing:\n%s", got)
	}

	// Whole numbers must not print as 20.000000.
	if strings.Contains(got, "20.0") {
		t.Errorf("integer rendered as a float:\n%s", got)
	}
}

// Two array fields are genuinely ambiguous, so the table falls back rather than
// guessing which one is the subject.
func TestTableFallsBackWhenTheListIsAmbiguous(t *testing.T) {
	reply := map[string]any{
		"items":  []any{map[string]any{"id": "a"}},
		"errors": []any{map[string]any{"id": "b"}},
	}

	got := render(t, output.FormatTable, nil, reply)

	if strings.Contains(got, "ID") {
		t.Errorf("expected a key/value rendering, got a table:\n%s", got)
	}
}

func TestEmptyListSaysSo(t *testing.T) {
	got := render(t, output.FormatTable, nil, map[string]any{"items": []any{}})

	if !strings.Contains(got, "empty") {
		t.Errorf("an empty list printed nothing useful: %q", got)
	}
}

func TestCustomColumns(t *testing.T) {
	columns, err := output.ParseColumns("NAME:.name,ZONE:.placement.zone,FIRST:.tags[0]")
	if err != nil {
		t.Fatalf("parsing columns: %v", err)
	}

	reply := map[string]any{"items": []any{
		map[string]any{
			"name":      "data",
			"placement": map[string]any{"zone": "az-1"},
			"tags":      []any{"prod", "db"},
		},
	}}

	got := render(t, output.FormatTable, columns, reply)

	for _, want := range []string{"NAME", "ZONE", "FIRST", "data", "az-1", "prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

// Across a list of mixed resources a field being absent is ordinary, so it
// prints as "-" rather than failing the whole command.
func TestCustomColumnsTolerateMissingFields(t *testing.T) {
	columns, err := output.ParseColumns("MISSING:.nope.deeper")
	if err != nil {
		t.Fatalf("parsing columns: %v", err)
	}

	got := render(t, output.FormatTable, columns, map[string]any{"items": []any{map[string]any{"id": "x"}}})

	if !strings.Contains(got, "-") {
		t.Errorf("missing field did not render as a placeholder:\n%s", got)
	}
}

func TestParseColumnsRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "NAME", "NAME:", "NAME:.tags[", "NAME:.tags[x]"} {
		if _, err := output.ParseColumns(bad); err == nil {
			t.Errorf("ParseColumns(%q) was accepted", bad)
		}
	}
}

func TestParseFormatAcceptsCustomColumns(t *testing.T) {
	format, columns, err := output.ParseFormat("custom-columns=NAME:.name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if format != output.FormatTable {
		t.Errorf("format = %q, want table", format)
	}

	if len(columns) != 1 || columns[0].Header != "NAME" {
		t.Errorf("columns = %#v", columns)
	}
}

func TestParseFormatRejectsUnknown(t *testing.T) {
	if _, _, err := output.ParseFormat("xml"); err == nil {
		t.Error("an unknown format was accepted")
	}
}

// A reply whose rows are entirely composite — a project listing is
// {grant, project} per row — has no top-level scalar, and picking only those
// leaves zero columns and prints an empty table.
func TestNestedOnlyRowsAreFlattened(t *testing.T) {
	reply := map[string]any{"items": []any{
		map[string]any{
			"grant":   map[string]any{"owner": true},
			"project": map[string]any{"id": "p-1", "name": "twilight"},
		},
	}}

	got := render(t, output.FormatTable, nil, reply)

	for _, want := range []string{"PROJECT.ID", "PROJECT.NAME", "twilight"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

// Identifying columns stay first after flattening: ranking uses the last
// segment, so project.name outranks project.created_at.
func TestFlattenedColumnsKeepTheirRanking(t *testing.T) {
	reply := map[string]any{"items": []any{
		map[string]any{"project": map[string]any{
			"created_at": "2026-01-01T00:00:00Z",
			"name":       "twilight",
			"id":         "p-1",
		}},
	}}

	got := render(t, output.FormatTable, nil, reply)
	header := strings.SplitN(got, "\n", 2)[0]

	if strings.Index(header, "PROJECT.ID") > strings.Index(header, "PROJECT.CREATED_AT") {
		t.Errorf("id should come before created_at: %q", header)
	}
}

// Trailing columns are dropped rather than cells truncated: a cut-off id looks
// copyable and is not.
func TestWideTablesAreTrimmedToWidth(t *testing.T) {
	var out, notice bytes.Buffer

	printer := &output.Printer{
		Format: output.FormatTable,
		Out:    &out,
		Width:  40,
		Notice: &notice,
	}

	if err := printer.Print(map[string]any{"items": []any{
		map[string]any{"id": "0123456789", "name": "abc", "extra": "0123456789012345678901234567890"},
	}}); err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line exceeds the width: %q", line)
		}
	}

	if !strings.Contains(notice.String(), "more column") {
		t.Errorf("dropped columns were not reported: %q", notice.String())
	}
}

// A pipe has no width, so nothing is trimmed to fit a screen nobody is reading.
func TestZeroWidthKeepsColumnsUpToTheCap(t *testing.T) {
	row := map[string]any{}
	for i := range 6 {
		row[string(rune('a'+i))] = strings.Repeat("x", 30)
	}

	got := render(t, output.FormatTable, nil, map[string]any{"items": []any{row}})
	header := strings.SplitN(got, "\n", 2)[0]

	if len([]rune(header)) < 100 {
		t.Errorf("expected a wide untrimmed header, got %q", header)
	}
}

// Every format must present fields in one order. Go sorts map keys
// alphabetically, which used to put availability_zone ahead of id in JSON while
// the table led with id — the same value read two different ways.
func TestAllFormatsShareOneFieldOrder(t *testing.T) {
	reply := map[string]any{"items": []any{map[string]any{
		"availability_zone": "hk-1",
		"created_at":        "2026-08-16T22:05:36Z",
		"hostname":          "h-1",
		"id":                "i-1",
		"name":              "test",
		"status":            "running",
	}}}

	jsonOrder := keyOrder(t, render(t, output.FormatJSON, nil, reply))
	yamlOrder := keyOrder(t, render(t, output.FormatYAML, nil, reply))

	if strings.Join(jsonOrder, ",") != strings.Join(yamlOrder, ",") {
		t.Errorf("json %v and yaml %v disagree", jsonOrder, yamlOrder)
	}

	tableHeader := strings.SplitN(render(t, output.FormatTable, nil, reply), "\n", 2)[0]
	tableOrder := strings.Fields(strings.ToLower(tableHeader))

	if strings.Join(jsonOrder, ",") != strings.Join(tableOrder, ",") {
		t.Errorf("json %v and table %v disagree", jsonOrder, tableOrder)
	}
}

// Identity first, timestamps last, the rest alphabetical. created_at is ranked
// so that it sinks; a two-band comparison floated it above ordinary fields.
func TestFieldOrderPutsIdentityFirstAndTimestampsLast(t *testing.T) {
	reply := map[string]any{"items": []any{map[string]any{
		"created_at": "2026-08-16T22:05:36Z",
		"hostname":   "h-1",
		"id":         "i-1",
		"name":       "test",
		"status":     "running",
		"updated_at": "2026-08-20T16:30:03Z",
	}}}

	got := keyOrder(t, render(t, output.FormatJSON, nil, reply))
	want := []string{"id", "name", "status", "hostname", "created_at", "updated_at"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// The hand-rolled encoder must still produce valid JSON, including escaping.
func TestOrderedJSONStaysValid(t *testing.T) {
	value := map[string]any{
		"name":   `quotes " and \ backslash`,
		"nested": map[string]any{"empty": map[string]any{}, "list": []any{}},
		"items":  []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
		"n":      float64(20),
		"null":   nil,
		"bool":   true,
	}

	encoded := render(t, output.FormatJSON, nil, value)

	var back map[string]any
	if err := json.Unmarshal([]byte(encoded), &back); err != nil {
		t.Fatalf("output is not valid json: %v\n%s", err, encoded)
	}

	if back["name"] != value["name"] {
		t.Errorf("escaping changed the value: %q", back["name"])
	}

	// < > & are left unescaped so jq and human readers see them as written.
	unescaped := render(t, output.FormatJSON, nil, map[string]any{"u": "a<b&c"})
	if strings.Contains(unescaped, `\u003c`) || !strings.Contains(unescaped, "a<b&c") {
		t.Errorf("HTML escaping leaked into the output: %s", unescaped)
	}
}

func keyOrder(t *testing.T, encoded string) []string {
	t.Helper()

	var order []string

	for _, line := range strings.Split(encoded, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "- ")

		key, _, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}

		key = strings.Trim(key, `"`)
		if key == "" || key == "items" || strings.Contains(key, " ") {
			continue
		}

		order = append(order, key)
	}

	return order
}
