// Package contract checks the live AudD services against the shapes the CLI
// relies on. It runs on a schedule (.github/workflows/contract.yml), never
// in the normal test suite:
//
//	go test -tags contract ./internal/contract/ -v
//
// Each check is skipped when its credentials are not set:
//
//	AUDD_CONTRACT_API_TOKEN      an API token for a test account (CI only)
//	AUDD_CONTRACT_RADIO_ID       a stream on that account, for recent results
//	AUDD_CONTRACT_OAUTH_SESSION  the JSON session `audd login` stores (secret
//	                             key "oauth"), for the account tool schemas
//	AUDD_CONTRACT_SESSION_OUT    where to write the refreshed session, so a
//	                             single-use refresh token can be saved back
//	AUDD_CONTRACT_SCHEMAS_OUT    where to write every live tool's output
//	                             schema as JSON, keyed by tool name
package contract
