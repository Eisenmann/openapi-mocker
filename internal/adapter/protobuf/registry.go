package protobuf

import (
	"fmt"
	"strings"
)

// wellKnown holds the sources of the supported google/protobuf types.
var wellKnown = map[string]string{
	"google/protobuf/timestamp.proto": `syntax = "proto3"; package google.protobuf;
message Timestamp { int64 seconds = 1; int32 nanos = 2; }`,
	"google/protobuf/duration.proto": `syntax = "proto3"; package google.protobuf;
message Duration { int64 seconds = 1; int32 nanos = 2; }`,
	"google/protobuf/empty.proto": `syntax = "proto3"; package google.protobuf;
message Empty {}`,
	"google/protobuf/field_mask.proto": `syntax = "proto3"; package google.protobuf;
message FieldMask { repeated string paths = 1; }`,
	"google/protobuf/wrappers.proto": `syntax = "proto3"; package google.protobuf;
message DoubleValue { double value = 1; }
message FloatValue { float value = 1; }
message Int64Value { int64 value = 1; }
message UInt64Value { uint64 value = 1; }
message Int32Value { int32 value = 1; }
message UInt32Value { uint32 value = 1; }
message BoolValue { bool value = 1; }
message StringValue { string value = 1; }
message BytesValue { bytes value = 1; }`,
	"google/protobuf/struct.proto": `syntax = "proto3"; package google.protobuf;
message Struct { map<string, Value> fields = 1; }
message Value {
  oneof kind {
    NullValue null_value = 1;
    double number_value = 2;
    string string_value = 3;
    bool bool_value = 4;
    Struct struct_value = 5;
    ListValue list_value = 6;
  }
}
enum NullValue { NULL_VALUE = 0; }
message ListValue { repeated Value values = 1; }`,
}

// Names of well-known types and other repeated literals.
const (
	wktTimestamp = "Timestamp"
	wktDuration  = "Duration"
	wktFieldMask = "FieldMask"
	wktStruct    = "Struct"
	wktValue     = "Value"
	wktListValue = "ListValue"
	wktEmpty     = "Empty"
	jsonTrue     = "true"
	syntaxProto2 = "proto2"
	syntaxProto3 = "proto3"
	mapKeyName   = "key"
	mapValueName = "value"
	kwOption     = "option"
)

// wrapperTypes are the google.protobuf wrapper messages.
var wrapperTypes = map[string]bool{ //nolint:gochecknoglobals // read-only lookup table.
	"DoubleValue": true, "FloatValue": true, "Int64Value": true, "UInt64Value": true, "Int32Value": true,
	"UInt32Value": true, "BoolValue": true, "StringValue": true, "BytesValue": true,
}

func isWellKnownFile(name string) bool {
	_, ok := wellKnown[name]

	return ok
}

// wellKnownOrder keeps registry construction deterministic.
var wellKnownOrder = []string{
	"google/protobuf/timestamp.proto", "google/protobuf/duration.proto", "google/protobuf/empty.proto",
	"google/protobuf/field_mask.proto", "google/protobuf/wrappers.proto", "google/protobuf/struct.proto",
}

const (
	maxFieldNumber = 536870911
	reservedFrom   = 19000
	reservedTo     = 19999
)

// Parse parses a .proto source into a registry that also knows the
// well-known types. The source is one file, or several files in one text,
// each introduced by a marker line (see splitFiles). Errors are descriptive
// and prefixed with the line (and the file name when there are several).
func Parse(src string) (*Registry, error) {
	srcs, err := splitFiles(src)
	if err != nil {
		return nil, err
	}

	users, err := parseUserFiles(srcs)
	if err != nil {
		return nil, err
	}

	r, err := newRegistry(users)
	if err != nil {
		return nil, err
	}

	err = r.resolve()
	if err != nil {
		return nil, err
	}

	return r, nil
}

// parseUserFiles parses the files of a contract and checks their imports.
func parseUserFiles(srcs []srcFile) ([]*File, error) {
	multi := len(srcs) > 1 || srcs[0].name != defaultProtoFile
	users := make([]*File, 0, len(srcs))

	for _, fs := range srcs {
		f, err := parseFile(fs.name, fs.text)
		if err != nil {
			if multi {
				err = fmt.Errorf("%s: %w", fs.name, err)
			}

			return nil, err
		}

		users = append(users, f)
	}

	err := checkImports(users)
	if err != nil {
		return nil, err
	}

	return users, nil
}

// newRegistry registers the well-known types and then the files of the contract.
func newRegistry(users []*File) (*Registry, error) {
	r := &Registry{ //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		messages: map[string]*Message{}, enums: map[string]*Enum{},
		services: map[string]*Service{}, byFile: map[string]*File{},
	}

	for _, name := range wellKnownOrder {
		f, err := parseFile(name, wellKnown[name])
		if err != nil {
			return nil, fmt.Errorf("internal: well-known type %s: %w", name, err)
		}

		err = r.add(f)
		if err != nil {
			return nil, err
		}
	}

	for _, f := range users {
		err := r.add(f)
		if err != nil {
			return nil, err
		}
	}

	return r, nil
}

