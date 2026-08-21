package transport

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrNoServiceAddress = errors.New("no address for service")

// Endpoints resolves a service's address for one deployment.
//
// Zero value means "use the contracts as written", which is what talking to the
// hosted platform needs and therefore what a caller who says nothing gets.
//
// Nothing is derived from the service name. That convention holds for every
// service but one, and the exception answers 404 — which reads as "no such
// endpoint" rather than "right address, wrong face".
type Endpoints struct {
	// Domain rewrites the domain part of every address a contract declares, so
	// that compute.leaflow.cloud becomes compute.leaflow.test for a self-hosted
	// deployment. It is a rewrite of a stated address, not a way of deriving one.
	Domain string

	// Overrides replaces one service's address outright, for a local stack whose
	// addresses do not follow from a domain — different ports, hostnames that are
	// nobody's subdomain.
	Overrides map[string]string
}

// ServiceURL is where a service answers.
//
// Resolution order: an explicit override, then the contract's own address with
// Domain applied if one is set, then the contract's address as written. A
// contract that states no address produces an error naming the contract,
// because that is where the fix belongs.
func (e Endpoints) ServiceURL(service, declared string) (string, error) {
	if endpoint, ok := e.Overrides[service]; ok && endpoint != "" {
		return strings.TrimRight(endpoint, "/"), nil
	}

	if declared == "" {
		return "", fmt.Errorf("%w: %s declares no address", ErrNoServiceAddress, service)
	}

	if e.Domain == "" {
		return declared, nil
	}

	return RewriteDomain(declared, e.Domain)
}

// RewriteDomain swaps everything after the first label of the host, so
// compute.leaflow.cloud becomes compute.leaflow.test while keeping the scheme
// and the port a local stack needs.
func RewriteDomain(declared, domain string) (string, error) {
	parsed, err := url.Parse(declared)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrNoServiceAddress, declared, err)
	}

	// Read before writing: assigning Host discards the port, and reading it
	// afterwards returns nothing.
	port := parsed.Port()
	host := parsed.Hostname()

	label, _, found := strings.Cut(host, ".")
	if !found {
		label = host
	}

	parsed.Host = label + "." + domain

	if port != "" {
		parsed.Host += ":" + port
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}
