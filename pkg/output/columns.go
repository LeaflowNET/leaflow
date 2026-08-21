package output

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	ErrBadColumnSpec = errors.New("malformed column specification")

	ErrBadFieldPath = errors.New("malformed field path")
)

// Column is one user-defined table column, as in
// -o custom-columns=NAME:.name,SIZE:.size_gb
//
// The syntax follows kubectl's, deliberately: it is the one people already
// know, and it is small enough to specify exactly — a dotted field path with
// optional array indices, not general JSONPath.
type Column struct {
	Header string
	Path   []step
}

// step is either a field name or an array index.
type step struct {
	field string
	index int
	isIdx bool
}

// ParseColumns reads `HEADER:.path,HEADER:.path`.
func ParseColumns(text string) ([]Column, error) {
	var columns []Column

	for part := range strings.SplitSeq(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		header, path, found := strings.Cut(part, ":")
		if !found {
			return nil, fmt.Errorf("%w %q: expected HEADER:.path", ErrBadColumnSpec, part)
		}

		steps, err := parsePath(strings.TrimSpace(path))
		if err != nil {
			return nil, err
		}

		columns = append(columns, Column{
			Header: strings.TrimSpace(header),
			Path:   steps,
		})
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("%w: no columns given", ErrBadColumnSpec)
	}

	return columns, nil
}

func parsePath(path string) ([]step, error) {
	trimmed := strings.TrimPrefix(path, ".")
	if trimmed == "" {
		return nil, fmt.Errorf("%w: %q is empty", ErrBadFieldPath, path)
	}

	var steps []step

	for segment := range strings.SplitSeq(trimmed, ".") {
		name, rest, hasIndex := strings.Cut(segment, "[")

		if name != "" {
			steps = append(steps, step{
				field: name,
			})
		}

		if !hasIndex {
			continue
		}

		digits, after, closed := strings.Cut(rest, "]")
		if !closed || after != "" {
			return nil, fmt.Errorf("%w: %q", ErrBadFieldPath, path)
		}

		index, err := strconv.Atoi(digits)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an index in %q", ErrBadFieldPath, digits, path)
		}

		steps = append(steps, step{
			index: index,
			isIdx: true,
		})
	}

	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: %q is empty", ErrBadFieldPath, path)
	}

	return steps, nil
}

// Resolve walks the path. A missing field yields nil, which prints as "-":
// absent is a normal state across a list of mixed resources, not an error.
func (c Column) Resolve(value any) any {
	current := value

	for _, one := range c.Path {
		if one.isIdx {
			list, ok := current.([]any)
			if !ok || one.index < 0 || one.index >= len(list) {
				return nil
			}

			current = list[one.index]

			continue
		}

		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}

		current = object[one.field]
	}

	return current
}

func (p *Printer) writeCustomColumns(rows []map[string]any) error {
	writer := newTabWriter(p.Out)

	headers := make([]string, len(p.Columns))
	for i, column := range p.Columns {
		headers[i] = strings.ToUpper(column.Header)
	}

	if _, err := fmt.Fprintln(writer, strings.Join(headers, "\t")); err != nil {
		return err
	}

	for _, row := range rows {
		cells := make([]string, len(p.Columns))
		for i, column := range p.Columns {
			cells[i] = renderCell(column.Resolve(row))
		}

		if _, err := fmt.Fprintln(writer, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}

	return writer.Flush()
}
