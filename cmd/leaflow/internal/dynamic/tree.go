package dynamic

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"
)

// Build assembles the command tree straight from the contracts:
//
//	leaflow <service> <operation-id>
//	leaflow compute    create-disk
//
// A service split by function into subpackages adds one level, named after the
// subpackage, because that is how its contracts, its Go import paths and its
// TypeScript namespaces are named too:
//
//	leaflow <service> <subpackage> <operation-id>
//	leaflow billing   catalog      list-prices
//
// The name is a literal value from the contract, lowercased and hyphenated.
// Nothing is inferred, so nothing can drift: an operationId is already treated
// as a stable identifier — the Go and TypeScript SDKs generate their method
// names from it — and a command name is now that same identifier rather than a
// second vocabulary to keep in sync with it. One identifier therefore serves
// the CLI, the SDKs and an MCP tool name alike.
//
// The contract's tag is not a level in the path. operationIds are unique within
// a contract, so a tag adds nothing to addressing and quite a lot of noise to
// typing: `compute disk create-disk` says disk twice. Tags become help groups
// instead, so `leaflow compute --help` still reads as a set of resources rather
// than eighty-three flat lines.
//
// Every command comes from a contract. Nothing is hand-written on top: a
// hand-written command is a second source for the same surface, and then the
// question "is this one generated" has to be answered per command. Where the
// contract makes something awkward — a verb that lives in the request body —
// the fix belongs upstream in the contract, not in a local exception.
func Build(specs *spec.Set, rt *Runtime) []*cobra.Command {
	var roots []*cobra.Command

	// parents holds the command each subpackage is attached to: the service's own
	// command when it also has a contract, otherwise a group made for the purpose.
	parents := map[string]*cobra.Command{}
	subpackages := map[string][]*spec.Service{}

	for _, service := range specs.Services() {
		cmd := &cobra.Command{
			Use:   path.Base(service.Name),
			Short: writeServiceSummary(service),
			Long:  writeServiceDetails(service),
			Annotations: map[string]string{
				"contract-tags": tagDescriptions(service),
			},
		}

		addOperations(cmd, service, rt)

		head, _, nested := strings.Cut(service.Name, "/")
		if !nested {
			parents[service.Name] = cmd
			roots = append(roots, cmd)

			continue
		}

		parent, ok := parents[head]
		if !ok {
			parent = &cobra.Command{Use: head}
			parents[head] = parent
			roots = append(roots, parent)
		}

		parent.AddCommand(cmd)
		subpackages[head] = append(subpackages[head], service)
	}

	for head, services := range subpackages {
		if parent := parents[head]; parent.Short == "" {
			parent.Short = writeGroupSummary(head, services)
		}
	}

	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Use < roots[j].Use
	})

	return roots
}

func addOperations(root *cobra.Command, service *spec.Service, rt *Runtime) {
	// Groups are declared up front and in order, because cobra prints them in
	// the order they were added — and adding them as operations are walked would
	// order the headings by whichever operationId happened to sort first.
	for _, tag := range listServiceTags(service) {
		root.AddGroup(&cobra.Group{
			ID:    tag,
			Title: groupTitle(root, tag),
		})
	}

	for _, op := range service.Operations() {
		cmd := NewCommand(naming.Kebab(op.ID), op, rt)

		if len(op.Tags) > 0 {
			cmd.GroupID = op.Tags[0]
		}

		root.AddCommand(cmd)
	}
}

// listServiceTags lists a service's tags in the contract's own order, falling back
// to alphabetical for any the contract does not declare. The contract's order
// is editorial — it puts the primary resource first — and alphabetising would
// throw that away.
func listServiceTags(service *spec.Service) []string {
	seen := map[string]bool{}

	var declared []string

	if service.Doc != nil {
		for _, tag := range service.Doc.Tags {
			if tag != nil && tag.Name != "" && !seen[tag.Name] {
				seen[tag.Name] = true
				declared = append(declared, tag.Name)
			}
		}
	}

	var rest []string

	for _, op := range service.Operations() {
		if len(op.Tags) == 0 || seen[op.Tags[0]] {
			continue
		}

		seen[op.Tags[0]] = true
		rest = append(rest, op.Tags[0])
	}

	sort.Strings(rest)

	return append(declared, rest...)
}

// groupTitle is the heading a tag gets in help. The contract's own description
// is used when it has one; otherwise the tag itself, because "operations tagged
// Account" is not a description, it is the heading restated as a sentence.
func groupTitle(root *cobra.Command, tag string) string {
	if summary := tagSummary(root, tag); summary != "" {
		return summary
	}

	return tag
}

// tagSummary looks up the tag's description in the contract's top-level tags
// list. Adding one upstream is what makes these groups self-describing.
func tagSummary(root *cobra.Command, tag string) string {
	doc, ok := root.Annotations["contract-tags"]
	if !ok || doc == "" {
		return ""
	}

	for entry := range strings.SplitSeq(doc, "\n") {
		name, description, found := strings.Cut(entry, "\t")
		if found && name == tag {
			return description
		}
	}

	return ""
}

// tagDescriptions flattens the contract's tag list into the annotation the
// group builder reads.
func tagDescriptions(service *spec.Service) string {
	if service.Doc == nil {
		return ""
	}

	var builder strings.Builder

	for _, tag := range service.Doc.Tags {
		if tag == nil || tag.Description == "" {
			continue
		}

		builder.WriteString(tag.Name)
		builder.WriteString("\t")
		builder.WriteString(cutAtNewline(tag.Description))
		builder.WriteString("\n")
	}

	return builder.String()
}

func cutAtNewline(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index > 0 {
		text = text[:index]
	}

	return text
}

// writeGroupSummary describes a service that exists only as subpackages, from
// the words its subpackages' titles share ("Leaflow Billing Catalog API" and
// "Leaflow Billing Account API" share "Leaflow Billing") followed by the
// subpackages themselves. There is no contract to take a title from, and a
// hand-written one would be the only text in the tree not read from a contract.
func writeGroupSummary(head string, services []*spec.Service) string {
	var shared []string

	names := make([]string, 0, len(services))

	for index, service := range services {
		names = append(names, path.Base(service.Name))

		words := strings.Fields(writeServiceSummary(service))
		if index == 0 {
			shared = words

			continue
		}

		length := 0
		for length < len(shared) && length < len(words) && shared[length] == words[length] {
			length++
		}

		shared = shared[:length]
	}

	title := strings.Join(shared, " ")
	if title == "" {
		title = head
	}

	sort.Strings(names)

	return title + ": " + strings.Join(names, ", ")
}

func writeServiceSummary(service *spec.Service) string {
	if service.Doc != nil && service.Doc.Info != nil && service.Doc.Info.Title != "" {
		return service.Doc.Info.Title
	}

	return service.Name
}

func writeServiceDetails(service *spec.Service) string {
	var builder strings.Builder

	if service.Doc != nil && service.Doc.Info != nil {
		if summary := strings.TrimSpace(service.Doc.Info.Description); summary != "" {
			if index := strings.Index(summary, "\n\n"); index > 0 {
				summary = summary[:index]
			}

			builder.WriteString(summary)
			builder.WriteString("\n\n")
		}
	}

	fmt.Fprintf(&builder, "contract: %s", service.Version)

	return builder.String()
}
