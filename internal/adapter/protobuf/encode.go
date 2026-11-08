package protobuf

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Field numbers of a map entry message.
const (
	mapKeyField   = 1
	mapValueField = 2
)

var base64Encodings = []*base64.Encoding{ //nolint:gochecknoglobals // read-only lookup table.
	base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
}

// ErrJSON is returned when a JSON value does not fit the message type.
var ErrJSON = errors.New("JSON does not match the message type")

func jsonErr(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrJSON, path, fmt.Sprintf(format, args...))
}

// Marshal converts a proto3-JSON document to the protobuf wire format of m.
func Marshal(m *Message, doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any

	err := dec.Decode(&v)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %w", ErrJSON, err)
	}

	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the JSON value", ErrJSON)
	}

	return encodeMessage(m, v, m.Name, 0)
}

//nolint:cyclop // grammar and wire kinds map to branches
func encodeMessage(m *Message, v any, path string, depth int) ([]byte, error) {
	if depth > maxDepth {
		return nil, jsonErr(path, "nesting is too deep")
	}

	if b, ok, err := encodeWellKnown(m, v, path); ok {
		return b, err
	}

	obj, ok := v.(map[string]any)
	if !ok {
		return nil, jsonErr(path, "expected an object")
	}

	for k := range obj {
		if m.byName[k] == nil {
			return nil, jsonErr(path, "unknown field %q", k)
		}
	}

	fields := slices.Clone(m.Fields)
	slices.SortFunc(fields, func(a, b *Field) int { return a.Number - b.Number })

	var out []byte

	oneofSet := map[int]string{}

	for _, f := range fields {
		val, present := obj[f.JSONName]
		if !present {
			val, present = obj[f.Name]
		}

		if !present || val == nil {
			continue
		}

		if f.Oneof >= 0 {
			if prev, dup := oneofSet[f.Oneof]; dup {
				return nil, jsonErr(path, "oneof %s has both %s and %s", m.Oneofs[f.Oneof], prev, f.Name)
			}

			oneofSet[f.Oneof] = f.Name
		}

		var err error

		out, err = appendField(out, m, f, val, path+"."+f.Name, depth)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

func appendField(out []byte, m *Message, f *Field, val any, path string, depth int) ([]byte, error) {
	switch {
	case f.IsMap:
		return appendMap(out, f, val, path, depth)
	case f.Label == LabelRepeated:
		arr, ok := val.([]any)
		if !ok {
			return nil, jsonErr(path, "expected an array")
		}

		return appendRepeated(out, f, arr, path, depth)
	default:
		return appendSingular(out, m, f, val, path, depth)
	}
}

func packable(k Kind) bool {
	return k != KindString && k != KindBytes && k != KindMessage
}

func appendRepeated(out []byte, f *Field, arr []any, path string, depth int) ([]byte, error) {
	if packable(f.Kind) {
		if len(arr) == 0 {
			return out, nil
		}

		var packed []byte

		for i, el := range arr {
			var err error

			packed, err = appendScalarValue(packed, f.Kind, f.Enum, el, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
		}

		return appendBytes(appendTag(out, f.Number, wireBytes), packed), nil
	}

	for i, el := range arr {
		var err error

		out, err = appendElement(out, f.Number, f.Kind, f.Message, f.Enum, el, fmt.Sprintf("%s[%d]", path, i), depth)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// appendElement writes a tagged string, bytes or message element.
func appendElement(
	out []byte, num int, k Kind, msg *Message, en *Enum, el any, path string, depth int,
) ([]byte, error) {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindMessage:
		b, err := encodeMessage(msg, el, path, depth+1)
		if err != nil {
			return nil, err
		}

		return appendBytes(appendTag(out, num, wireBytes), b), nil
	case KindString, KindBytes:
		b, err := stringOrBytes(k, el, path)
		if err != nil {
			return nil, err
		}

		return appendBytes(appendTag(out, num, wireBytes), b), nil
	default:
		wt := wireTypeOf(k)
		out = appendTag(out, num, wt)

		return appendScalarValue(out, k, en, el, path)
	}
}

func appendSingular(out []byte, m *Message, f *Field, val any, path string, depth int) ([]byte, error) {
	implicit := f.Label != LabelOptional && f.Label != LabelRequired && f.Oneof < 0 && m.file.Syntax == syntaxProto3

	if implicit && f.Kind != KindMessage {
		zero, err := isZeroValue(f, val, path)
		if err != nil {
			return nil, err
		}

		if zero {
			return out, nil
		}
	}

	return appendElement(out, f.Number, f.Kind, f.Message, f.Enum, val, path, depth)
}

//nolint:cyclop // grammar and wire kinds map to branches
func isZeroValue(f *Field, val any, path string) (bool, error) {
	switch f.Kind { //nolint:exhaustive // other kinds share the default branch
	case KindString:
		s, ok := val.(string)
		if !ok {
			return false, jsonErr(path, "expected a string")
		}

		return s == "", nil
	case KindBytes:
		s, ok := val.(string)
		if !ok {
			return false, jsonErr(path, "expected a base64 string")
		}

		return s == "", nil
	case KindBool:
		b, err := toBool(val, path)

		return !b, err
	case KindDouble, KindFloat:
		x, err := toFloat(val, path)

		return x == 0 && !math.Signbit(x), err
	case KindEnum:
		n, err := enumNumber(f.Enum, val, path)

		return n == 0, err
	case KindUint32, KindUint64, KindFixed32, KindFixed64:
		n, err := toUint(val, path)

		return n == 0, err
	default:
		n, err := toInt(val, path)

		return n == 0, err
	}
}

func appendMap(out []byte, f *Field, val any, path string, depth int) ([]byte, error) {
	obj, ok := val.(map[string]any)
	if !ok {
		return nil, jsonErr(path, "expected an object")
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	for _, k := range keys {
		kp := path + "[" + k + "]"

		keyVal, err := mapKeyValue(f.MapKey.Kind, k, kp)
		if err != nil {
			return nil, err
		}

		entry, err := appendElement(nil, mapKeyField, f.MapKey.Kind, nil, nil, keyVal, kp, depth)
		if err != nil {
			return nil, err
		}

		if obj[k] == nil {
			return nil, jsonErr(kp, "map values cannot be null")
		}

		entry, err = appendElement(entry, mapValueField, f.MapValue.Kind, f.MapValue.Message, f.MapValue.Enum,
			obj[k], kp, depth)
		if err != nil {
			return nil, err
		}

		out = appendBytes(appendTag(out, f.Number, wireBytes), entry)
	}

	return out, nil
}

// mapKeyValue converts a JSON object key to the value form of the key kind.
func mapKeyValue(k Kind, key, path string) (any, error) {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindBool:
		switch key {
		case jsonTrue:
			return true, nil
		case "false":
			return false, nil
		}

		return nil, jsonErr(path, "bool map keys must be true or false")
	case KindString:
		return key, nil
	default:
		return json.Number(key), nil
	}
}

func wireTypeOf(k Kind) int {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindDouble, KindFixed64, KindSfixed64:
		return wireFixed64
	case KindFloat, KindFixed32, KindSfixed32:
		return wireFixed32
	case KindString, KindBytes, KindMessage:
		return wireBytes
	default:
		return wireVarint
	}
}

func stringOrBytes(k Kind, v any, path string) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, jsonErr(path, "expected a string")
	}

	if k == KindString {
		return []byte(s), nil
	}

	for _, enc := range base64Encodings {
		b, err := enc.DecodeString(s)
		if err == nil {
			return b, nil
		}
	}

	return nil, jsonErr(path, "bytes must be a base64 string")
}

// appendScalarValue appends the untagged encoding of a scalar value.
func appendScalarValue(out []byte, k Kind, en *Enum, v any, path string) ([]byte, error) {
	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindDouble:
		x, err := toFloat(v, path)

		return appendFixed64(out, f64bits(x)), err
	case KindFloat:
		x, err := toFloat(v, path)

		return appendFixed32(out, f32bits(float32(x))), err
	case KindBool:
		b, err := toBool(v, path)
		if b {
			return appendVarint(out, 1), err
		}

		return appendVarint(out, 0), err
	case KindEnum:
		n, err := enumNumber(en, v, path)

		return appendVarint(out, uint64(int64(n))), err //nolint:gosec // negative enums are sign-extended on the wire.
	case KindUint32, KindUint64, KindFixed32, KindFixed64:
		return appendUnsigned(out, k, v, path)
	default:
		return appendSigned(out, k, v, path)
	}
}

