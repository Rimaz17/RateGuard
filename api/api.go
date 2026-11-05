// Package api embeds RateGuard's HTTP contract, so a running binary serves
// exactly the OpenAPI document it was built with.
package api

import _ "embed" // for the contract

//go:embed openapi.yaml
var openAPI string

// OpenAPI returns the OpenAPI 3.1 document, api/openapi.yaml.
func OpenAPI() string { return openAPI }
