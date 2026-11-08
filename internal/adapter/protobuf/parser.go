package protobuf

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ErrSyntax is returned for a .proto file that cannot be parsed.
var ErrSyntax = errors.New("proto syntax error")

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokInt
	tokFloat
	tokString
	tokSym
)

type token struct {
	kind tokKind
	text string
	line int
}

// Lexer.

type lexer struct {
	rs   []rune
	i    int
	line int
	toks []token
}

func lex(src string) ([]token, error) {
	l := &lexer{rs: []rune(src), i: 0, line: 1, toks: nil}

	for l.i < len(l.rs) {
		err := l.step()
		if err != nil {
			return nil, err
		}
	}

	return append(l.toks, token{tokEOF, "", l.line}), nil
}

func (l *lexer) emit(kind tokKind, text string) {
	l.toks = append(l.toks, token{kind, text, l.line})
}

func (l *lexer) peekIs(offset int, c rune) bool {
	return l.i+offset < len(l.rs) && l.rs[l.i+offset] == c
}

// step consumes one lexical element.
//
//nolint:cyclop // a flat character-class dispatch is clearer than splitting it.
func (l *lexer) step() error {
	c := l.rs[l.i]

	switch {
	case c == '\n':
		l.line++
		l.i++
	case unicode.IsSpace(c):
		l.i++
	case c == '/' && l.peekIs(1, '/'):
		for l.i < len(l.rs) && l.rs[l.i] != '\n' {
			l.i++
		}
	case c == '/' && l.peekIs(1, '*'):
		return l.blockComment()
	case c == '"' || c == '\'':
		return l.stringLit()
	case unicode.IsLetter(c) || c == '_':
		l.ident()
	case unicode.IsDigit(c) || (c == '.' && l.i+1 < len(l.rs) && unicode.IsDigit(l.rs[l.i+1])):
		l.number()
	default:
		l.emit(tokSym, string(c))
		l.i++
	}

	return nil
}

func (l *lexer) blockComment() error {
	l.i += 2

	for l.i+1 < len(l.rs) && (l.rs[l.i] != '*' || l.rs[l.i+1] != '/') {
		if l.rs[l.i] == '\n' {
			l.line++
		}

		l.i++
	}

	if l.i+1 >= len(l.rs) {
		return fmt.Errorf("%w: line %d: unterminated comment", ErrSyntax, l.line)
	}

	l.i += 2

	return nil
}

func (l *lexer) ident() {
	j := l.i
	for j < len(l.rs) && (unicode.IsLetter(l.rs[j]) || unicode.IsDigit(l.rs[j]) || l.rs[j] == '_') {
		j++
	}

	l.emit(tokIdent, string(l.rs[l.i:j]))
	l.i = j
}

// number lexes an integer or floating point literal (including hex and exponents).
//
//nolint:cyclop // a flat number-literal scanner is clearer than splitting it.
func (l *lexer) number() {
	j := l.i
	hex := l.peekIs(0, '0') && (l.peekIs(1, 'x') || l.peekIs(1, 'X'))
	isFloat := false

	for j < len(l.rs) {
		c := l.rs[j]
		exp := !hex && (c == 'e' || c == 'E')
		sign := (c == '+' || c == '-') && j > l.i && !hex && (l.rs[j-1] == 'e' || l.rs[j-1] == 'E')

		if !unicode.IsDigit(c) && !unicode.IsLetter(c) && c != '.' && !sign {
			break
		}

		isFloat = isFloat || c == '.' || exp
		j++
	}

	kind := tokInt
	if isFloat {
		kind = tokFloat
	}

	l.emit(kind, string(l.rs[l.i:j]))
	l.i = j
}

func (l *lexer) stringLit() error {
	text, next, err := lexString(l.rs, l.i, l.line)
	if err != nil {
		return err
	}

	l.emit(tokString, text)
	l.i = next

	return nil
}

