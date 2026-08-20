// Command leaflow-doctor checks the CLI against the contracts it ships with.
//
// It is a separate binary rather than a `leaflow doctor` subcommand so that it
// cannot reach a production build: there is no build tag to remember and no
// release flag to get wrong. CI runs `go run ./cmd/leaflow-doctor`.
//
// What it checks is what cannot be derived from a contract and therefore has to
// be written down somewhere:
//
//   - command name collisions, which would otherwise surface as a cobra panic
//     on a user's machine.
//   - operations with no operationId, which cannot be named and are silently
//     skipped at runtime.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/LeaflowNET/leaflow/internal/naming"
	"github.com/LeaflowNET/leaflow/internal/spec"
)

func main() {
	// -commands prints the command surface the embedded contracts produce.
	//
	// Taken before and after a contract sync, the two lists diff into something
	// a reviewer can act on — three commands added, one removed — instead of a
	// few thousand lines of YAML in which a deleted operation is invisible.
	commands := flag.Bool("commands", false, "print every command the embedded contracts produce")
	flag.Parse()

	if *commands {
		if err := printCommands(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

		return
	}

	problems, err := check()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if len(problems) == 0 {
		fmt.Println("ok")

		return
	}

	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}

	fmt.Fprintf(os.Stderr, "\n%d problem(s)\n", len(problems))
	os.Exit(1)
}

func printCommands() error {
	specs, err := spec.Load()
	if err != nil {
		return err
	}

	var lines []string

	for _, service := range specs.Services() {
		for _, op := range service.Operations() {
			parts := []string{service.Name}
			if len(op.Tags) > 0 {
				parts = append(parts, naming.Kebab(op.Tags[0]))
			}

			parts = append(parts, naming.Kebab(op.ID))

			lines = append(lines, fmt.Sprintf("%s\t%s\t%s",
				service.Name, op.ID, strings.Join(parts, " ")))
		}
	}

	sort.Strings(lines)

	for _, line := range lines {
		fmt.Println(line)
	}

	return nil
}

func check() ([]string, error) {
	specs, err := spec.Load()
	if err != nil {
		return nil, err
	}

	var problems []string

	problems = append(problems, checkCollisions(specs)...)
	problems = append(problems, checkMissingIDs(specs)...)

	sort.Strings(problems)

	return problems, nil
}

// checkCollisions mirrors the tree builder, where a tag becomes a group command
// and an operationId becomes a command. Two names that collide make cobra
// shadow one of them, and that would happen on a user's machine.
func checkCollisions(specs *spec.Set) []string {
	var problems []string

	for _, service := range specs.Services() {
		tagsByName := map[string]string{}
		rootOperations := map[string]string{}
		perGroup := map[string]map[string]string{}

		for _, op := range service.Operations() {
			name := naming.Kebab(op.ID)

			if len(op.Tags) == 0 {
				// Untagged operations sit at the service root, next to the groups.
				rootOperations[name] = op.ID

				continue
			}

			tag := op.Tags[0]
			group := naming.Kebab(tag)

			if previous, seen := tagsByName[group]; seen && previous != tag {
				problems = append(problems, fmt.Sprintf(
					"%s: tags %q and %q both become group %q", service.Name, previous, tag, group))
			}

			tagsByName[group] = tag

			if perGroup[group] == nil {
				perGroup[group] = map[string]string{}
			}

			if previous, seen := perGroup[group][name]; seen {
				problems = append(problems, fmt.Sprintf(
					"%s: %q and %q both become %q", service.Name, previous, op.ID, group+" "+name))
			}

			perGroup[group][name] = op.ID
		}

		for group, tag := range tagsByName {
			if id, clash := rootOperations[group]; clash {
				problems = append(problems, fmt.Sprintf(
					"%s: tag %q and untagged operation %q both become %q",
					service.Name, tag, id, group))
			}
		}
	}

	return problems
}

func checkMissingIDs(specs *spec.Set) []string {
	var problems []string

	for _, service := range specs.Services() {
		declared := countOperations(service)

		if named := len(service.OperationIDs()); named < declared {
			problems = append(problems, fmt.Sprintf(
				"%s: %d operation(s) have no operationId and are unreachable", service.Name, declared-named))
		}
	}

	return problems
}

func countOperations(service *spec.Service) int {
	if service.Doc == nil || service.Doc.Paths == nil {
		return 0
	}

	total := 0

	for _, path := range service.Doc.Paths.InMatchingOrder() {
		if item := service.Doc.Paths.Find(path); item != nil {
			total += len(item.Operations())
		}
	}

	return total
}
