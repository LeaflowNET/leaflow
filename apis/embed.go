// Package apis holds the OpenAPI contracts this CLI is built from, mirroring
// the layout of the contracts repository:
//
//	leaflow/<service>/v1/openapi.yaml
//	leaflow/type/v1/error.yaml        shared, referenced by every contract
//
// Keeping the layout is what lets each contract's relative reference to the
// shared error schema resolve, and means a synced file is byte-identical to
// upstream — so a review diff is the upstream diff, with nothing in between.
//
// It is a package of its own, outside internal/, because the contracts are an
// asset of the repository rather than the private data of one package, and
// because go:embed only reaches the directory it is declared in.
package apis

import "embed"

//go:embed all:leaflow
var Contracts embed.FS

// Root is the directory inside Contracts that holds the services.
const Root = "leaflow"