func lexString(rs []rune, start, line int) (text string, next int, err error) {
	quote := rs[start]

	var b strings.Builder

	for i := start + 1; i < len(rs); i++ {
		c := rs[i]

		switch {
		case c == quote:
			return b.String(), i + 1, nil
		case c == '\n':
			return "", 0, fmt.Errorf("%w: line %d: unterminated string", ErrSyntax, line)
		case c == '\\' && i+1 < len(rs):
			i++
			b.WriteRune(unescape(rs[i]))
		default:
			b.WriteRune(c)
		}
	}

	return "", 0, fmt.Errorf("%w: line %d: unterminated string", ErrSyntax, line)
}

func unescape(c rune) rune {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	default:
		return c
	}
}

// Parser.

type parser struct {
	toks []token
	pos  int
	file *File
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}

	return t
}

func (p *parser) errf(t token, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrSyntax, t.line, fmt.Sprintf(format, args...))
}

func (p *parser) isSym(s string) bool {
	t := p.peek()

	return t.kind == tokSym && t.text == s
}

func (p *parser) acceptSym(s string) bool {
	if p.isSym(s) {
		p.pos++

		return true
	}

	return false
}

func (p *parser) expectSym(s string) error {
	t := p.next()
	if t.kind != tokSym || t.text != s {
		return p.errf(t, "expected %q, found %q", s, t.text)
	}

	return nil
}

func (p *parser) ident() (string, error) {
	t := p.next()
	if t.kind != tokIdent {
		return "", p.errf(t, "expected an identifier, found %q", t.text)
	}

	return t.text, nil
}

// typeRef reads a possibly dotted, possibly leading-dot type name.
func (p *parser) typeRef() (string, error) {
	var b strings.Builder

	if p.acceptSym(".") {
		b.WriteString(".")
	}

	id, err := p.ident()
	if err != nil {
		return "", err
	}

	b.WriteString(id)

	for p.isSym(".") {
		p.pos++

		id, err = p.ident()
		if err != nil {
			return "", err
		}

		b.WriteString("." + id)
	}

	return b.String(), nil
}

func (p *parser) number() (int, error) {
	neg := p.acceptSym("-")
	t := p.next()

	if t.kind != tokInt {
		return 0, p.errf(t, "expected a number, found %q", t.text)
	}

	n, err := strconv.ParseInt(t.text, 0, 64)
	if err != nil {
		return 0, p.errf(t, "bad number %q", t.text)
	}

	if neg {
		n = -n
	}

	return int(n), nil
}

// parseFile parses one .proto source.
func parseFile(name, src string) (*File, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}

	//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	p := &parser{toks: toks, file: &File{Name: name, Syntax: syntaxProto2}}

	for p.peek().kind != tokEOF {
		err = p.topLevel()
		if err != nil {
			return nil, err
		}
	}

	return p.file, nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) topLevel() error {
	t := p.next()
	if t.kind == tokSym && t.text == ";" {
		return nil
	}

	if t.kind != tokIdent {
		return p.errf(t, "unexpected %q", t.text)
	}

	switch t.text {
	case "syntax":
		return p.syntax()
	case "package":
		name, err := p.typeRef()
		if err != nil {
			return err
		}

		p.file.Package = name

		return p.expectSym(";")
	case "import":
		return p.importStmt()
	case kwOption:
		return p.skipStatement()
	case "message":
		m, err := p.message(p.file.Package)
		if err != nil {
			return err
		}

		p.file.Messages = append(p.file.Messages, m)

		return nil
	case "enum":
		e, err := p.enum(p.file.Package)
		if err != nil {
			return err
		}

		p.file.Enums = append(p.file.Enums, e)

		return nil
	case "service":
		s, err := p.service()
		if err != nil {
			return err
		}

		p.file.Services = append(p.file.Services, s)

		return nil
	case "extend":
		return p.errf(t, "extend is not supported")
	default:
		return p.errf(t, "unexpected %q", t.text)
	}
}

