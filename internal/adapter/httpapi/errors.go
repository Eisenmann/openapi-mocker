package httpapi

import "errors"

// Sentinel errors returned to HTTP clients through writeError. Keeping them
// as package-level values lets err113 stay clean and gives tests/callers a
// stable identity (errors.Is) instead of matching on message text.
var (
	errInvalidVersionNumber      = errors.New("invalid version number")
	errFileNotProvided           = errors.New("file not provided (field 'file')")
	errFromParamRequired         = errors.New("from parameter is required and must be a number")
	errToParamMustBeNumber       = errors.New("to parameter must be a number")
	errContractNotLoaded         = errors.New("contract has not been loaded yet")
	errGraphQLNotEnabled         = errors.New("GraphQL not enabled")
	errQueryRequired             = errors.New("query is required")
	errCodeGenAgentNotConfigured = errors.New("code generation agent is not configured")
	errInvalidJSON               = errors.New("invalid JSON body")
)
