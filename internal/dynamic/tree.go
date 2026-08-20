package dynamic

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/internal/naming"
	"github.com/LeaflowNET/leaflow/internal/spec"
)

// Build assembles the command tree straight from the contracts:
//
//	leaflow <service> <tag> <operation-id>
//	leaflow compute    disk  create-disk
//
// Both name parts are literal values from the contract, lowercased and
// hyphenated. Nothing is inferred, so nothing can drift: an operationId is
// already treated as a stable identifier — the Go and TypeScript SDKs generate
// their method names from it — and a command name is now the same identifier
// rather than a second vocabulary that has to be kept in sync with it.
//
// It also means one identifier serves the CLI, the SDKs and an MCP tool name,
// so a new backend operation becomes reachable everywhere at once, with no
// table to update.
//
// Commands written in code are layered on top and may replace any of these; see
// package extension.
func Build(specs *spec.Set, rt *Runtime, ext *extension.Context) []*cobra.Command {
	services := map[string]*cobra.Command{}

	var roots []*cobra.Command

	for _, service := range specs.Services() {
		cmd := &cobra.Command{
			Use:         service.Name,
			Short:       serviceSummary(service),
			Long:        serviceDetails(service),
			Annotations: map[string]string{"contract-tags": tagDescriptions(service)},
		}

		addOperations(cmd, service, rt)

		services[service.Name] = cmd
		roots = append(roots, cmd)
	}

	sort.Slice(roots, func(i, j int) bool { return roots[i].Use < roots[j].Use })

	return append(roots, applyExtensions(services, ext)...)
}

func addOperations(root *cobra.Command, service *spec.Service, rt *Runtime) {
	groups := map[string]*cobra.Command{}

	for _, op := range service.Operations() {
		parent := root

		if len(op.Tags) > 0 {
			parent = ensureGroup(root, groups, op.Tags[0])
		}

		name := naming.Kebab(op.ID)

		// operationIds are unique within a contract, so two commands can only
		// collide with a group name. Renaming either would invent a name that is
		// in no contract, so the operation keeps its own and the group yields.
		if existing := findChild(parent, name); existing != nil && !existing.Runnable() {
			existing.Use = name + "-group"
		}

		parent.AddCommand(NewCommand(name, op, rt))
	}
}

func ensureGroup(root *cobra.Command, groups map[string]*cobra.Command, tag string) *cobra.Command {
	name := naming.Kebab(tag)
	if name == "" {
		return root
	}

	if group, ok := groups[name]; ok {
		return group
	}

	group := &cobra.Command{
		Use: name,
		// Short is whatever the contract says about this tag, and nothing when it
		// says nothing. "operations tagged Account" is not a description, it is
		// the command name restated as a sentence.
		Short: tagSummary(root, tag),
	}

	groups[name] = group
	root.AddCommand(group)

	return group
}

// tagSummary looks up the tag's description in the contract's top-level tags
// list. Adding one upstream is what makes these groups self-describing.
func tagSummary(root *cobra.Command, tag string) string {
	doc, ok := root.Annotations["contract-tags"]
	if !ok || doc == "" {
		return ""
	}

	for _, entry := range strings.Split(doc, "\n") {
		name, description, found := strings.Cut(entry, "\t")
		if found && name == tag {
			return description
		}
	}

	return ""
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}

	return nil
}

// applyExtensions attaches code-written commands, replacing generated ones of
// the same name, and returns those belonging at the top level.
func applyExtensions(services map[string]*cobra.Command, ext *extension.Context) []*cobra.Command {
	if ext == nil {
		return nil
	}

	var top []*cobra.Command

	for _, declared := range extension.All() {
		built := declared.Build(ext)
		if built == nil {
			continue
		}

		if declared.Service == "" {
			top = append(top, built)

			continue
		}

		// Declared under a service with no contract bundled. Skipping beats
		// crashing: one missing contract should not stop the CLI from starting.
		parent, ok := services[declared.Service]
		if !ok {
			continue
		}

		for i := 0; i+1 < len(declared.Path); i++ {
			child := findChild(parent, declared.Path[i])
			if child == nil {
				child = &cobra.Command{Use: declared.Path[i]}
				parent.AddCommand(child)
			}

			parent = child
		}

		if len(declared.Path) > 0 {
			if existing := findChild(parent, declared.Path[len(declared.Path)-1]); existing != nil {
				parent.RemoveCommand(existing)
			}
		}

		parent.AddCommand(built)
	}

	return top
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
		builder.WriteString(firstLine(tag.Description))
		builder.WriteString("\n")
	}

	return builder.String()
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index > 0 {
		text = text[:index]
	}

	return text
}

func serviceSummary(service *spec.Service) string {
	if service.Doc != nil && service.Doc.Info != nil && service.Doc.Info.Title != "" {
		return service.Doc.Info.Title
	}

	return service.Name
}

func serviceDetails(service *spec.Service) string {
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