func (p *parser) syntax() error {
	err := p.expectSym("=")
	if err != nil {
		return err
	}

	t := p.next()
	if t.kind != tokString || (t.text != syntaxProto2 && t.text != syntaxProto3) {
		return p.errf(t, `syntax must be "proto2" or "proto3"`)
	}

	p.file.Syntax = t.text

	return p.expectSym(";")
}

func (p *parser) importStmt() error {
	if t := p.peek(); t.kind == tokIdent && (t.text == "public" || t.text == "weak") {
		p.pos++
	}

	t := p.next()
	if t.kind != tokString {
		return p.errf(t, "expected a file name")
	}

	if t.text == "" {
		return p.errf(t, "an import needs a file name")
	}

	p.file.Imports = append(p.file.Imports, t.text)

	return p.expectSym(";")
}

// skipStatement skips to the end of a statement, honouring {} and [].
//
//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) skipStatement() error {
	depth := 0

	for {
		t := p.next()

		switch {
		case t.kind == tokEOF:
			return p.errf(t, "unexpected end of file")
		case t.kind == tokSym && (t.text == "{" || t.text == "["):
			depth++
		case t.kind == tokSym && (t.text == "}" || t.text == "]"):
			depth--
		case t.kind == tokSym && t.text == ";" && depth <= 0:
			return nil
		}
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) enum(prefix string) (*Enum, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}

	e := &Enum{Name: name, FullName: join(prefix, name)} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero

	err = p.expectSym("{")
	if err != nil {
		return nil, err
	}

	for !p.acceptSym("}") {
		t := p.next()

		switch {
		case t.kind == tokEOF:
			return nil, p.errf(t, "unterminated enum %s", name)
		case t.kind == tokSym && t.text == ";":
		case t.kind == tokIdent && (t.text == kwOption || t.text == "reserved"):
			err = p.skipStatement()
			if err != nil {
				return nil, err
			}
		case t.kind == tokIdent:
			err = p.expectSym("=")
			if err != nil {
				return nil, err
			}

			n, nerr := p.number()
			if nerr != nil {
				return nil, nerr
			}

			err = p.skipFieldOptions()
			if err != nil {
				return nil, err
			}

			//nolint:gosec // enum numbers are int32 by definition.
			e.Values = append(e.Values, EnumValue{Name: t.text, Number: int32(n)})
		default:
			return nil, p.errf(t, "unexpected %q in enum", t.text)
		}
	}

	if len(e.Values) == 0 {
		return nil, p.errf(p.peek(), "enum %s has no values", name)
	}

	return e, nil
}

// skipFieldOptions consumes "[...]" (if any) and the closing ";".
func (p *parser) skipFieldOptions() error {
	if p.isSym("[") {
		depth := 0

		for {
			t := p.next()

			switch {
			case t.kind == tokEOF:
				return p.errf(t, "unterminated option list")
			case t.kind == tokSym && t.text == "[":
				depth++
			case t.kind == tokSym && t.text == "]":
				depth--

				if depth == 0 {
					return p.expectSym(";")
				}
			}
		}
	}

	return p.expectSym(";")
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) service() (*Service, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}

	//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	s := &Service{Name: name, FullName: join(p.file.Package, name)}

	err = p.expectSym("{")
	if err != nil {
		return nil, err
	}

	for !p.acceptSym("}") {
		t := p.next()

		switch {
		case t.kind == tokEOF:
			return nil, p.errf(t, "unterminated service %s", name)
		case t.kind == tokSym && t.text == ";":
		case t.kind == tokIdent && t.text == kwOption:
			err = p.skipStatement()
			if err != nil {
				return nil, err
			}
		case t.kind == tokIdent && t.text == "rpc":
			m, merr := p.rpc()
			if merr != nil {
				return nil, merr
			}

			s.Methods = append(s.Methods, m)
		default:
			return nil, p.errf(t, "unexpected %q in service", t.text)
		}
	}

	return s, nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) rpc() (*Method, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}

	m := &Method{Name: name} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero

	m.InputType, m.ClientStreaming, err = p.rpcType()
	if err != nil {
		return nil, err
	}

	t := p.next()
	if t.kind != tokIdent || t.text != "returns" {
		return nil, p.errf(t, `expected "returns"`)
	}

	m.OutputType, m.ServerStreaming, err = p.rpcType()
	if err != nil {
		return nil, err
	}

	if p.acceptSym("{") {
		for !p.acceptSym("}") {
			t = p.next()
			if t.kind == tokEOF {
				return nil, p.errf(t, "unterminated rpc body")
			}

			if t.kind == tokIdent && t.text == kwOption {
				err = p.skipStatement()
				if err != nil {
					return nil, err
				}
			}
		}

		p.acceptSym(";")

		return m, nil
	}

	return m, p.expectSym(";")
}

