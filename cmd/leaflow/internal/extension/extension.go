// Package extension carries the kernel's capabilities to the commands that are
// not generated from a contract — signing in, choosing a project, configuring
// a context. Those correspond to no operation, so they are written by hand in
// package builtin.
//
// Commands that do correspond to an operation are never written by hand. One
// surface, one source.
package extension

import (
	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// Context is what the kernel hands to a command: capabilities, not raw
// material. Client already knows which token to attach and how to retry;
// Printer already knows the requested format.
type Context struct {
	Config  *config.Config
	Tokens  *auth.Manager
	Client  *transport.Client
	Printer *output.Printer
	Specs   *spec.Set
}