func appendUnsigned(out []byte, k Kind, v any, path string) ([]byte, error) {
	u, err := toUint(v, path)
	if err != nil {
		return nil, err
	}

	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindUint32:
		if u > math.MaxUint32 {
			return nil, jsonErr(path, "value %d is out of range for uint32", u)
		}

		return appendVarint(out, u), nil
	case KindFixed32:
		if u > math.MaxUint32 {
			return nil, jsonErr(path, "value %d is out of range for fixed32", u)
		}

		return appendFixed32(out, uint32(u)), nil
	case KindFixed64:
		return appendFixed64(out, u), nil
	default:
		return appendVarint(out, u), nil
	}
}

func appendSigned(out []byte, k Kind, v any, path string) ([]byte, error) {
	n, err := toInt(v, path)
	if err != nil {
		return nil, err
	}

	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindInt32, KindSint32, KindSfixed32:
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, jsonErr(path, "value %d is out of range for a 32-bit integer", n)
		}
	}

	switch k { //nolint:exhaustive // other kinds share the default branch
	case KindInt32, KindInt64:
		return appendVarint(out, uint64(n)), nil //nolint:gosec // two's complement on the wire.
	case KindSint32:
		return appendVarint(out, zigzag32(int32(n))), nil //nolint:gosec // range checked.
	case KindSint64:
		return appendVarint(out, zigzag64(n)), nil
	case KindSfixed32:
		return appendFixed32(out, uint32(int32(n))), nil //nolint:gosec // range checked.
	default:
		return appendFixed64(out, uint64(n)), nil //nolint:gosec // two's complement on the wire.
	}
}

