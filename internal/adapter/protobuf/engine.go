package protobuf

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// maxCachedContracts bounds the parse cache.
const maxCachedContracts = 64

var _ usecase.ProtoEngine = (*Engine)(nil)

// Engine implements usecase.ProtoEngine on top of the parser and codec of
// this package. Parsed contracts are cached by their source text.
type Engine struct {
	mu    sync.Mutex
	cache map[string]*Registry
	order []string
}

// NewEngine creates an Engine.
func NewEngine() *Engine {
	return &Engine{mu: sync.Mutex{}, cache: map[string]*Registry{}, order: nil}
}

var errNoServices = errors.New("the .proto file declares no service with an rpc")

// Validate implements usecase.ProtoEngine.
func (e *Engine) Validate(raw []byte) usecase.ProtoValidationResult {
	r, err := e.registry(raw)
	if err != nil {
		//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		return usecase.ProtoValidationResult{Errors: []string{err.Error()}}
	}

	res := usecase.ProtoValidationResult{Valid: true} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero

	for _, s := range r.Services() {
		res.Services++
		res.Methods += len(s.Methods)
	}

	if res.Methods == 0 {
		res.Valid, res.Errors = false, []string{errNoServices.Error()}
	}

	return res
}

// ParseAndValidate implements usecase.ProtoEngine.
func (e *Engine) ParseAndValidate(raw []byte) error {
	res := e.Validate(raw)
	if !res.Valid {
		return fmt.Errorf("%w: %s", usecase.ErrContractInvalid, strings.Join(res.Errors, "; "))
	}

	return nil
}

// Services implements usecase.ProtoEngine.
func (e *Engine) Services(raw []byte) ([]usecase.GRPCService, error) {
	r, err := e.registry(raw)
	if err != nil {
		return nil, err
	}

	services := r.Services()
	out := make([]usecase.GRPCService, 0, len(services))

	for _, s := range services {
		svc := usecase.GRPCService{Name: s.FullName, Methods: make([]usecase.GRPCMethod, 0, len(s.Methods))}

		for _, m := range s.Methods {
			svc.Methods = append(svc.Methods, usecase.GRPCMethod{
				Name: m.Name, Path: m.Path(s), InputType: m.Input.FullName, OutputType: m.Output.FullName,
				ClientStreaming: m.ClientStreaming, ServerStreaming: m.ServerStreaming,
				RequestExample: "", ResponseExample: "",
			})
		}

		out = append(out, svc)
	}

	return out, nil
}

// JSONToWire implements usecase.ProtoEngine.
func (e *Engine) JSONToWire(raw []byte, message string, doc []byte) ([]byte, error) {
	m, err := e.message(raw, message)
	if err != nil {
		return nil, err
	}

	return Marshal(m, doc)
}

// WireToJSON implements usecase.ProtoEngine.
func (e *Engine) WireToJSON(raw []byte, message string, wire []byte) ([]byte, error) {
	m, err := e.message(raw, message)
	if err != nil {
		return nil, err
	}

	return Unmarshal(m, wire)
}

// Example implements usecase.ProtoEngine.
func (e *Engine) Example(raw []byte, message string) ([]byte, error) {
	m, err := e.message(raw, message)
	if err != nil {
		return nil, err
	}

	return Example(m), nil
}

// FileDescriptor implements usecase.ProtoEngine.
func (e *Engine) FileDescriptor(raw []byte, name string) ([]byte, error) {
	r, err := e.registry(raw)
	if err != nil {
		return nil, err
	}

	return r.FileDescriptor(name)
}

// FileContaining implements usecase.ProtoEngine.
func (e *Engine) FileContaining(raw []byte, symbol string) (name string, deps []string, err error) {
	r, err := e.registry(raw)
	if err != nil {
		return "", nil, err
	}

	name, ok := r.FileContaining(symbol)
	if !ok {
		return "", nil, fmt.Errorf("%w: symbol %q", ErrUnknownSymbol, symbol)
	}

	return name, r.byFile[name].Imports, nil
}

var _ usecase.ProtoFileImporter = (*Engine)(nil)

// FileImports implements usecase.ProtoFileImporter.
func (e *Engine) FileImports(raw []byte, name string) ([]string, error) {
	r, err := e.registry(raw)
	if err != nil {
		return nil, err
	}

	f := r.byFile[name]
	if f == nil {
		return nil, fmt.Errorf("%w: file %q", ErrUnknownSymbol, name)
	}

	return f.Imports, nil
}

func (e *Engine) registry(raw []byte) (*Registry, error) {
	key := string(raw)

	e.mu.Lock()
	r, ok := e.cache[key]
	e.mu.Unlock()

	if ok {
		return r, nil
	}

	r, err := Parse(key)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.order) >= maxCachedContracts {
		delete(e.cache, e.order[0])
		e.order = e.order[1:]
	}

	e.cache[key] = r
	e.order = append(e.order, key)

	return r, nil
}

func (e *Engine) message(raw []byte, name string) (*Message, error) {
	r, err := e.registry(raw)
	if err != nil {
		return nil, err
	}

	m := r.Message(name)
	if m == nil {
		return nil, fmt.Errorf("%w: message type %q", ErrUnknownSymbol, name)
	}

	return m, nil
}
