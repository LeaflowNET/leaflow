package transport

import (
	"errors"
	"net/http"
)

// The kinds a refusal falls into.
//
// A caller has to be able to tell "get another token" from "you may not do
// that" from "try again later", because the three call for opposite responses
// and only one of them is worth retrying. Without these it has to match on
// APIError.Code, which means every caller carries its own copy of the mapping
// below — and the copies drift the day the platform adds a code.
//
// They are reached with errors.Is. An APIError unwraps to the kind it belongs
// to, and to more than one where the finer answer is also useful:
//
//	errors.Is(err, transport.ErrTokenExpired)     // mint a new one and retry
//	errors.Is(err, transport.ErrUnauthenticated)  // ... or just: not signed in
var (
	// ErrUnauthenticated means no usable credentials. The identity was not
	// established, so the request was not considered.
	ErrUnauthenticated = errors.New("not authenticated")

	// ErrTokenExpired is the case worth retrying: the credential was valid and
	// is not any more, and a fresh one fixes it. It also unwraps to
	// ErrUnauthenticated.
	ErrTokenExpired = errors.New("token expired")

	// ErrPermissionDenied means the identity is established and not allowed.
	// Retrying with a fresh token cannot help.
	ErrPermissionDenied = errors.New("not permitted")

	ErrNotFound = errors.New("not found")

	// ErrInvalidArgument means the request was understood and refused as
	// malformed. Sending it again unchanged will be refused again.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrConflict means the request contradicts the current state — creating
	// something that exists, deleting something in use.
	ErrConflict = errors.New("conflict")

	ErrRateLimited = errors.New("rate limited")

	// ErrUnavailable is the service failing rather than refusing, which is the
	// other case where trying again later is reasonable.
	ErrUnavailable = errors.New("service unavailable")
)

// Unwrap places this refusal among the kinds above.
//
// A slice rather than a single error, so that a caller can ask the precise
// question or the general one and get a true answer to both: an expired token
// is an expired token and is also a failure to authenticate.
//
// The service's code is preferred over the status because it is the part that
// is contract — the status is chosen by whichever component answered, and the
// gateway and IAM do not always agree on it.
func (e *APIError) Unwrap() []error {
	switch e.Code {
	case "TOKEN_EXPIRED":
		return []error{ErrTokenExpired, ErrUnauthenticated}
	case "TOKEN_MISSING", "TOKEN_INVALID", "IDENTITY_MISSING", "IDENTITY_INVALID":
		return []error{ErrUnauthenticated}
	case "NOT_A_MEMBER", "USER_SUSPENDED", "USER_BANNED":
		return []error{ErrPermissionDenied}
	}

	switch e.Status {
	case http.StatusUnauthorized:
		// No code to go on. Treated as expiry because that is the common cause
		// and the only one a retry can fix; a retry that was not going to help
		// costs one round trip, while missing one costs the whole call.
		return []error{ErrTokenExpired, ErrUnauthenticated}
	case http.StatusForbidden:
		return []error{ErrPermissionDenied}
	case http.StatusNotFound:
		return []error{ErrNotFound}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return []error{ErrInvalidArgument}
	case http.StatusConflict:
		return []error{ErrConflict}
	case http.StatusTooManyRequests:
		return []error{ErrRateLimited}
	}

	if e.Status >= 500 {
		return []error{ErrUnavailable}
	}

	return nil
}
