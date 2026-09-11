package spec

import (
	"path"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

// Credential identifies the authentication required by an operation.
type Credential int

const (
	// AccessToken is the CLI's project access key (ScopedToken in OpenAPI).
	AccessToken Credential = iota
	// AccountToken is the identity provider's AccessToken in OpenAPI.
	AccountToken
	NoCredential
)

func (c Credential) String() string {
	switch c {
	case AccountToken:
		return "account"
	case NoCredential:
		return "none"
	default:
		return "project"
	}
}

// ReadCredential applies the operation's security override, or the document's
// default when no override is declared. An empty requirement permits anonymous
// requests. Scheme references identify the token independently of service names.
func ReadCredential(doc *openapi3.T, operation *openapi3.Operation) Credential {
	security := doc.Security
	if operation.Security != nil {
		security = *operation.Security
	}
	if len(security) == 0 {
		return NoCredential
	}
	for _, requirement := range security {
		if len(requirement) == 0 {
			return NoCredential
		}
	}

	// Leaflow contracts declare one bearer scheme per requirement. Keep the
	// choice deterministic if a future contract contains multiple scheme names.
	names := make([]string, 0, len(security[0]))
	for name := range security[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	name := names[0]
	if doc.Components != nil {
		if scheme := doc.Components.SecuritySchemes[name]; scheme != nil && scheme.Ref != "" {
			name = path.Base(scheme.Ref)
		}
	}
	if name == "AccessToken" {
		return AccountToken
	}
	return AccessToken
}
