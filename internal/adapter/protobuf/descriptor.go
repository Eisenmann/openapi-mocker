package protobuf

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Descriptor field numbers (google/protobuf/descriptor.proto).
const (
	fdpName       = 1
	fdpPackage    = 2
	fdpDependency = 3
	fdpMessage    = 4
	fdpEnum       = 5
	fdpService    = 6
	fdpSyntax     = 12

	dpName    = 1
	dpField   = 2
	dpNested  = 3
	dpEnum    = 4
	dpOptions = 7
	dpOneof   = 8

	fieldName     = 1
	fieldNumber   = 3
	fieldLabel    = 4
	fieldType     = 5
	fieldTypeName = 6
	fieldOneof    = 9
	fieldJSONName = 10
	fieldProto3   = 17

	labelOptional = 1
	labelRequired = 2
	labelRepeated = 3

	typeMessage = 11
	typeEnum    = 14

	// EnumDescriptorProto / EnumValueDescriptorProto / MessageOptions.
	edpValue    = 2
	evName      = 1
	evNumber    = 2
	optMapEntry = 7
)

// FieldDescriptorProto.Type values of the scalar kinds.
const (
	tDouble   = 1
	tFloat    = 2
	tInt64    = 3
	tUint64   = 4
	tInt32    = 5
	tFixed64  = 6
	tFixed32  = 7
	tBool     = 8
	tString   = 9
	tBytes    = 12
	tUint32   = 13
	tSfixed32 = 15
	tSfixed64 = 16
	tSint32   = 17
	tSint64   = 18
)

var descriptorTypes = map[Kind]uint64{ //nolint:gochecknoglobals // read-only lookup table.
	KindDouble: tDouble, KindFloat: tFloat, KindInt64: tInt64, KindUint64: tUint64, KindInt32: tInt32,
	KindFixed64: tFixed64, KindFixed32: tFixed32, KindBool: tBool, KindString: tString, KindBytes: tBytes,
	KindUint32: tUint32, KindSfixed32: tSfixed32, KindSfixed64: tSfixed64, KindSint32: tSint32, KindSint64: tSint64,
}

// ErrUnknownSymbol is returned when a file, message or symbol is not defined.
var ErrUnknownSymbol = errors.New("unknown name")

// FileDescriptor returns the serialized FileDescriptorProto of a file.
func (r *Registry) FileDescriptor(name string) ([]byte, error) {
	f := r.byFile[name]
	if f == nil {
		return nil, fmt.Errorf("%w: file %q", ErrUnknownSymbol, name)
	}

	var out []byte

	out = appendString(out, fdpName, f.Name)

	if f.Package != "" {
		out = appendString(out, fdpPackage, f.Package)
	}

	for _, imp := range f.Imports {
		out = appendString(out, fdpDependency, imp)
	}

	for _, m := range f.Messages {
		out = appendBytes(appendTag(out, fdpMessage, wireBytes), messageDescriptor(m))
	}

	for _, e := range f.Enums {
		out = appendBytes(appendTag(out, fdpEnum, wireBytes), enumDescriptor(e))
	}

	for _, s := range f.Services {
		out = appendBytes(appendTag(out, fdpService, wireBytes), serviceDescriptor(s))
	}

	if f.Syntax == syntaxProto3 {
		out = appendString(out, fdpSyntax, syntaxProto3)
	}

	return out, nil
}

// FileContaining returns the name of the file that declares a symbol
// (message, enum, service, or service method) by full name.
func (r *Registry) FileContaining(symbol string) (string, bool) {
	if m := r.messages[symbol]; m != nil {
		return m.file.Name, true
	}

	if s := r.services[symbol]; s != nil {
		return r.fileOfService(s), true
	}

	if e := r.enums[symbol]; e != nil {
		for _, f := range r.Files {
			if enumInFile(f, e) {
				return f.Name, true
			}
		}
	}

	if i := strings.LastIndex(symbol, "."); i > 0 {
		if s := r.services[symbol[:i]]; s != nil {
			for _, m := range s.Methods {
				if m.Name == symbol[i+1:] {
					return r.fileOfService(s), true
				}
			}
		}
	}

	return "", false
}

func (r *Registry) fileOfService(s *Service) string {
	for _, f := range r.Files {
		for _, fs := range f.Services {
			if fs == s {
				return f.Name
			}
		}
	}

	return ""
}

func enumInFile(f *File, e *Enum) bool {
	for _, fe := range f.Enums {
		if fe == e {
			return true
		}
	}

	var walk func(ms []*Message) bool

	walk = func(ms []*Message) bool {
		for _, m := range ms {
			for _, me := range m.Enums {
				if me == e {
					return true
				}
			}

			if walk(m.Nested) {
				return true
			}
		}

		return false
	}

	return walk(f.Messages)
}