// add registers a file and every type it declares; a name that is declared
// twice (also across files) is an error.
func (r *Registry) add(f *File) error {
	r.Files = append(r.Files, f)
	r.byFile[f.Name] = f

	var dup string

	declare := func(full string) {
		_, isMsg := r.messages[full]
		_, isEnum := r.enums[full]
		_, isSvc := r.services[full]

		if (isMsg || isEnum || isSvc) && dup == "" {
			dup = full
		}
	}

	var walk func(ms []*Message)

	walk = func(ms []*Message) {
		for _, m := range ms {
			m.file = f

			declare(m.FullName)
			r.messages[m.FullName] = m

			for _, e := range m.Enums {
				declare(e.FullName)
				r.enums[e.FullName] = e
			}

			walk(m.Nested)
		}
	}

	walk(f.Messages)

	for _, e := range f.Enums {
		declare(e.FullName)
		r.enums[e.FullName] = e
	}

	for _, s := range f.Services {
		declare(s.FullName)
		r.services[s.FullName] = s
	}

	if dup != "" {
		return fmt.Errorf("%w: %s is defined more than once (in %s)", ErrSyntax, dup, f.Name)
	}

	return nil
}

// lookup resolves a type reference seen from the scope (a full message name).
func (r *Registry) lookup(scope, name string) (*Message, *Enum) {
	if strings.HasPrefix(name, ".") {
		name = name[1:]

		return r.messages[name], r.enums[name]
	}

	for {
		cand := join(scope, name)
		if m, e := r.messages[cand], r.enums[cand]; m != nil || e != nil {
			return m, e
		}

		if scope == "" {
			return nil, nil
		}

		if i := strings.LastIndex(scope, "."); i >= 0 {
			scope = scope[:i]
		} else {
			scope = ""
		}
	}
}

func (r *Registry) resolve() error {
	for _, f := range r.Files {
		err := r.resolveFile(f)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *Registry) resolveFile(f *File) error {
	var walk func(ms []*Message) error

	walk = func(ms []*Message) error {
		for _, m := range ms {
			err := r.resolveMessage(f, m)
			if err != nil {
				return err
			}

			err = walk(m.Nested)
			if err != nil {
				return err
			}
		}

		return nil
	}

	err := walk(f.Messages)
	if err != nil {
		return err
	}

	if f.Syntax == syntaxProto3 {
		err = r.checkProto3Enums(f)
		if err != nil {
			return err
		}
	}

	for _, s := range f.Services {
		err = r.resolveService(f, s)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *Registry) checkProto3Enums(f *File) error {
	var enums []*Enum

	enums = append(enums, f.Enums...)

	var walk func(ms []*Message)

	walk = func(ms []*Message) {
		for _, m := range ms {
			enums = append(enums, m.Enums...)
			walk(m.Nested)
		}
	}

	walk(f.Messages)

	for _, e := range enums {
		if e.Values[0].Number != 0 {
			return fmt.Errorf("%w: enum %s: the first value of a proto3 enum must be zero", ErrSyntax, e.FullName)
		}
	}

	return nil
}

func (r *Registry) resolveService(f *File, s *Service) error {
	seen := map[string]bool{}

	for _, m := range s.Methods {
		if seen[m.Name] {
			return fmt.Errorf("%w: service %s: duplicate method %s", ErrSyntax, s.FullName, m.Name)
		}

		seen[m.Name] = true

		in, _ := r.lookup(f.Package, m.InputType)
		out, _ := r.lookup(f.Package, m.OutputType)

		switch {
		case in == nil:
			return fmt.Errorf("%w: %s.%s: unknown request type %s", ErrSyntax, s.FullName, m.Name, m.InputType)
		case out == nil:
			return fmt.Errorf("%w: %s.%s: unknown response type %s", ErrSyntax, s.FullName, m.Name, m.OutputType)
		}

		m.Input, m.Output = in, out
	}

	return nil
}

func (r *Registry) resolveMessage(f *File, m *Message) error {
	m.byNum, m.byName = map[int]*Field{}, map[string]*Field{}

	for _, fld := range m.Fields {
		err := r.resolveField(m, fld)
		if err != nil {
			return err
		}

		switch {
		case fld.Number < 1 || fld.Number > maxFieldNumber || (fld.Number >= reservedFrom && fld.Number <= reservedTo):
			return fmt.Errorf("%w: %s.%s: field number %d is out of range", ErrSyntax, m.FullName, fld.Name, fld.Number)
		case m.byNum[fld.Number] != nil:
			return fmt.Errorf("%w: %s: field number %d is used twice", ErrSyntax, m.FullName, fld.Number)
		case m.byName[fld.Name] != nil:
			return fmt.Errorf("%w: %s: field %s is declared twice", ErrSyntax, m.FullName, fld.Name)
		}

		m.byNum[fld.Number] = fld
		m.byName[fld.Name] = fld

		if fld.JSONName != fld.Name {
			m.byName[fld.JSONName] = fld
		}
	}

	_ = f

	return nil
}

func (r *Registry) resolveField(m *Message, f *Field) error {
	if f.IsMap {
		return r.resolveMapValue(m, f)
	}

	if f.Kind != KindMessage {
		return nil
	}

	msg, enum := r.lookup(m.FullName, f.TypeName)

	switch {
	case msg != nil:
		f.Message = msg
	case enum != nil:
		f.Kind, f.Enum = KindEnum, enum
	default:
		return fmt.Errorf("%w: %s.%s: unknown type %s", ErrSyntax, m.FullName, f.Name, f.TypeName)
	}

	return nil
}

func (r *Registry) resolveMapValue(m *Message, f *Field) error {
	v := f.MapValue
	if v.Kind != KindMessage {
		return nil
	}

	msg, enum := r.lookup(m.FullName, v.TypeName)

	switch {
	case msg != nil:
		v.Message = msg
	case enum != nil:
		v.Kind, v.Enum = KindEnum, enum
	default:
		return fmt.Errorf("%w: %s.%s: unknown map value type %s", ErrSyntax, m.FullName, f.Name, v.TypeName)
	}

	return nil
}
