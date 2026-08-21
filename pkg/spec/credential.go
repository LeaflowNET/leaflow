package spec

// Credential says which of the platform's two user-facing tokens an operation
// takes.
//
//	account token   signed by Keycloak   register, list projects, exchange
//	access token    signed by IAM        everything inside a project
//
// An access token names a project as well as a person, which is why no request
// path carries a project id. The contracts still call it a project token, and
// the product calls it 访问密钥; all three are the same thing.
//
// The exchange itself can only take the account token: it is what mints project
// tokens, so requiring one would deadlock.
//
// This has nothing to do with the operator console, which this CLI never
// touches.
type Credential int

const (
	AccessToken Credential = iota

	AccountToken
)

func (c Credential) String() string {
	if c == AccountToken {
		return "account"
	}

	return "project"
}

// accountService is the one service that speaks account tokens.
//
// This used to be a hand-written list of thirteen operationIds, because both
// faces lived in one contract and the difference could not be read off it.
// They are now separate services with separate contracts and separate
// addresses, so the service name answers the question and the list is gone —
// along with everything that could rot in it.
const accountService = "account"

// ReadCredential is exported because refreshing a contract is a request too, and
// the account face does not accept a project token.
func ReadCredential(service string) Credential {
	if service == accountService {
		return AccountToken
	}

	return AccessToken
}