func numberText(v any) (string, bool) {
	switch x := v.(type) {
	case json.Number:
		return x.String(), true
	case string:
		return strings.TrimSpace(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}

	return "", false
}

func toInt(v any, path string) (int64, error) {
	s, ok := numberText(v)
	if !ok {
		return 0, jsonErr(path, "expected an integer")
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return n, nil
	}

	// "1e3" and "2.0" are valid JSON spellings of integers.
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
		return int64(f), nil
	}

	return 0, jsonErr(path, "%q is not a valid integer", s)
}

func toUint(v any, path string) (uint64, error) {
	s, ok := numberText(v)
	if !ok {
		return 0, jsonErr(path, "expected an unsigned integer")
	}

	n, err := strconv.ParseUint(s, 10, 64)
	if err == nil {
		return n, nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err == nil && f == math.Trunc(f) && f >= 0 && f < 1<<64 {
		return uint64(f), nil
	}

	return 0, jsonErr(path, "%q is not a valid unsigned integer", s)
}

func toFloat(v any, path string) (float64, error) {
	s, ok := numberText(v)
	if !ok {
		return 0, jsonErr(path, "expected a number")
	}

	switch s {
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		return math.Inf(1), nil
	case "-Infinity":
		return math.Inf(-1), nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, jsonErr(path, "%q is not a valid number", s)
	}

	return f, nil
}

func toBool(v any, path string) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch x {
		case jsonTrue:
			return true, nil
		case "false":
			return false, nil
		}
	}

	return false, jsonErr(path, "expected true or false")
}

func enumNumber(e *Enum, v any, path string) (int32, error) {
	if s, ok := v.(string); ok {
		for _, ev := range e.Values {
			if ev.Name == s {
				return ev.Number, nil
			}
		}

		if _, isNum := numberText(json.Number(s)); !isNum || !looksNumeric(s) {
			return 0, jsonErr(path, "%q is not a value of enum %s", s, e.Name)
		}
	}

	n, err := toInt(v, path)
	if err != nil {
		return 0, err
	}

	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, jsonErr(path, "enum number %d is out of range", n)
	}

	return int32(n), nil //nolint:gosec // range checked.
}

func looksNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)

	return err == nil
}

// Well-known types.

//nolint:cyclop // grammar and wire kinds map to branches
func encodeWellKnown(m *Message, v any, path string) (b []byte, handled bool, err error) {
	if !strings.HasPrefix(m.FullName, "google.protobuf.") {
		return nil, false, nil
	}

	switch m.Name {
	case wktTimestamp:
		b, err = encodeTimestamp(v, path)
	case wktDuration:
		b, err = encodeDuration(v, path)
	case wktEmpty:
		if obj, ok := v.(map[string]any); !ok || len(obj) > 0 {
			err = jsonErr(path, "Empty must be {}")
		}
	case wktFieldMask:
		b, err = encodeFieldMask(v, path)
	case wktStruct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, true, jsonErr(path, "expected an object")
		}

		b, err = encodeStruct(obj, path)
	case wktValue:
		b, err = encodeValue(v, path)
	case wktListValue:
		arr, ok := v.([]any)
		if !ok {
			return nil, true, jsonErr(path, "expected an array")
		}

		b, err = encodeList(arr, path)
	default:
		if !wrapperTypes[m.Name] {
			return nil, false, nil
		}

		b, err = encodeWrapper(m, v, path)
	}

	return b, true, err
}

