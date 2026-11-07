package usecase

// SchemeKind is how a security scheme carries its credential.
type SchemeKind string

// The kinds of credential the auth simulation understands.
const (
	// SchemeBearer is "Authorization: Bearer <token>" (http bearer, oauth2
	// and openIdConnect schemes).
	SchemeBearer SchemeKind = "bearer"
	// SchemeBasic is "Authorization: Basic <base64(user:password)>".
	SchemeBasic SchemeKind = "basic"
	// SchemeAPIKey is an API key in a header, query parameter or cookie.
	SchemeAPIKey SchemeKind = "apiKey"
	// SchemeAny accepts a bearer token or an API key in the default places.
	SchemeAny SchemeKind = "any"
)

// Places an API key can be sent.
const (
	InHeader = "header"
	InQuery  = "query"
	InCookie = "cookie"
)

// SchemeUse is one security scheme an operation requires, with its scopes.
type SchemeUse struct {
	Name   string
	Kind   SchemeKind
	In     string // apiKey only: header, query or cookie.
	Param  string // apiKey only: the header, parameter or cookie name.
	Scopes []string
}

// SecurityAlternative is a set of schemes that must all be satisfied.
type SecurityAlternative []SchemeUse

// OperationSecurity lists the alternatives of an operation: a request is
// allowed when it satisfies any one of them. No alternatives means the
// operation is public.
type OperationSecurity struct {
	Alternatives []SecurityAlternative
}

// SecurityReader reads the security requirements of a contract operation.
type SecurityReader interface {
	// OperationSecurity returns the requirements of the operation (ok is
	// false when the operation does not exist).
	OperationSecurity(raw []byte, method, path string) (sec OperationSecurity, ok bool)
}
