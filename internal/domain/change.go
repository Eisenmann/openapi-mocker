package domain

// ChangeSeverity says how a contract change affects existing clients.
type ChangeSeverity string

const (
	// SeverityBreaking is a change that makes a client written against the old
	// contract fail: a removed field, a changed type, a new required input.
	SeverityBreaking ChangeSeverity = "breaking"
	// SeverityWarning is a change that is compatible on paper but can still
	// surprise a client: an input the server no longer reads, a new enum value.
	SeverityWarning ChangeSeverity = "warning"
)

// Kinds of ContractChange.
const (
	ChangeOperationRemoved   = "operation-removed"
	ChangeResponseRemoved    = "response-removed"
	ChangeMediaTypeRemoved   = "media-type-removed"
	ChangeParamRemoved       = "param-removed"
	ChangeParamAddedRequired = "param-added-required"
	ChangeParamRequired      = "param-became-required"
	ChangeBodyRequired       = "request-body-required"
	ChangeBodyRemoved        = "request-body-removed"
	ChangeFieldRemoved       = "field-removed"
	ChangeFieldAddedRequired = "field-added-required"
	ChangeFieldRequired      = "field-became-required"
	ChangeFieldOptional      = "field-became-optional"
	ChangeTypeChanged        = "type-changed"
	ChangeEnumRemoved        = "enum-value-removed"
	ChangeEnumAdded          = "enum-value-added"
)

// ContractChange is one difference between two versions of an OpenAPI
// contract that matters to clients.
type ContractChange struct {
	Kind     string         `json:"kind"`
	Severity ChangeSeverity `json:"severity"`
	// Operation is "GET /users/{id}".
	Operation string `json:"operation"`
	// Location says where in the operation: "query parameter limit",
	// "request body application/json: user.email", "response 200
	// application/json: items[].id". Empty for the operation as a whole.
	Location string `json:"location,omitempty"`
	Message  string `json:"message"`
}

// CountBreaking returns how many of the changes are breaking.
func CountBreaking(changes []ContractChange) int {
	n := 0

	for i := range changes {
		if changes[i].Severity == SeverityBreaking {
			n++
		}
	}

	return n
}
