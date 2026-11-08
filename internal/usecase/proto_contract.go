package usecase

import (
	"regexp"
	"strings"
)

// FormatProto is the contract format of a Protocol Buffers (.proto) file that
// declares gRPC services.
const FormatProto = "proto"

// ProtoValidationResult is the outcome of validating a .proto contract.
type ProtoValidationResult struct {
	Valid    bool
	Errors   []string
	Services int
	Methods  int
}

// GRPCMethod describes one rpc of a service.
type GRPCMethod struct {
	Name            string `json:"name"`
	Path            string `json:"path"` // "/pkg.Service/Method".
	InputType       string `json:"inputType"`
	OutputType      string `json:"outputType"`
	ClientStreaming bool   `json:"clientStreaming"`
	ServerStreaming bool   `json:"serverStreaming"`
	// RequestExample and ResponseExample are sample JSON documents.
	RequestExample  string `json:"requestExample,omitempty"`
	ResponseExample string `json:"responseExample,omitempty"`
}

// Kind names the call type: unary, server-stream, client-stream or bidi.
func (m *GRPCMethod) Kind() string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "bidi"
	case m.ServerStreaming:
		return "server-stream"
	case m.ClientStreaming:
		return "client-stream"
	default:
		return "unary"
	}
}

// GRPCService describes a service of a .proto contract.
type GRPCService struct {
	Name    string       `json:"name"` // fully qualified, e.g. "shop.v1.Shop".
	Methods []GRPCMethod `json:"methods"`
}

// ProtoEngine is the port the usecase layer depends on for .proto work. Types
// are addressed by fully qualified message name.
type ProtoEngine interface {
	Validate(raw []byte) ProtoValidationResult
	ParseAndValidate(raw []byte) error
	Services(raw []byte) ([]GRPCService, error)
	// JSONToWire encodes a proto3-JSON document as a protobuf message.
	JSONToWire(raw []byte, message string, doc []byte) ([]byte, error)
	// WireToJSON decodes a protobuf message to a proto3-JSON document.
	WireToJSON(raw []byte, message string, wire []byte) ([]byte, error)
	// Example returns a sample JSON document of a message.
	Example(raw []byte, message string) ([]byte, error)
	// FileDescriptor returns a serialized FileDescriptorProto (for reflection).
	FileDescriptor(raw []byte, name string) ([]byte, error)
	// FileContaining names the file that declares a symbol, and the files it imports.
	FileContaining(raw []byte, symbol string) (name string, deps []string, err error)
}

// ProtoFileImporter is an optional capability of a ProtoEngine: it lists the
// files that one file of a (possibly multi-file) contract imports.
type ProtoFileImporter interface {
	FileImports(raw []byte, name string) ([]string, error)
}

var (
	protoSyntax  = regexp.MustCompile(`(?m)^\s*syntax\s*=\s*["']proto[23]["']\s*;`)
	protoService = regexp.MustCompile(`(?m)^\s*rpc\s+\w+\s*\(.*\)\s*returns\s*\(`)
)

// IsProto reports whether raw looks like a .proto file: it declares its
// syntax, or has an rpc definition. OpenAPI documents, GraphQL SDL and MCP
// manifests never do.
func IsProto(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "{") {
		return false
	}

	return protoSyntax.MatchString(raw) || protoService.MatchString(raw)
}