func (p *parser) rpcType() (typ string, stream bool, err error) {
	err = p.expectSym("(")
	if err != nil {
		return "", false, err
	}

	if t := p.peek(); t.kind == tokIdent && t.text == "stream" {
		// "stream" could also be a type named stream; a following type means keyword.
		if nt := p.toks[p.pos+1]; nt.kind == tokIdent || (nt.kind == tokSym && nt.text == ".") {
			p.pos++

			stream = true
		}
	}

	typ, err = p.typeRef()
	if err != nil {
		return "", false, err
	}

	return typ, stream, p.expectSym(")")
}

var scalarKinds = map[string]Kind{
	"double": KindDouble, "float": KindFloat, "int32": KindInt32, "int64": KindInt64,
	"uint32": KindUint32, "uint64": KindUint64, "sint32": KindSint32, "sint64": KindSint64,
	"fixed32": KindFixed32, "fixed64": KindFixed64, "sfixed32": KindSfixed32, "sfixed64": KindSfixed64,
	"bool": KindBool, "string": KindString, "bytes": KindBytes,
}

func (p *parser) message(prefix string) (*Message, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}

	//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero
	m := &Message{Name: name, FullName: join(prefix, name), file: p.file}

	err = p.expectSym("{")
	if err != nil {
		return nil, err
	}

	for !p.acceptSym("}") {
		err = p.messageItem(m, -1)
		if err != nil {
			return nil, err
		}
	}

	return m, nil
}

// messageItem parses one item of a message body; oneof is the index of the
// enclosing oneof or -1.
//
//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) messageItem(m *Message, oneof int) error {
	t := p.peek()

	switch {
	case t.kind == tokEOF:
		return p.errf(t, "unterminated message %s", m.Name)
	case t.kind == tokSym && t.text == ";":
		p.pos++

		return nil
	case t.kind != tokIdent:
		return p.errf(t, "unexpected %q in message", t.text)
	}

	switch t.text {
	case kwOption, "reserved", "extensions":
		p.pos++

		return p.skipStatement()
	case "message":
		p.pos++

		nm, err := p.message(m.FullName)
		if err == nil {
			m.Nested = append(m.Nested, nm)
		}

		return err
	case "enum":
		p.pos++

		e, err := p.enum(m.FullName)
		if err == nil {
			m.Enums = append(m.Enums, e)
		}

		return err
	case "oneof":
		p.pos++

		return p.oneof(m)
	case "extend", "group":
		return p.errf(t, "%s is not supported", t.text)
	default:
		return p.field(m, oneof)
	}
}

