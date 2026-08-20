// Package extension is where a command is written in code.
//
// The contract-driven path turns every operation into a usable command, but it
// can only express "one operation, one command". Some things are not that
// shape:
//
//   - `instance start` is `act-on-instance` with one body field fixed
//   - `instance launch --wait` is a call followed by polling
//   - `login` and `project use` correspond to no operation at all
//
// These live here. They share the kernel with generated commands — same HTTP
// client, same printer, same config — so --output, --project and token renewal
// behave identically. A hand-written command should not feel like a different
// program.
//
// # Overriding
//
// When an extension's Path collides with a generated command, the extension
// wins. That is the point: it is how a command gets improved. The operation it
// displaces is still reachable as `leaflow <service> op <operationId>`.
package extension

import (
	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/internal/output"
	"github.com/LeaflowNET/leaflow/internal/spec"
	"github.com/LeaflowNET/leaflow/internal/transport"
)

// Context is what the kernel hands to an extension: capabilities, not raw
// material. Client already knows which token to attach and how to retry;
// Printer already knows the requested format.
type Context struct {
	Config  *config.Config
	Tokens  *auth.Manager
	Client  *transport.Client
	Printer *output.Printer
	Specs   *spec.Set
}

type Command struct {
	// Service is the service to hang this under, e.g. "compute". Empty means
	// top level, as for `leaflow login`.
	Service string

	// Path is the command path, so ["instance", "start"] becomes
	// `leaflow compute instance start`. Missing intermediate levels are created.
	Path []string

	// Build constructs the command. Everything in Context is usable by the time
	// it is called.
	Build func(*Context) *cobra.Command
}

// registry is populated from init(), so adding a command means adding a file
// rather than also registering it in a central list — the kind of list where a
// missed edit shows up as a command that mysteriously does not exist.
var registry []Command

func Register(commands ...Command) {
	registry = append(registry, commands...)
}

func All() []Command {
	out := make([]Command, len(registry))
	copy(out, registry)

	return out
}
