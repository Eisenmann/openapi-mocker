package protobuf

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

const exampleDepth = 4

// Example builds a deterministic sample proto3-JSON document for m, with one
// element in every repeated field and map, and the first member of each oneof.
func Example(m *Message) []byte {
	var buf bytes.Buffer

	writeExample(&buf, m, 0, map[string]bool{})

	return buf.Bytes()
}

func writeExample(buf *bytes.Buffer, m *Message, depth int, active map[string]bool) {
	if wellKnownExample(buf, m) {
		return
	}

	if depth >= exampleDepth || active[m.FullName] {
		buf.WriteString("{}")

		return
	}

	active[m.FullName] = true

	defer delete(active, m.FullName)

	buf.WriteByte('{')

	seenOneof := map[int]bool{}
	n := 0

	for _, f := range m.Fields {
		if f.Oneof >= 0 {
			if seenOneof[f.Oneof] {
				continue
			}

			seenOneof[f.Oneof] = true
		}

		if n > 0 {
			buf.WriteByte(',')
		}

		n++

		key, _ := json.Marshal(f.JSONName) //nolint:errchkjson // strings always marshal
		buf.Write(key)
		buf.WriteByte(':')

		writeFieldExample(buf, f, depth, active)
	}

	buf.WriteByte('}')
}

func writeFieldExample(buf *bytes.Buffer, f *Field, depth int, active map[string]bool) {
	switch {
	case f.IsMap:
		buf.WriteByte('{')

		key, _ := json.Marshal(mapKeyExample(f.MapKey.Kind)) //nolint:errchkjson // strings always marshal
		buf.Write(key)
		buf.WriteByte(':')
		writeValueExample(buf, f.MapValue, mapValueName, depth, active)
		buf.WriteByte('}')
	case f.Label == LabelRepeated:
		buf.WriteByte('[')
		writeValueExample(buf, f, f.Name, depth, active)
		buf.WriteByte(']')
	default:
		writeValueExample(buf, f, f.Name, depth, active)
	}
}

func mapKeyExample(k Kind) string {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindString:
		return mapKeyName
	case KindBool:
		return "true"
	default:
		return "1"
	}
}

func writeValueExample(buf *bytes.Buffer, f *Field, name string, depth int, active map[string]bool) {
	switch f.Kind { //nolint:exhaustive // other kinds share the default branch
	case KindMessage:
		writeExample(buf, f.Message, depth+1, active)
	case KindString:
		b, _ := json.Marshal(name) //nolint:errchkjson // strings always marshal
		buf.Write(b)
	case KindBytes:
		buf.WriteString(strconv.Quote(base64.StdEncoding.EncodeToString([]byte(name))))
	case KindBool:
		buf.WriteString("true")
	case KindDouble, KindFloat:
		buf.WriteString("1.5")
	case KindEnum:
		ev := f.Enum.Values[0]
		if len(f.Enum.Values) > 1 {
			ev = f.Enum.Values[1]
		}

		buf.WriteString(strconv.Quote(ev.Name))
	case KindInt64, KindUint64, KindFixed64, KindSfixed64, KindSint64:
		buf.WriteString(`"1"`)
	default:
		buf.WriteString("1")
	}
}

// wellKnownExample writes the JSON form of a well-known type.
//
//nolint:cyclop // grammar and wire kinds map to branches
func wellKnownExample(buf *bytes.Buffer, m *Message) bool {
	if !strings.HasPrefix(m.FullName, "google.protobuf.") {
		return false
	}

	switch m.Name {
	case wktTimestamp:
		buf.WriteString(`"2024-01-01T00:00:00Z"`)
	case wktDuration:
		buf.WriteString(`"1s"`)
	case wktFieldMask:
		buf.WriteString(`"name"`)
	case wktStruct:
		buf.WriteString("{}")
	case wktValue:
		buf.WriteString("null")
	case wktListValue:
		buf.WriteString("[]")
	case "StringValue":
		buf.WriteString(`"string"`)
	case "BoolValue":
		buf.WriteString("true")
	case "Int64Value", "UInt64Value":
		buf.WriteString(`"1"`)
	case "Int32Value", "UInt32Value":
		buf.WriteString("1")
	case "DoubleValue", "FloatValue":
		buf.WriteString("1.5")
	case "BytesValue":
		buf.WriteString(`"ZXhhbXBsZQ=="`)
	default:
		return false
	}

	return true
}