func appendString(out []byte, num int, s string) []byte {
	return appendBytes(appendTag(out, num, wireBytes), []byte(s))
}

func appendUint(out []byte, num int, v uint64) []byte {
	return appendVarint(appendTag(out, num, wireVarint), v)
}

func enumDescriptor(e *Enum) []byte {
	out := appendString(nil, 1, e.Name)

	for _, v := range e.Values {
		var val []byte

		number := uint64(int64(v.Number)) //nolint:gosec // negative numbers are sign-extended.
		val = appendString(val, evName, v.Name)
		val = appendUint(val, evNumber, number)
		out = appendBytes(appendTag(out, edpValue, wireBytes), val)
	}

	return out
}

func messageDescriptor(m *Message) []byte {
	out := appendString(nil, dpName, m.Name)

	var entries []*Message

	for _, f := range m.Fields {
		out = appendBytes(appendTag(out, dpField, wireBytes), fieldDescriptor(m, f))

		if f.IsMap {
			entries = append(entries, mapEntryMessage(m, f))
		}
	}

	for _, n := range m.Nested {
		out = appendBytes(appendTag(out, dpNested, wireBytes), messageDescriptor(n))
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	for _, e := range entries {
		out = appendBytes(appendTag(out, dpNested, wireBytes), messageDescriptor(e))
	}

	for _, e := range m.Enums {
		out = appendBytes(appendTag(out, dpEnum, wireBytes), enumDescriptor(e))
	}

	if m.MapEntry {
		out = appendBytes(appendTag(out, dpOptions, wireBytes), appendUint(nil, optMapEntry, 1))
	}

	for _, name := range m.Oneofs {
		out = appendBytes(appendTag(out, dpOneof, wireBytes), appendString(nil, 1, name))
	}

	return out
}

// mapEntryName is the nested type protoc generates for a map field.
func mapEntryName(f *Field) string {
	var b strings.Builder

	up := true

	for _, c := range f.Name {
		if c == '_' {
			up = true

			continue
		}

		if up && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}

		up = false

		b.WriteRune(c)
	}

	return b.String() + "Entry"
}

func mapEntryMessage(parent *Message, f *Field) *Message {
	key := *f.MapKey
	val := *f.MapValue
	key.JSONName, val.JSONName = mapKeyName, mapValueName
	key.Oneof, val.Oneof = -1, -1

	return &Message{ //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		Name: mapEntryName(f), FullName: parent.FullName + "." + mapEntryName(f),
		Fields: []*Field{&key, &val}, MapEntry: true, file: parent.file,
	}
}

func fieldDescriptor(m *Message, f *Field) []byte {
	out := appendString(nil, fieldName, f.Name)
	out = appendUint(out, fieldNumber, uint64(f.Number)) //nolint:gosec // validated positive.

	label := uint64(labelOptional)

	switch f.Label {
	case LabelRepeated:
		label = labelRepeated
	case LabelRequired:
		label = labelRequired
	}

	out = appendUint(out, fieldLabel, label)

	switch {
	case f.IsMap:
		out = appendUint(out, fieldType, typeMessage)
		out = appendString(out, fieldTypeName, "."+m.FullName+"."+mapEntryName(f))
	case f.Kind == KindMessage:
		out = appendUint(out, fieldType, typeMessage)
		out = appendString(out, fieldTypeName, "."+f.Message.FullName)
	case f.Kind == KindEnum:
		out = appendUint(out, fieldType, typeEnum)
		out = appendString(out, fieldTypeName, "."+f.Enum.FullName)
	default:
		out = appendUint(out, fieldType, descriptorTypes[f.Kind])
	}

	if f.Oneof >= 0 {
		out = appendUint(out, fieldOneof, uint64(f.Oneof)) //nolint:gosec // non-negative.
	}

	out = appendString(out, fieldJSONName, f.JSONName)

	if f.Proto3Optional {
		out = appendUint(out, fieldProto3, 1)
	}

	return out
}

func serviceDescriptor(s *Service) []byte {
	out := appendString(nil, 1, s.Name)

	for _, m := range s.Methods {
		var d []byte

		d = appendString(d, 1, m.Name)
		d = appendString(d, 2, "."+m.Input.FullName)  //nolint:mnd // input_type.
		d = appendString(d, 3, "."+m.Output.FullName) //nolint:mnd // output_type.

		if m.ClientStreaming {
			d = appendUint(d, 5, 1) //nolint:mnd // client_streaming.
		}

		if m.ServerStreaming {
			d = appendUint(d, 6, 1) //nolint:mnd // server_streaming.
		}

		out = appendBytes(appendTag(out, 2, wireBytes), d) //nolint:mnd // method.
	}

	return out
}
