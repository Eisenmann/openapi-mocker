// Package protobuf is a standard-library implementation of the parts of
// Protocol Buffers the gRPC mock needs: a .proto parser, a proto3-JSON <->
// wire codec, example generation and descriptor encoding (for server
// reflection). Only the well-known types may be imported.
package protobuf

// Kind classifies the type of a field.
type Kind int

// Field kinds.
const (
	KindDouble Kind = iota + 1
	KindFloat
	KindInt64
	KindUint64
	KindInt32
	KindFixed64
	KindFixed32
	KindBool
	KindString
	KindBytes
	KindUint32
	KindSfixed32
	KindSfixed64
	KindSint32
	KindSint64
	KindMessage
	KindEnum
)

// Field labels.
const (
	LabelNone     = ""
	LabelOptional = "optional"
	LabelRepeated = "repeated"
	LabelRequired = "required"
)

// File is one parsed .proto file.
type File struct {
	Name     string
	Package  string
	Syntax   string
	Imports  []string
	Messages []*Message
	Enums    []*Enum
	Services []*Service
}

// Message is a message type.
type Message struct {
	Name     string
	FullName string // without a leading dot, e.g. "shop.v1.Item".
	Fields   []*Field
	Nested   []*Message
	Enums    []*Enum
	Oneofs   []string
	MapEntry bool
	file     *File
	byNum    map[int]*Field
	byName   map[string]*Field
}

// Field is a message field.
type Field struct {
	Name     string
	JSONName string
	Number   int
	Label    string
	Kind     Kind
	// TypeName is the type as written (scalar name or a type reference).
	TypeName string
	Message  *Message // set for KindMessage once resolved.
	Enum     *Enum    // set for KindEnum once resolved.
	// Oneof is the index into Message.Oneofs, or -1.
	Oneof int
	// Proto3Optional marks a proto3 "optional" field (it has presence).
	Proto3Optional bool
	// Map fields: the key kind and the value field (Number 2 of the entry).
	IsMap    bool
	MapKey   *Field
	MapValue *Field
}

// Enum is an enum type.
type Enum struct {
	Name     string
	FullName string
	Values   []EnumValue
}

// EnumValue is one enum constant.
type EnumValue struct {
	Name   string
	Number int32
}

// Service is a gRPC service.
type Service struct {
	Name     string
	FullName string
	Methods  []*Method
}

// Method is a gRPC method.
type Method struct {
	Name            string
	Input           *Message
	Output          *Message
	InputType       string
	OutputType      string
	ClientStreaming bool
	ServerStreaming bool
}

// Path is the HTTP/2 path of the method: "/pkg.Service/Method".
func (m *Method) Path(s *Service) string { return "/" + s.FullName + "/" + m.Name }

// Registry holds parsed files and resolves symbols across them.
type Registry struct {
	Files    []*File
	messages map[string]*Message
	enums    map[string]*Enum
	services map[string]*Service
	byFile   map[string]*File
}

// Message looks a message up by full name.
func (r *Registry) Message(full string) *Message { return r.messages[full] }

// Enum looks an enum up by full name.
func (r *Registry) Enum(full string) *Enum { return r.enums[full] }

// Service looks a service up by full name.
func (r *Registry) Service(full string) *Service { return r.services[full] }

// File looks a file up by name.
func (r *Registry) File(name string) *File { return r.byFile[name] }

// Services lists the services of the user's file (not of imported
// well-known types), in declaration order.
func (r *Registry) Services() []*Service {
	var out []*Service

	for _, f := range r.Files {
		if !isWellKnownFile(f.Name) {
			out = append(out, f.Services...)
		}
	}

	return out
}

// Field returns the field with the given number.
func (m *Message) Field(num int) *Field { return m.byNum[num] }

// FieldByName finds a field by proto name or JSON name.
func (m *Message) FieldByName(name string) *Field { return m.byName[name] }

// File returns the file that declares the message.
func (m *Message) File() *File { return m.file }
