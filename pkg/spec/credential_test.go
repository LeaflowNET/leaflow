package spec

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestReadCredential(t *testing.T) {
	doc := &openapi3.T{
		Security: openapi3.SecurityRequirements{{"projectAuth": {}}},
		Components: &openapi3.Components{SecuritySchemes: openapi3.SecuritySchemes{
			"projectAuth": {Ref: "../../type/v1/security.yaml#/components/securitySchemes/ScopedToken"},
			"accountAuth": {Ref: "../../type/v1/security.yaml#/components/securitySchemes/AccessToken"},
		}},
	}
	public := openapi3.SecurityRequirements{}
	account := openapi3.SecurityRequirements{{"accountAuth": {}}}
	optional := openapi3.SecurityRequirements{{"accountAuth": {}}, {}}
	for _, test := range []struct {
		name     string
		security *openapi3.SecurityRequirements
		want     Credential
	}{
		{"inherited project", nil, AccessToken},
		{"explicit public", &public, NoCredential},
		{"account override", &account, AccountToken},
		{"optional authentication", &optional, NoCredential},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ReadCredential(doc, &openapi3.Operation{Security: test.security}); got != test.want {
				t.Fatalf("credential = %v, want %v", got, test.want)
			}
		})
	}
	if got := ReadCredential(&openapi3.T{}, &openapi3.Operation{}); got != NoCredential {
		t.Fatalf("no security declared: %v", got)
	}
}

func TestEmbeddedOperationCredentials(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		service, operation string
		want               Credential
	}{
		{"billing", "create-estimate", NoCredential},
		{"billing", "list-billing-accounts", AccountToken},
		{"billing", "get-project-billing-account", AccessToken},
		{"account", "list-locales", NoCredential},
		{"account", "list-projects", AccountToken},
		{"iam", "get-role", AccessToken},
	} {
		t.Run(test.service+"/"+test.operation, func(t *testing.T) {
			service, ok := set.Service(test.service)
			if !ok {
				t.Fatal("missing service")
			}
			op, ok := service.Operation(test.operation)
			if !ok {
				t.Fatal("missing operation")
			}
			if op.Credential != test.want {
				t.Fatalf("credential = %v, want %v", op.Credential, test.want)
			}
		})
	}
}
