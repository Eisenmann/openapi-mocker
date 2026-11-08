package protobuf

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Unmarshal converts protobuf wire data of message m to a proto3-JSON
// document. Every field is emitted (zero values included), except unset
// message, optional and oneof members; unknown fields are skipped.
func Unmarshal(m *Message, data []byte) ([]byte, error) {
	var buf bytes.Buffer

	err := decodeMessage(&buf, m, data, 0)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func decodeMessage(buf *bytes.Buffer, m *Message, data []byte, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: nesting is too deep", ErrWire)
	}

	raws, err := readFields(data)
	if err != nil {
		return err
	}

	if handled, werr := decodeWellKnown(buf, m, raws, depth); handled {
		return werr
	}

	byNum := map[int][]rawField{}
	for _, r := range raws {
		byNum[r.num] = append(byNum[r.num], r)
	}

	buf.WriteByte('{')

	first := true

	for _, f := range m.Fields {
		elems := byNum[f.Number]
		if len(elems) == 0 && !emitsDefault(m, f) {
			continue
		}

		if !first {
			buf.WriteByte(',')
		}

		first = false

		key, _ := json.Marshal(f.JSONName) //nolint:errchkjson // strings always marshal
		buf.Write(key)
		buf.WriteByte(':')

		err = decodeField(buf, f, elems, depth)
		if err != nil {
			return err
		}
	}

	buf.WriteByte('}')

	return nil
}

// emitsDefault reports whether an absent field is written with its default.
func emitsDefault(m *Message, f *Field) bool {
	switch {
	case f.IsMap || f.Label == LabelRepeated:
		return true
	case f.Oneof >= 0 || f.Proto3Optional || f.Kind == KindMessage:
		return false
	case m.file.Syntax == syntaxProto2 && f.Label != LabelRequired:
		return false
	default:
		return true
	}
}

func decodeField(buf *bytes.Buffer, f *Field, elems []rawField, depth int) error {
	switch {
	case f.IsMap:
		return decodeMap(buf, f, elems, depth)
	case f.Label == LabelRepeated:
		buf.WriteByte('[')

		n := 0

		for _, e := range elems {
			items, err := elementsOf(f, e)
			if err != nil {
				return err
			}

			for _, it := range items {
				if n > 0 {
					buf.WriteByte(',')
				}

				n++

				err = decodeValue(buf, f.Kind, f.Message, f.Enum, it, depth)
				if err != nil {
					return err
				}
			}
		}

		buf.WriteByte(']')

		return nil
	case len(elems) == 0:
		return writeDefault(buf, f)
	default:
		return decodeValue(buf, f.Kind, f.Message, f.Enum, elems[len(elems)-1], depth)
	}
}

// elementsOf expands a packed run into single elements.
func elementsOf(f *Field, e rawField) ([]rawField, error) {
	if e.wt != wireBytes || !packable(f.Kind) {
		return []rawField{e}, nil
	}

	switch wireTypeOf(f.Kind) {
	case wireVarint:
		vs, err := readVarints(e.data)
		if err != nil {
			return nil, err
		}

		out := make([]rawField, len(vs))
		for i, v := range vs {
			out[i] = rawField{num: e.num, wt: wireVarint, v: v} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
		}

		return out, nil
	case wireFixed32:
		return splitFixed(e, 4, wireFixed32) //nolint:mnd // wire-format constant
	default:
		return splitFixed(e, 8, wireFixed64) //nolint:mnd // wire-format constant
	}
}

