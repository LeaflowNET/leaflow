package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/LeaflowNET/leaflow/cmd/leaflow/internal/builtin"
	"github.com/LeaflowNET/leaflow/cmd/leaflow/internal/dynamic"
	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/pkg/leaflow"
	"github.com/LeaflowNET/leaflow/pkg/mcp"
	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/transport"
	"github.com/LeaflowNET/leaflow/pkg/validate"
)

// Exit codes are part of the CLI's contract, because this tool is meant to be
// wrapped by other programs. A caller has to be able to tell "log in again"
// from "you cannot do that" from "the service is down" without parsing prose.
const (
	ExitOK = 0

	// ExitError is anything not classified below.
	ExitError = 1

	// ExitUsage is a malformed command line: unknown flag, wrong arity.
	ExitUsage = 2

	// ExitAuth means no usable credentials. Retrying without logging in again
	// cannot help.
	ExitAuth = 3

	// ExitPermission means the identity is fine but not allowed.
	ExitPermission = 4

	ExitNotFound = 5

	// ExitValidation is a local failure: the request was never sent.
	ExitValidation = 6

	// ExitAPI is a service-side refusal that is none of the above.
	ExitAPI = 7

	// ExitInterrupted follows the shell convention of 128 + SIGINT.
	ExitInterrupted = 130
)

// Failure is the machine-readable form of an error, emitted under -o json and
// -o yaml so a wrapping program gets a parseable object instead of a sentence.
type Failure struct {
	// Kind is the coarse category, matching the exit code.
	Kind string `json:"kind"`

	// Code is the service's own error code (NOT_A_MEMBER, TOKEN_EXPIRED) when
	// there is one. This is contract; Message is not.
	Code string `json:"code,omitempty"`

	Status int `json:"status,omitempty"`

	Message string `json:"message"`

	// Problems carries every validation failure, since they are reported
	// together rather than one per run.
	Problems []string `json:"problems,omitempty"`

	// Hint is the next action, when there is an obvious one.
	Hint string `json:"hint,omitempty"`

	ExitCode int `json:"exit_code"`
}

// classify maps an error onto a Failure and its exit code.
func classify(err error) Failure {
	var (
		apiErr      *transport.APIError
		validateErr *validate.Error
	)

	switch {
	case errors.Is(err, context.Canceled):
		return Failure{
			Kind:     "interrupted",
			Message:  "interrupted",
			ExitCode: ExitInterrupted,
		}

	case errors.Is(err, auth.ErrNoCredentials):
		return Failure{
			Kind:     "auth",
			Message:  "not logged in",
			Hint:     "leaflow login",
			ExitCode: ExitAuth,
		}

	case errors.Is(err, auth.ErrNeedProject):
		return Failure{
			Kind:     "auth",
			Message:  "no project selected",
			Hint:     "leaflow project use <project>, or pass --project / LEAFLOW_PROJECT",
			ExitCode: ExitAuth,
		}

	case errors.Is(err, auth.ErrAccountTokenInCI):
		return Failure{
			Kind:     "auth",
			Message:  err.Error(),
			ExitCode: ExitAuth,
		}

	case errors.Is(err, auth.ErrNotAMember):
		return Failure{
			Kind:     "permission",
			Message:  err.Error(),
			Hint:     "leaflow project list",
			ExitCode: ExitPermission,
		}

	case errors.As(err, &validateErr):
		return Failure{
			Kind:     "validation",
			Message:  "invalid arguments",
			Problems: validateErr.Problems,
			ExitCode: ExitValidation,
		}

	case errors.As(err, &apiErr):
		return classifyAPI(apiErr)

	case errors.Is(err, dynamic.ErrMissingArguments),
		errors.Is(err, dynamic.ErrTooManyArguments),
		errors.Is(err, config.ErrConfigNotFound),
		errors.Is(err, config.ErrConfigMalformed),
		errors.Is(err, output.ErrUnknownFormat),
		errors.Is(err, output.ErrBadColumnSpec),
		errors.Is(err, output.ErrBadFieldPath),
		errors.Is(err, mcp.ErrUnknownMode),
		errors.Is(err, leaflow.ErrNoSuchService),
		errors.Is(err, builtin.ErrAddressNotLocal):
		return Failure{
			Kind:     "usage",
			Message:  err.Error(),
			ExitCode: ExitUsage,
		}
	}

	return Failure{
		Kind:     "error",
		Message:  err.Error(),
		ExitCode: ExitError,
	}
}

// classifyAPI turns a service refusal into an exit code.
//
// The kinds come from the library rather than from a second reading of the
// codes and statuses here: one mapping, so that a caller branching on
// leaflow.ErrPermissionDenied and a script branching on exit code 4 are asking
// the same question and cannot start disagreeing.
func classifyAPI(err *transport.APIError) Failure {
	failure := Failure{
		Kind:     "api",
		Code:     err.Code,
		Status:   err.Status,
		Message:  err.Error(),
		ExitCode: ExitAPI,
	}

	switch {
	case errors.Is(err, leaflow.ErrUnauthenticated):
		failure.Kind = "auth"
		failure.Hint = "leaflow login"
		failure.ExitCode = ExitAuth
	case errors.Is(err, leaflow.ErrPermissionDenied):
		failure.Kind = "permission"
		failure.ExitCode = ExitPermission

		if err.Code == "NOT_A_MEMBER" {
			failure.Hint = "leaflow project list"
		}
	case errors.Is(err, leaflow.ErrNotFound):
		failure.Kind = "not_found"
		failure.ExitCode = ExitNotFound
	case errors.Is(err, leaflow.ErrInvalidArgument):
		failure.Kind = "validation"
		failure.ExitCode = ExitValidation
	}

	return failure
}

// report writes the failure and returns the exit code.
//
// Errors always go to stderr, in every format, so that stdout carries only the
// command's data and a wrapper can pipe the two apart.
func (a *App) report(err error, format output.Format) int {
	failure := classify(err)

	// Interruption is not a failure; exiting quietly avoids printing something
	// that reads like a crash after Ctrl-C.
	if failure.ExitCode == ExitInterrupted {
		return ExitInterrupted
	}

	switch format {
	case output.FormatJSON, output.FormatYAML:
		printer := &output.Printer{
			Format: format,
			Out:    a.Err,
		}
		if printErr := printer.Print(map[string]any{
			"error": failure,
		}); printErr == nil {
			return failure.ExitCode
		}
	}

	writeHuman(a.Err, failure)

	return failure.ExitCode
}

func writeHuman(out io.Writer, failure Failure) {
	if len(failure.Problems) > 0 {
		fmt.Fprintln(out, "error:", failure.Message)

		for _, problem := range failure.Problems {
			fmt.Fprintln(out, "      ", problem)
		}
	} else {
		fmt.Fprintln(out, "error:", failure.Message)
	}

	if failure.Hint != "" {
		fmt.Fprintln(out, "hint: ", failure.Hint)
	}
}
