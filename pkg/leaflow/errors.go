package leaflow

import (
	"bytes"
	"errors"

	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/transport"
	"github.com/LeaflowNET/leaflow/pkg/validate"
)

// What a failure can be, reached with errors.Is.
//
// A caller has to be able to tell these apart without reading prose: the
// wording of a refusal changes between releases, and matching on it is how a
// program starts behaving differently after a deployment nobody told it about.
// Three of these call for opposite responses — get another token, give up, wait
// and try again — so collapsing them into "the call failed" throws away the
// only part that decides what happens next.
//
//	errors.Is(err, leaflow.ErrTokenExpired)      // mint a fresh one, call again
//	errors.Is(err, leaflow.ErrPermissionDenied)  // no retry will help
//	errors.Is(err, leaflow.ErrInvalidArgument)   // the arguments were wrong
//
// They are aliases of the transport's, so a caller that reaches one through
// either package is asking the same question.
var (
	// ErrUnauthenticated means the identity was never established.
	ErrUnauthenticated = transport.ErrUnauthenticated

	// ErrTokenExpired is the one worth retrying: the credential was good and is
	// not any more. It also satisfies ErrUnauthenticated.
	ErrTokenExpired = transport.ErrTokenExpired

	// ErrPermissionDenied means the identity is fine and not allowed.
	ErrPermissionDenied = transport.ErrPermissionDenied

	ErrNotFound = transport.ErrNotFound

	// ErrInvalidArgument covers both the contract refusing arguments here and
	// the service refusing them there.
	ErrInvalidArgument = transport.ErrInvalidArgument

	ErrConflict = transport.ErrConflict

	ErrRateLimited = transport.ErrRateLimited

	// ErrUnavailable is the service failing rather than refusing, including a
	// request that never got an answer at all.
	ErrUnavailable = transport.ErrUnavailable
)

// Problems returns the individual complaints behind a failed call, when the
// failure was about the arguments.
//
// The contract checks every argument before anything is sent and reports all of
// them at once, because a caller fixing three fields in three round trips is
// three round trips slower — and where the caller is a model, each of those is
// a turn. Nil for any other kind of failure.
func Problems(err error) []string {
	var invalid *validate.Error
	if errors.As(err, &invalid) {
		return invalid.Problems
	}

	return nil
}

// Code returns the service's own error code, which is the part of a refusal
// that is contract. Empty when the failure did not come from the service or
// carried no code.
func Code(err error) string {
	var refused *transport.APIError
	if errors.As(err, &refused) {
		return refused.Code
	}

	return ""
}

// Status returns the HTTP status a refusal came back with, or zero when the
// failure never reached the service.
func Status(err error) int {
	var refused *transport.APIError
	if errors.As(err, &refused) {
		return refused.Status
	}

	return 0
}

// RenderError renders a failure as the XML a model reads, matching what
// Result.XML does for a reply.
//
// Here rather than in whichever surface happens to need it, so that a caller
// not going through MCP gets the same rendering — and so that the day APIError
// grows a field, every caller grows it too instead of one of them quietly
// falling behind.
//
// Returns text in every case: a caller handling a failure should not have to
// handle a second one from the attempt to describe the first.
func RenderError(err error, root string) string {
	if err == nil {
		return ""
	}

	if root == "" {
		root = "error"
	}

	payload := map[string]any{
		"message": err.Error(),
	}

	if problems := Problems(err); len(problems) > 0 {
		// Every complaint as its own element. The validator joins them for a
		// terminal, where the indentation lines the second line up under the
		// first; here the reader parses tags, and four problems are four things
		// to fix rather than one string to pick apart.
		payload["message"] = "invalid arguments"

		listed := make([]any, 0, len(problems))
		for _, problem := range problems {
			listed = append(listed, problem)
		}

		payload["problems"] = listed
	}

	var refused *transport.APIError
	if errors.As(err, &refused) {
		payload["status"] = refused.Status

		if refused.Code != "" {
			payload["code"] = refused.Code
		}

		if len(refused.Meta) > 0 {
			payload["meta"] = refused.Meta
		}
	}

	// Said in a form a model can act on. It is the difference between trying
	// again and giving up, and it is not reliably readable out of the message.
	if kind := classify(err); kind != "" {
		payload["kind"] = kind
	}

	var buffer bytes.Buffer

	printer := &output.Printer{
		Format: output.FormatXML,
		Out:    &buffer,
		Root:   root,
	}
	if printErr := printer.Print(payload); printErr != nil {
		return err.Error()
	}

	return buffer.String()
}

// classify names the kind in one word, and says whether trying again could
// possibly help.
func classify(err error) string {
	switch {
	case errors.Is(err, ErrTokenExpired):
		return "token_expired"
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, ErrPermissionDenied):
		return "permission_denied"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrInvalidArgument):
		return "invalid_argument"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, ErrNoToken):
		return "no_token"
	case errors.Is(err, ErrNoSuchService), errors.Is(err, ErrNoSuchOperation):
		return "no_such_operation"
	}

	return ""
}

// CanRetry reports whether the same call, made again, could succeed.
//
// An expired token can be replaced and a service that is down can come back; a
// refusal about permissions or arguments will be the same refusal every time.
// Saying so here means each caller does not have to work it out from the kinds,
// and does not have to be updated when a new one is added.
func CanRetry(err error) bool {
	return errors.Is(err, ErrTokenExpired) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrUnavailable)
}