func splitFixed(e rawField, size, wt int) ([]rawField, error) {
	if len(e.data)%size != 0 {
		return nil, fmt.Errorf("%w: bad packed fixed-width field", ErrWire)
	}

	var out []rawField

	for b := e.data; len(b) > 0; b = b[size:] {
		var v uint64
		for i := size - 1; i >= 0; i-- {
			v = v<<8 | uint64(b[i]) //nolint:mnd // wire-format constant
		}

		out = append(out, rawField{num: e.num, wt: wt, v: v}) //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	}

	return out, nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func decodeMap(buf *bytes.Buffer, f *Field, elems []rawField, depth int) error {
	buf.WriteByte('{')

	n := 0

	for _, e := range elems {
		if e.wt != wireBytes {
			return fmt.Errorf("%w: map entry must be length-delimited", ErrWire)
		}

		fields, err := readFields(e.data)
		if err != nil {
			return err
		}

		var key, val *rawField

		for i := range fields {
			switch fields[i].num {
			case 1:
				key = &fields[i]
			case 2: //nolint:mnd // the value is field 2 of a map entry.
				val = &fields[i]
			}
		}

		if n > 0 {
			buf.WriteByte(',')
		}

		n++

		err = writeMapKey(buf, f.MapKey.Kind, key)
		if err != nil {
			return err
		}

		buf.WriteByte(':')

		if val == nil {
			err = writeDefault(buf, f.MapValue)
		} else {
			err = decodeValue(buf, f.MapValue.Kind, f.MapValue.Message, f.MapValue.Enum, *val, depth)
		}

		if err != nil {
			return err
		}
	}

	buf.WriteByte('}')

	return nil
}

func writeMapKey(buf *bytes.Buffer, k Kind, key *rawField) error {
	var tmp bytes.Buffer

	if key == nil {
		switch k { //nolint:exhaustive // other kinds share the default branch
		case KindString:
			tmp.WriteString(`""`)
		case KindBool:
			tmp.WriteString("false")
		default:
			tmp.WriteString("0")
		}
	} else {
		err := decodeValue(&tmp, k, nil, nil, *key, 0)
		if err != nil {
			return err
		}
	}

	s := tmp.String()
	if !strings.HasPrefix(s, `"`) {
		s = strconv.Quote(s)
	}

	buf.WriteString(s)

	return nil
}

func writeDefault(buf *bytes.Buffer, f *Field) error {
	switch f.Kind { //nolint:exhaustive // other kinds share the default branch
	case KindString:
		buf.WriteString(`""`)
	case KindBytes:
		buf.WriteString(`""`)
	case KindBool:
		buf.WriteString("false")
	case KindInt64, KindUint64, KindFixed64, KindSfixed64, KindSint64:
		buf.WriteString(`"0"`)
	case KindEnum:
		buf.WriteString(strconv.Quote(f.Enum.Values[0].Name))
	case KindMessage:
		buf.WriteString("null")
	default:
		buf.WriteString("0")
	}

	return nil
}

// decodeValue writes one wire element as JSON.
func decodeValue(buf *bytes.Buffer, k Kind, msg *Message, en *Enum, e rawField, depth int) error {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindMessage:
		if e.wt != wireBytes {
			return fmt.Errorf("%w: message field with wire type %d", ErrWire, e.wt)
		}

		return decodeMessage(buf, msg, e.data, depth+1)
	case KindString:
		if e.wt != wireBytes {
			return fmt.Errorf("%w: string field with wire type %d", ErrWire, e.wt)
		}

		b, _ := json.Marshal(strings.ToValidUTF8(string(e.data), "�")) //nolint:errchkjson // strings always marshal
		buf.Write(b)

		return nil
	case KindBytes:
		if e.wt != wireBytes {
			return fmt.Errorf("%w: bytes field with wire type %d", ErrWire, e.wt)
		}

		buf.WriteString(strconv.Quote(base64.StdEncoding.EncodeToString(e.data)))

		return nil
	}

	if e.wt == wireBytes {
		return fmt.Errorf("%w: scalar field with wire type %d", ErrWire, e.wt)
	}

	writeScalar(buf, k, en, e.v)

	return nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func writeScalar(buf *bytes.Buffer, k Kind, en *Enum, v uint64) {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindDouble:
		writeFloat(buf, math.Float64frombits(v))
	case KindFloat:
		writeFloat(buf, float64(math.Float32frombits(uint32(v)))) //nolint:gosec // fixed32 payload.
	case KindBool:
		buf.WriteString(strconv.FormatBool(v != 0))
	case KindInt32:
		buf.WriteString(strconv.FormatInt(int64(int32(v)), 10)) //nolint:gosec // truncation is the int32 semantic.
	case KindSfixed32:
		buf.WriteString(strconv.FormatInt(int64(int32(uint32(v))), 10)) //nolint:gosec // fixed32 payload.
	case KindSint32:
		buf.WriteString(strconv.FormatInt(unzigzag(v), 10))
	case KindUint32, KindFixed32:
		buf.WriteString(strconv.FormatUint(uint64(uint32(v)), 10)) //nolint:gosec // truncation is the uint32 semantic.
	case KindInt64, KindSfixed64:
		buf.WriteString(strconv.Quote(strconv.FormatInt(int64(v), 10))) //nolint:gosec // two's complement.
	case KindSint64:
		buf.WriteString(strconv.Quote(strconv.FormatInt(unzigzag(v), 10)))
	case KindUint64, KindFixed64:
		buf.WriteString(strconv.Quote(strconv.FormatUint(v, 10)))
	case KindEnum:
		n := int32(v) //nolint:gosec // enum numbers are int32.
		for _, ev := range en.Values {
			if ev.Number == n {
				buf.WriteString(strconv.Quote(ev.Name))

				return
			}
		}

		buf.WriteString(strconv.FormatInt(int64(n), 10))
	}
}

func writeFloat(buf *bytes.Buffer, f float64) {
	switch {
	case math.IsNaN(f):
		buf.WriteString(`"NaN"`)
	case math.IsInf(f, 1):
		buf.WriteString(`"Infinity"`)
	case math.IsInf(f, -1):
		buf.WriteString(`"-Infinity"`)
	default:
		buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	}
}

// Well-known types.

//nolint:cyclop // grammar and wire kinds map to branches
func decodeWellKnown(buf *bytes.Buffer, m *Message, raws []rawField, depth int) (bool, error) {
	if !strings.HasPrefix(m.FullName, "google.protobuf.") {
		return false, nil
	}

	switch m.Name {
	case wktTimestamp:
		sec, nanos := secondsNanos(raws)
		buf.WriteString(strconv.Quote(time.Unix(sec, nanos).UTC().Format(time.RFC3339Nano)))
	case wktDuration:
		sec, nanos := secondsNanos(raws)
		buf.WriteString(strconv.Quote(formatDuration(sec, nanos)))
	case wktEmpty:
		buf.WriteString("{}")
	case wktFieldMask:
		var paths []string

		for _, r := range raws {
			if r.num == 1 {
				paths = append(paths, jsonName(string(r.data)))
			}
		}

		buf.WriteString(strconv.Quote(strings.Join(paths, ",")))
	case wktStruct:
		return true, decodeStruct(buf, raws, depth)
	case wktValue:
		return true, decodeValueMsg(buf, raws, depth)
	case wktListValue:
		return true, decodeListValue(buf, raws, depth)
	default:
		if !wrapperTypes[m.Name] {
			return false, nil
		}

		return true, decodeWrapper(buf, m, raws, depth)
	}

	return true, nil
}

// decodeWrapper writes a google.protobuf wrapper message as its bare value.
func decodeWrapper(buf *bytes.Buffer, m *Message, raws []rawField, depth int) error {
	var el []rawField

	for _, r := range raws {
		if r.num == 1 {
			el = append(el, r)
		}
	}

	f := &Field{ //nolint:exhaustruct,exhaustruct_v5 // only the kind and number matter for a bare value.
		Kind: m.Fields[0].Kind, Number: 1, Oneof: -1,
	}

	return decodeField(buf, f, el, depth)
}

func secondsNanos(raws []rawField) (sec, nanos int64) {
	for _, r := range raws {
		switch r.num {
		case 1:
			sec = int64(r.v) //nolint:gosec // two's complement.
		case 2: //nolint:mnd // nanos.
			nanos = int64(int32(r.v)) //nolint:gosec // int32 payload.
		}
	}

	return sec, nanos
}

func formatDuration(sec, nanos int64) string {
	sign := ""
	if sec < 0 || nanos < 0 {
		sign, sec, nanos = "-", -sec, -nanos
	}

	if nanos == 0 {
		return fmt.Sprintf("%s%ds", sign, sec)
	}

	frac := strings.TrimRight(fmt.Sprintf("%09d", nanos), "0")

	return fmt.Sprintf("%s%d.%ss", sign, sec, frac)
}

//nolint:cyclop // grammar and wire kinds map to branches
func decodeStruct(buf *bytes.Buffer, raws []rawField, depth int) error {
	buf.WriteByte('{')

	n := 0

	for _, r := range raws {
		if r.num != 1 || r.wt != wireBytes {
			continue
		}

		entry, err := readFields(r.data)
		if err != nil {
			return err
		}

		var key string

		var val []rawField

		for _, e := range entry {
			switch e.num {
			case 1:
				key = string(e.data)
			case 2: //nolint:mnd // value.
				val, err = readFields(e.data)
				if err != nil {
					return err
				}
			}
		}

		if n > 0 {
			buf.WriteByte(',')
		}

		n++

		kb, _ := json.Marshal(key) //nolint:errchkjson // strings always marshal
		buf.Write(kb)
		buf.WriteByte(':')

		err = decodeValueMsg(buf, val, depth+1)
		if err != nil {
			return err
		}
	}

	buf.WriteByte('}')

	return nil
}

func decodeListValue(buf *bytes.Buffer, raws []rawField, depth int) error {
	buf.WriteByte('[')

	n := 0

	for _, r := range raws {
		if r.num != 1 || r.wt != wireBytes {
			continue
		}

		inner, err := readFields(r.data)
		if err != nil {
			return err
		}

		if n > 0 {
			buf.WriteByte(',')
		}

		n++

		err = decodeValueMsg(buf, inner, depth+1)
		if err != nil {
			return err
		}
	}

	buf.WriteByte(']')

	return nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func decodeValueMsg(buf *bytes.Buffer, raws []rawField, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: nesting is too deep", ErrWire)
	}

	if len(raws) == 0 {
		buf.WriteString("null")

		return nil
	}

	r := raws[len(raws)-1]

	switch r.num {
	case 2: //nolint:mnd // number_value.
		writeFloat(buf, math.Float64frombits(r.v))
	case 3: //nolint:mnd // string_value.
		b, _ := json.Marshal(strings.ToValidUTF8(string(r.data), "�")) //nolint:errchkjson // strings always marshal
		buf.Write(b)
	case 4: //nolint:mnd // bool_value.
		buf.WriteString(strconv.FormatBool(r.v != 0))
	case 5: //nolint:mnd // struct_value.
		inner, err := readFields(r.data)
		if err != nil {
			return err
		}

		return decodeStruct(buf, inner, depth+1)
	case 6: //nolint:mnd // list_value.
		inner, err := readFields(r.data)
		if err != nil {
			return err
		}

		return decodeListValue(buf, inner, depth+1)
	default:
		buf.WriteString("null")
	}

	return nil
}