func (p *parser) oneof(m *Message) error {
	name, err := p.ident()
	if err != nil {
		return err
	}

	idx := len(m.Oneofs)
	m.Oneofs = append(m.Oneofs, name)

	err = p.expectSym("{")
	if err != nil {
		return err
	}

	for !p.acceptSym("}") {
		if t := p.peek(); t.kind == tokIdent && t.text == kwOption {
			p.pos++

			err = p.skipStatement()
			if err != nil {
				return err
			}

			continue
		}

		err = p.messageItem(m, idx)
		if err != nil {
			return err
		}
	}

	return nil
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) field(m *Message, oneof int) error {
	f := &Field{Oneof: oneof} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero

	if t := p.peek(); t.kind == tokIdent &&
		(t.text == LabelRepeated || t.text == LabelOptional || t.text == LabelRequired) {
		next := p.toks[p.pos+1]
		// "optional" could in theory be a type name; treat it as a label only when a type follows.
		if next.kind == tokIdent || (next.kind == tokSym && next.text == ".") {
			f.Label = t.text
			p.pos++
		}
	}

	if t := p.peek(); t.kind == tokIdent && t.text == "map" && p.toks[p.pos+1].text == "<" {
		return p.mapField(m, f)
	}

	typ, err := p.typeRef()
	if err != nil {
		return err
	}

	f.TypeName = typ

	return p.fieldRest(m, f)
}

//nolint:cyclop // grammar and wire kinds map to branches
func (p *parser) fieldRest(m *Message, f *Field) error {
	name, err := p.ident()
	if err != nil {
		return err
	}

	f.Name = name

	err = p.expectSym("=")
	if err != nil {
		return err
	}

	f.Number, err = p.number()
	if err != nil {
		return err
	}

	f.JSONName = jsonName(name)

	if p.isSym("[") {
		start := p.pos

		err = p.skipFieldOptions()
		if err != nil {
			return err
		}

		// Pick json_name out of the skipped options.
		for i := start; i < p.pos-2; i++ {
			if p.toks[i].text == "json_name" && p.toks[i+1].text == "=" && p.toks[i+2].kind == tokString {
				f.JSONName = p.toks[i+2].text
			}
		}
	} else {
		err = p.expectSym(";")
		if err != nil {
			return err
		}
	}

	if k, ok := scalarKinds[f.TypeName]; ok {
		f.Kind = k
	} else if !f.IsMap {
		f.Kind = KindMessage // refined to KindEnum when resolved.
	}

	if f.Label == LabelOptional && p.file.Syntax == syntaxProto3 {
		f.Proto3Optional = true
	}

	m.Fields = append(m.Fields, f)

	return nil
}

// mapField parses a map<K, V> field and its synthetic entry message.
func (p *parser) mapField(m *Message, f *Field) error {
	p.pos += 2 // Skip "map" and "<".

	keyType, err := p.typeRef()
	if err != nil {
		return err
	}

	err = p.expectSym(",")
	if err != nil {
		return err
	}

	valType, err := p.typeRef()
	if err != nil {
		return err
	}

	err = p.expectSym(">")
	if err != nil {
		return err
	}

	if kk, ok := scalarKinds[keyType]; !ok || kk == KindDouble || kk == KindFloat || kk == KindBytes {
		return p.errf(p.peek(), "invalid map key type %q", keyType)
	}

	f.IsMap, f.Label = true, LabelRepeated
	//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero.
	f.MapKey = &Field{Name: mapKeyName, Number: 1, TypeName: keyType, Kind: scalarKinds[keyType], Oneof: -1}
	//nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero.
	f.MapValue = &Field{Name: mapValueName, Number: mapValueNumber, TypeName: valType, Oneof: -1}

	if k, ok := scalarKinds[valType]; ok {
		f.MapValue.Kind = k
	} else {
		f.MapValue.Kind = KindMessage
	}

	f.TypeName, f.Kind = "map<"+keyType+","+valType+">", KindMessage

	return p.fieldRest(m, f)
}

// jsonName is the protobuf default JSON name: lowerCamelCase of the field name.
func jsonName(s string) string {
	var b strings.Builder

	up := false

	for _, c := range s {
		if c == '_' {
			up = true

			continue
		}

		if up {
			b.WriteRune(unicode.ToUpper(c))

			up = false
		} else {
			b.WriteRune(c)
		}
	}

	return b.String()
}

// mapValueNumber is the field number of a map entry value.
const mapValueNumber = 2
