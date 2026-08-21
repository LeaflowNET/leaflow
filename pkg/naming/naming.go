// Package naming holds the CLI's one naming rule, shared by the command tree
// and by leaflow-doctor. Duplicating it would mean the collision check ran
// against a different rule than the tree it is checking.
package naming

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kebab lowercases an identifier and hyphenates word boundaries.
//
// It is the only naming rule in this CLI, and it is deliberately a mechanical
// transform rather than an interpretation. An operationId is already kebab
// (`create-disk` maps to itself); a tag is CamelCase (`FloatingIP` becomes
// `floating-ip`).
//
// Nothing is inferred from the value. Splitting `create-disk` into a `disk`
// group and a `create` verb would read better, but it would depend on a naming
// habit upstream, and when that habit changed the command name would move
// silently underneath scripts that already use it. Command names here can only
// change when the contract's own identifiers change — which breaks the
// generated SDKs too, so it is not something that happens quietly.
func Kebab(value string) string {
	value = expandAcronyms(value)

	var builder strings.Builder

	var previous rune

	for i, current := range value {
		if previous != 0 && unicode.IsUpper(current) {
			var next rune
			if j := i + utf8.RuneLen(current); j < len(value) {
				next, _ = utf8.DecodeRuneInString(value[j:])
			}

			// Break after a lowercase run (webCheck) and at the end of an acronym
			// followed by a word (SSHKey -> ssh-key), but not inside one (SLA).
			if unicode.IsLower(previous) || (unicode.IsUpper(previous) && unicode.IsLower(next)) {
				builder.WriteRune('-')
			}
		}

		builder.WriteRune(unicode.ToLower(current))
		previous = current
	}

	return slug(builder.String())
}

// expandAcronyms keeps initialisms that are followed by more capitals from
// collapsing into the next word.
var acronyms = []struct {
	from, to string
}{
	{"OAuth", "Oauth"},
	{"APIs", "Apis"},
	{"API", "Api"},
	{"URLs", "Urls"},
	{"URL", "Url"},
	{"IDs", "Ids"},
	{"JSON", "Json"},
}

func expandAcronyms(value string) string {
	for _, pair := range acronyms {
		value = strings.ReplaceAll(value, pair.from, pair.to)
	}

	return value
}

// slug reduces anything that is not a letter or digit to a single hyphen, so a
// tag with a space or a slash still yields a usable command name.
func slug(value string) string {
	var builder strings.Builder

	dashed := false

	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			builder.WriteRune(current)
			dashed = false

			continue
		}

		if !dashed && builder.Len() > 0 {
			builder.WriteRune('-')
			dashed = true
		}
	}

	return strings.Trim(builder.String(), "-")
}