func encodeWrapper(m *Message, v any, path string) ([]byte, error) {
	f := m.Fields[0]

	if v == nil {
		return nil, nil
	}

	return appendElement(nil, f.Number, f.Kind, nil, nil, v, path, 0)
}

func encodeTimestamp(v any, path string) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, jsonErr(path, "a Timestamp is an RFC 3339 string")
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, jsonErr(path, "invalid Timestamp %q", s)
	}

	return appendSecondsNanos(t.Unix(), int64(t.Nanosecond())), nil
}

// Field numbers of Timestamp and Duration.
const (
	fieldSeconds = 1
	fieldNanos   = 2
)

func appendSecondsNanos(sec, nanos int64) []byte {
	var out []byte

	if sec != 0 {
		out = appendVarint(appendTag(out, fieldSeconds, wireVarint), uint64(sec)) //nolint:gosec // two's complement.
	}

	if nanos != 0 {
		out = appendVarint(appendTag(out, fieldNanos, wireVarint), uint64(nanos)) //nolint:gosec // two's complement.
	}

	return out
}

func encodeDuration(v any, path string) ([]byte, error) {
	s, ok := v.(string)
	if !ok || !strings.HasSuffix(s, "s") {
		return nil, jsonErr(path, `a Duration is a string like "1.5s"`)
	}

	s = strings.TrimSuffix(s, "s")
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")

	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || len(frac) > 9 { //nolint:mnd // nanosecond precision.
		return nil, jsonErr(path, "invalid Duration %q", s)
	}

	var nanos int64

	if frac != "" {
		nanos, err = strconv.ParseInt((frac + "000000000")[:9], 10, 64)
		if err != nil {
			return nil, jsonErr(path, "invalid Duration %q", s)
		}
	}

	if neg {
		sec, nanos = -sec, -nanos
	}

	return appendSecondsNanos(sec, nanos), nil
}

func encodeFieldMask(v any, path string) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, jsonErr(path, "a FieldMask is a comma separated string")
	}

	var out []byte

	if s == "" {
		return nil, nil
	}

	for _, p := range strings.Split(s, ",") {
		out = appendBytes(appendTag(out, 1, wireBytes), []byte(snakeCase(p)))
	}

	return out, nil
}

func snakeCase(s string) string {
	var b strings.Builder

	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}

			c += 'a' - 'A'
		}

		b.WriteRune(c)
	}

	return b.String()
}

func encodeStruct(obj map[string]any, path string) ([]byte, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	var out []byte

	for _, k := range keys {
		val, err := encodeValue(obj[k], path+"."+k)
		if err != nil {
			return nil, err
		}

		entry := appendBytes(appendTag(nil, 1, wireBytes), []byte(k))
		entry = appendBytes(appendTag(entry, 2, wireBytes), val) //nolint:mnd // wire-format constant
		out = appendBytes(appendTag(out, 1, wireBytes), entry)
	}

	return out, nil
}

func encodeList(arr []any, path string) ([]byte, error) {
	var out []byte

	for i, el := range arr {
		val, err := encodeValue(el, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}

		out = appendBytes(appendTag(out, 1, wireBytes), val)
	}

	return out, nil
}

// encodeValue encodes any JSON value as a google.protobuf.Value.
func encodeValue(v any, path string) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return appendVarint(appendTag(nil, 1, wireVarint), 0), nil
	case bool:
		b := uint64(0)
		if x {
			b = 1
		}

		return appendVarint(appendTag(nil, 4, wireVarint), b), nil //nolint:mnd // wire-format constant
	case string:
		return appendBytes(appendTag(nil, 3, wireBytes), []byte(x)), nil //nolint:mnd // wire-format constant
	case json.Number:
		f, err := toFloat(x, path)

		return appendFixed64(appendTag(nil, 2, wireFixed64), f64bits(f)), err //nolint:mnd // wire-format constant
	case float64:
		return appendFixed64(appendTag(nil, 2, wireFixed64), f64bits(x)), nil //nolint:mnd // wire-format constant
	case map[string]any:
		s, err := encodeStruct(x, path)

		return appendBytes(appendTag(nil, 5, wireBytes), s), err //nolint:mnd // wire-format constant
	case []any:
		l, err := encodeList(x, path)

		return appendBytes(appendTag(nil, 6, wireBytes), l), err //nolint:mnd // wire-format constant
	}

	return nil, jsonErr(path, "unsupported JSON value")
}
