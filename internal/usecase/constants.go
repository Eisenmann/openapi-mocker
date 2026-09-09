package usecase

// Contract format constants.
const (
	FormatGraphQL = "graphql"
	FormatYAML    = "yaml"
	FormatJSON    = "json"
)

// Mock scenario constants.
const (
	ScenarioDefault = "default"
)

// Content type constants.
const (
	ContentTypeJSON = "application/json"
)

// HTTP status code constants.
const (
	StatusOK                  = 200
	StatusBadRequest          = 400
	StatusNotFound            = 404
	StatusInternalServerError = 500
)

// LLM generation constants.
const (
	DefaultTemperature  = 0.7
	DefaultMaxTokens    = 2000
	ContractTemperature = 0.4
	ContractMaxTokens   = 4000
	TestMaxTokens       = 10
)

// Chaos testing constants.
const (
	PercentBase      = 100
	ServerErrorRange = 4
)
