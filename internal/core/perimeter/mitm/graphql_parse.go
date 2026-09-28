package mitm

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// gqlValueKind identifies the kind of a GraphQL value node.
type gqlValueKind int

const (
	gqlVariable gqlValueKind = iota // Raw = var name without '$'
	gqlInt                          // Raw = literal text
	gqlFloat                        // Raw = literal text
	gqlString                       // Raw = DECODED string value
	gqlBoolean                      // Raw = "true"/"false"
	gqlNull
	gqlEnum   // Raw = enum name
	gqlList   // List
	gqlObject // Fields (order preserved; duplicate names = parse error)
)

// gqlValue is a GraphQL value node.
type gqlValue struct {
	Kind   gqlValueKind
	Raw    string
	List   []gqlValue
	Fields []gqlObjectField
}

// gqlObjectField is a field in an object literal value.
type gqlObjectField struct {
	Name  string
	Value gqlValue
}

// gqlArgument is a name=value pair in a field argument list.
type gqlArgument struct {
	Name  string
	Value gqlValue
}

// gqlVarDef is a variable definition in an operation.
type gqlVarDef struct {
	Name    string
	Type    string    // canonical, no spaces, e.g. "[PullRequestState!]" "String!"
	Default *gqlValue // must be const (no variables)
}

// gqlField is a resolved (inlined) field in a selection set.
type gqlField struct {
	Alias    string
	Name     string
	Args     []gqlArgument
	Children []*gqlField
}

// ResponseKey returns Alias if non-empty, else Name.
func (f *gqlField) ResponseKey() string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Name
}

// gqlOperation is the parsed, inlined, merged operation.
type gqlOperation struct {
	Kind    string // "query" | "mutation"
	Name    string
	VarDefs []gqlVarDef
	Root    []*gqlField
}

// ── limits ────────────────────────────────────────────────────────────────────

const (
	maxQueryBytes = 32768
	maxDepth      = 24
	maxFields     = 3000
	maxTokens     = 20000
)

// ── token kinds ──────────────────────────────────────────────────────────────

type tokKind int

const (
	tokEOF tokKind = iota
	tokName
	tokIntValue
	tokFloatValue
	tokStringValue
	tokPunct // single char punctuator
)

type token struct {
	kind tokKind
	val  string // for tokPunct: the char; for tokStringValue: decoded; for others: raw text
}

// ── lexer ─────────────────────────────────────────────────────────────────────

type lexer struct {
	src  string
	pos  int
	toks int // token count
}

func newLexer(src string) *lexer { return &lexer{src: src} }

// isIgnored returns true for whitespace, BOM, commas, line terminators.
func isIgnored(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == ',' || b == 0xFE || b == 0xFF
}

func isNameStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
func isNameCont(b byte) bool {
	return isNameStart(b) || (b >= '0' && b <= '9')
}
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// next returns the next token, advancing the lexer.
func (l *lexer) next() (token, error) {
top:
	// skip ignored
	for l.pos < len(l.src) && isIgnored(l.src[l.pos]) {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return token{kind: tokEOF}, nil
	}
	b := l.src[l.pos]

	// comment
	if b == '#' {
		for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
			l.pos++
		}
		goto top
	}

	l.toks++
	if l.toks > maxTokens {
		return token{}, fmt.Errorf("graphql: token limit exceeded (%d)", maxTokens)
	}

	// block string — forbidden
	if b == '"' && l.pos+2 < len(l.src) && l.src[l.pos+1] == '"' && l.src[l.pos+2] == '"' {
		return token{}, fmt.Errorf("graphql: block strings are not allowed")
	}

	// regular string
	if b == '"' {
		s, err := l.readString()
		if err != nil {
			return token{}, err
		}
		return token{kind: tokStringValue, val: s}, nil
	}

	// punctuators
	switch b {
	case '!', '$', '&', '(', ')', ':', '=', '@', '[', ']', '{', '|', '}':
		l.pos++
		return token{kind: tokPunct, val: string(b)}, nil
	case '.':
		if l.pos+2 < len(l.src) && l.src[l.pos+1] == '.' && l.src[l.pos+2] == '.' {
			l.pos += 3
			return token{kind: tokPunct, val: "..."}, nil
		}
		return token{}, fmt.Errorf("graphql: unexpected character '.'")
	}

	// int / float
	if b == '-' || isDigit(b) {
		return l.readNumber()
	}

	// name
	if isNameStart(b) {
		start := l.pos
		for l.pos < len(l.src) && isNameCont(l.src[l.pos]) {
			l.pos++
		}
		return token{kind: tokName, val: l.src[start:l.pos]}, nil
	}

	// reject non-ASCII / control bytes
	r := rune(b)
	if b >= 0x80 {
		// try to decode a utf-8 rune for a nicer message
		var rr rune
		for i, c := range l.src[l.pos:] {
			if i == 0 {
				rr = c
			}
			break
		}
		r = rr
	}
	if r < 0x20 || r == 0x7F {
		return token{}, fmt.Errorf("graphql: unexpected control character U+%04X", r)
	}
	return token{}, fmt.Errorf("graphql: unexpected character %q", r)
}

func (l *lexer) readString() (string, error) {
	l.pos++ // skip opening "
	var buf strings.Builder
	for {
		if l.pos >= len(l.src) {
			return "", fmt.Errorf("graphql: unterminated string")
		}
		b := l.src[l.pos]
		if b == '"' {
			l.pos++
			return buf.String(), nil
		}
		if b == '\n' || b == '\r' {
			return "", fmt.Errorf("graphql: unterminated string (line terminator)")
		}
		// control chars not allowed raw
		if b < 0x20 {
			return "", fmt.Errorf("graphql: invalid control character U+%04X in string", b)
		}
		if b == '\\' {
			l.pos++
			if l.pos >= len(l.src) {
				return "", fmt.Errorf("graphql: unterminated escape")
			}
			esc := l.src[l.pos]
			l.pos++
			switch esc {
			case '"':
				buf.WriteByte('"')
			case '\\':
				buf.WriteByte('\\')
			case '/':
				buf.WriteByte('/')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case 'u':
				if l.pos+4 > len(l.src) {
					return "", fmt.Errorf("graphql: incomplete \\uXXXX escape")
				}
				hex := l.src[l.pos : l.pos+4]
				l.pos += 4
				r, err := gqlParseHex4(hex)
				if err != nil {
					return "", fmt.Errorf("graphql: invalid \\uXXXX escape: %s", hex)
				}
				// handle surrogate pairs
				if r >= 0xD800 && r <= 0xDBFF {
					// expect \uXXXX for low surrogate
					if l.pos+6 <= len(l.src) && l.src[l.pos] == '\\' && l.src[l.pos+1] == 'u' {
						hex2 := l.src[l.pos+2 : l.pos+6]
						r2, err2 := gqlParseHex4(hex2)
						if err2 == nil && r2 >= 0xDC00 && r2 <= 0xDFFF {
							full := utf16.DecodeRune(r, r2)
							buf.WriteRune(full)
							l.pos += 6
							continue
						}
					}
					return "", fmt.Errorf("graphql: lone high surrogate in string")
				}
				buf.WriteRune(r)
			default:
				return "", fmt.Errorf("graphql: invalid escape \\%c", esc)
			}
			continue
		}
		// multi-byte UTF-8 — copy raw; GraphQL allows Unicode in strings
		if b >= 0x80 {
			start := l.pos
			l.pos++
			for l.pos < len(l.src) && (l.src[l.pos]&0xC0) == 0x80 {
				l.pos++
			}
			buf.WriteString(l.src[start:l.pos])
		} else {
			buf.WriteByte(b)
			l.pos++
		}
	}
}

func gqlParseHex4(s string) (rune, error) {
	var r rune
	for _, c := range s {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return 0, fmt.Errorf("invalid hex")
		}
	}
	return r, nil
}

func (l *lexer) readNumber() (token, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
	}
	if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
		return token{}, fmt.Errorf("graphql: invalid number at %d", start)
	}
	if l.src[l.pos] == '0' {
		l.pos++
		if l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			return token{}, fmt.Errorf("graphql: leading zero in number")
		}
	} else {
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	isFloat := false
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		isFloat = true
		l.pos++
		if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
			return token{}, fmt.Errorf("graphql: expected digit after decimal point")
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		isFloat = true
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if l.pos >= len(l.src) || !isDigit(l.src[l.pos]) {
			return token{}, fmt.Errorf("graphql: expected digit in exponent")
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	raw := l.src[start:l.pos]
	if isFloat {
		return token{kind: tokFloatValue, val: raw}, nil
	}
	return token{kind: tokIntValue, val: raw}, nil
}

// ── parser ────────────────────────────────────────────────────────────────────

type parser struct {
	lex     *lexer
	current token
	peeked  bool
	peek1   token
}

func newParser(src string) (*parser, error) {
	p := &parser{lex: newLexer(src)}
	return p, nil
}

func (p *parser) next() (token, error) {
	if p.peeked {
		p.peeked = false
		return p.peek1, nil
	}
	return p.lex.next()
}

func (p *parser) peek() (token, error) {
	if p.peeked {
		return p.peek1, nil
	}
	t, err := p.lex.next()
	if err != nil {
		return token{}, err
	}
	p.peeked = true
	p.peek1 = t
	return t, nil
}

func (p *parser) expect(kind tokKind, val string) error {
	t, err := p.next()
	if err != nil {
		return err
	}
	if t.kind != kind || t.val != val {
		return fmt.Errorf("graphql: expected %q got %q", val, t.val)
	}
	return nil
}

func (p *parser) expectName() (string, error) {
	t, err := p.next()
	if err != nil {
		return "", err
	}
	if t.kind != tokName {
		return "", fmt.Errorf("graphql: expected name got %q (kind=%d)", t.val, t.kind)
	}
	return t.val, nil
}

// ── parse type string ─────────────────────────────────────────────────────────

// parseType reads a type reference and returns its canonical form (no spaces).
// e.g. String! [PullRequestState!] [PullRequestState!]!
func (p *parser) parseTypeStr() (string, error) {
	t, err := p.peek()
	if err != nil {
		return "", err
	}
	if t.kind == tokPunct && t.val == "[" {
		p.next() //nolint:errcheck
		inner, err := p.parseTypeStr()
		if err != nil {
			return "", err
		}
		if err := p.expect(tokPunct, "]"); err != nil {
			return "", err
		}
		s := "[" + inner + "]"
		n, err := p.peek()
		if err != nil {
			return "", err
		}
		if n.kind == tokPunct && n.val == "!" {
			p.next() //nolint:errcheck
			s += "!"
		}
		return s, nil
	}
	name, err := p.expectName()
	if err != nil {
		return "", err
	}
	n, err := p.peek()
	if err != nil {
		return "", err
	}
	if n.kind == tokPunct && n.val == "!" {
		p.next() //nolint:errcheck
		return name + "!", nil
	}
	return name, nil
}

// ── parse value ───────────────────────────────────────────────────────────────

func (p *parser) parseValue(constOnly bool) (gqlValue, error) {
	t, err := p.next()
	if err != nil {
		return gqlValue{}, err
	}
	switch t.kind {
	case tokPunct:
		switch t.val {
		case "$":
			if constOnly {
				return gqlValue{}, fmt.Errorf("graphql: variable not allowed in const value")
			}
			name, err := p.expectName()
			if err != nil {
				return gqlValue{}, err
			}
			return gqlValue{Kind: gqlVariable, Raw: name}, nil
		case "[":
			var items []gqlValue
			for {
				pk, err := p.peek()
				if err != nil {
					return gqlValue{}, err
				}
				if pk.kind == tokPunct && pk.val == "]" {
					p.next() //nolint:errcheck
					break
				}
				v, err := p.parseValue(constOnly)
				if err != nil {
					return gqlValue{}, err
				}
				items = append(items, v)
			}
			if items == nil {
				items = []gqlValue{}
			}
			return gqlValue{Kind: gqlList, List: items}, nil
		case "{":
			var fields []gqlObjectField
			seen := map[string]bool{}
			for {
				pk, err := p.peek()
				if err != nil {
					return gqlValue{}, err
				}
				if pk.kind == tokPunct && pk.val == "}" {
					p.next() //nolint:errcheck
					break
				}
				fname, err := p.expectName()
				if err != nil {
					return gqlValue{}, err
				}
				if seen[fname] {
					return gqlValue{}, fmt.Errorf("graphql: duplicate object field %q", fname)
				}
				seen[fname] = true
				if err := p.expect(tokPunct, ":"); err != nil {
					return gqlValue{}, err
				}
				fval, err := p.parseValue(constOnly)
				if err != nil {
					return gqlValue{}, err
				}
				fields = append(fields, gqlObjectField{Name: fname, Value: fval})
			}
			return gqlValue{Kind: gqlObject, Fields: fields}, nil
		default:
			return gqlValue{}, fmt.Errorf("graphql: unexpected punctuator %q in value", t.val)
		}
	case tokIntValue:
		return gqlValue{Kind: gqlInt, Raw: t.val}, nil
	case tokFloatValue:
		return gqlValue{Kind: gqlFloat, Raw: t.val}, nil
	case tokStringValue:
		return gqlValue{Kind: gqlString, Raw: t.val}, nil
	case tokName:
		switch t.val {
		case "true", "false":
			return gqlValue{Kind: gqlBoolean, Raw: t.val}, nil
		case "null":
			return gqlValue{Kind: gqlNull}, nil
		default:
			return gqlValue{Kind: gqlEnum, Raw: t.val}, nil
		}
	default:
		return gqlValue{}, fmt.Errorf("graphql: unexpected token %q in value", t.val)
	}
}

// ── parse arguments ───────────────────────────────────────────────────────────

func (p *parser) parseArguments(constOnly bool) ([]gqlArgument, error) {
	pk, err := p.peek()
	if err != nil {
		return nil, err
	}
	if !(pk.kind == tokPunct && pk.val == "(") {
		return nil, nil
	}
	p.next() //nolint:errcheck
	var args []gqlArgument
	seen := map[string]bool{}
	for {
		pk, err := p.peek()
		if err != nil {
			return nil, err
		}
		if pk.kind == tokPunct && pk.val == ")" {
			p.next() //nolint:errcheck
			break
		}
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("graphql: duplicate argument %q", name)
		}
		seen[name] = true
		if err := p.expect(tokPunct, ":"); err != nil {
			return nil, err
		}
		val, err := p.parseValue(constOnly)
		if err != nil {
			return nil, err
		}
		args = append(args, gqlArgument{Name: name, Value: val})
	}
	return args, nil
}

// ── raw (pre-inlining) AST ────────────────────────────────────────────────────

type rawField struct {
	Alias    string
	Name     string
	Args     []gqlArgument
	Children []rawSelection
}

type rawFragSpread struct {
	Name string
}

type rawInlineFragment struct {
	TypeCond string // may be ""
	Children []rawSelection
}

type rawSelection struct {
	Field  *rawField
	Spread *rawFragSpread
	Inline *rawInlineFragment
}

type rawFragDef struct {
	Name     string
	TypeCond string
	Children []rawSelection
}

type rawOpDef struct {
	Kind    string // "query" | "mutation"
	Name    string
	VarDefs []gqlVarDef
	Body    []rawSelection
}

// ── parse selection set ───────────────────────────────────────────────────────

func (p *parser) parseSelectionSet() ([]rawSelection, error) {
	if err := p.expect(tokPunct, "{"); err != nil {
		return nil, err
	}
	var sels []rawSelection
	for {
		pk, err := p.peek()
		if err != nil {
			return nil, err
		}
		if pk.kind == tokPunct && pk.val == "}" {
			p.next() //nolint:errcheck
			break
		}
		sel, err := p.parseSelection()
		if err != nil {
			return nil, err
		}
		sels = append(sels, sel)
	}
	return sels, nil
}

func (p *parser) parseSelection() (rawSelection, error) {
	pk, err := p.peek()
	if err != nil {
		return rawSelection{}, err
	}

	if pk.kind == tokPunct && pk.val == "..." {
		p.next() //nolint:errcheck
		pk2, err := p.peek()
		if err != nil {
			return rawSelection{}, err
		}
		// inline fragment: "... on Type {...}" or "... {...}"
		if pk2.kind == tokName && pk2.val == "on" {
			p.next() //nolint:errcheck
			typeName, err := p.expectName()
			if err != nil {
				return rawSelection{}, err
			}
			// check for directive
			if err := p.rejectDirective(); err != nil {
				return rawSelection{}, err
			}
			children, err := p.parseSelectionSet()
			if err != nil {
				return rawSelection{}, err
			}
			return rawSelection{Inline: &rawInlineFragment{TypeCond: typeName, Children: children}}, nil
		}
		if pk2.kind == tokPunct && pk2.val == "{" {
			// inline fragment without type condition
			if err := p.rejectDirective(); err != nil {
				return rawSelection{}, err
			}
			children, err := p.parseSelectionSet()
			if err != nil {
				return rawSelection{}, err
			}
			return rawSelection{Inline: &rawInlineFragment{Children: children}}, nil
		}
		// named fragment spread
		if pk2.kind != tokName {
			return rawSelection{}, fmt.Errorf("graphql: expected fragment name after '...'")
		}
		name, err := p.expectName()
		if err != nil {
			return rawSelection{}, err
		}
		// check for directive
		if err := p.rejectDirective(); err != nil {
			return rawSelection{}, err
		}
		return rawSelection{Spread: &rawFragSpread{Name: name}}, nil
	}

	// field: [alias:] name [args] [selectionSet]
	nameOrAlias, err := p.expectName()
	if err != nil {
		return rawSelection{}, err
	}
	fieldName := nameOrAlias
	alias := ""
	pk2, err := p.peek()
	if err != nil {
		return rawSelection{}, err
	}
	if pk2.kind == tokPunct && pk2.val == ":" {
		p.next() //nolint:errcheck
		alias = nameOrAlias
		fieldName, err = p.expectName()
		if err != nil {
			return rawSelection{}, err
		}
	}

	args, err := p.parseArguments(false)
	if err != nil {
		return rawSelection{}, err
	}

	// reject directives
	if err := p.rejectDirective(); err != nil {
		return rawSelection{}, err
	}

	var children []rawSelection
	pk3, err := p.peek()
	if err != nil {
		return rawSelection{}, err
	}
	if pk3.kind == tokPunct && pk3.val == "{" {
		children, err = p.parseSelectionSet()
		if err != nil {
			return rawSelection{}, err
		}
	}
	return rawSelection{Field: &rawField{Alias: alias, Name: fieldName, Args: args, Children: children}}, nil
}

func (p *parser) rejectDirective() error {
	pk, err := p.peek()
	if err != nil {
		return err
	}
	if pk.kind == tokPunct && pk.val == "@" {
		return fmt.Errorf("graphql: directives are not allowed")
	}
	return nil
}

// ── parse variable definitions ────────────────────────────────────────────────

func (p *parser) parseVarDefs() ([]gqlVarDef, error) {
	pk, err := p.peek()
	if err != nil {
		return nil, err
	}
	if !(pk.kind == tokPunct && pk.val == "(") {
		return nil, nil
	}
	p.next() //nolint:errcheck
	var defs []gqlVarDef
	seen := map[string]bool{}
	for {
		pk, err := p.peek()
		if err != nil {
			return nil, err
		}
		if pk.kind == tokPunct && pk.val == ")" {
			p.next() //nolint:errcheck
			break
		}
		if err := p.expect(tokPunct, "$"); err != nil {
			return nil, err
		}
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("graphql: duplicate variable $%s", name)
		}
		seen[name] = true
		if err := p.expect(tokPunct, ":"); err != nil {
			return nil, err
		}
		typeStr, err := p.parseTypeStr()
		if err != nil {
			return nil, err
		}
		var def *gqlValue
		pk2, err := p.peek()
		if err != nil {
			return nil, err
		}
		if pk2.kind == tokPunct && pk2.val == "=" {
			p.next()                      //nolint:errcheck
			dv, err := p.parseValue(true) // const only
			if err != nil {
				return nil, err
			}
			def = &dv
		}
		// reject directives on var defs
		if err := p.rejectDirective(); err != nil {
			return nil, err
		}
		defs = append(defs, gqlVarDef{Name: name, Type: typeStr, Default: def})
	}
	return defs, nil
}

// ── parse operation / fragment ────────────────────────────────────────────────

// parseDocument parses one operation and zero or more fragments.
// Returns the operation and a map of fragments.
func parseDocument(src string) (rawOpDef, map[string]rawFragDef, error) {
	if len(src) > maxQueryBytes {
		return rawOpDef{}, nil, fmt.Errorf("graphql: query exceeds %d bytes", maxQueryBytes)
	}

	p, err := newParser(src)
	if err != nil {
		return rawOpDef{}, nil, err
	}

	var ops []rawOpDef
	frags := map[string]rawFragDef{}
	fragNames := map[string]bool{}

	for {
		pk, err := p.peek()
		if err != nil {
			return rawOpDef{}, nil, err
		}
		if pk.kind == tokEOF {
			break
		}
		if pk.kind == tokPunct && pk.val == "{" {
			// shorthand query
			body, err := p.parseSelectionSet()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			ops = append(ops, rawOpDef{Kind: "query", Body: body})
			continue
		}
		if pk.kind != tokName {
			return rawOpDef{}, nil, fmt.Errorf("graphql: expected operation or fragment definition, got %q", pk.val)
		}
		kw, err := p.expectName()
		if err != nil {
			return rawOpDef{}, nil, err
		}
		switch kw {
		case "query", "mutation":
			// optional name
			name := ""
			pk2, err := p.peek()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			if pk2.kind == tokName {
				name, err = p.expectName()
				if err != nil {
					return rawOpDef{}, nil, err
				}
			}
			varDefs, err := p.parseVarDefs()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			// reject directives on operation
			if err := p.rejectDirective(); err != nil {
				return rawOpDef{}, nil, err
			}
			body, err := p.parseSelectionSet()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			ops = append(ops, rawOpDef{Kind: kw, Name: name, VarDefs: varDefs, Body: body})
		case "subscription":
			return rawOpDef{}, nil, fmt.Errorf("graphql: subscriptions are not allowed")
		case "fragment":
			fragName, err := p.expectName()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			if fragName == "on" {
				return rawOpDef{}, nil, fmt.Errorf("graphql: fragment may not be named 'on'")
			}
			if fragNames[fragName] {
				return rawOpDef{}, nil, fmt.Errorf("graphql: duplicate fragment %q", fragName)
			}
			fragNames[fragName] = true
			if err := p.expect(tokName, "on"); err != nil {
				return rawOpDef{}, nil, err
			}
			typeCond, err := p.expectName()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			// reject directives on fragment def
			if err := p.rejectDirective(); err != nil {
				return rawOpDef{}, nil, err
			}
			children, err := p.parseSelectionSet()
			if err != nil {
				return rawOpDef{}, nil, err
			}
			frags[fragName] = rawFragDef{Name: fragName, TypeCond: typeCond, Children: children}
		default:
			// type-system keywords: type, interface, union, enum, input, scalar, schema, extend, directive, implements
			typeSystemKws := map[string]bool{
				"type": true, "interface": true, "union": true, "enum": true,
				"input": true, "scalar": true, "schema": true, "extend": true,
				"directive": true, "implements": true,
			}
			if typeSystemKws[kw] {
				return rawOpDef{}, nil, fmt.Errorf("graphql: type-system definitions are not allowed (%q)", kw)
			}
			return rawOpDef{}, nil, fmt.Errorf("graphql: unexpected keyword %q", kw)
		}
	}

	if len(ops) == 0 {
		return rawOpDef{}, nil, fmt.Errorf("graphql: document contains no operation")
	}
	if len(ops) > 1 {
		return rawOpDef{}, nil, fmt.Errorf("graphql: document contains %d operations (only 1 allowed)", len(ops))
	}
	return ops[0], frags, nil
}

// ── fragment validation (undefined, unused, cycles) ───────────────────────────

// collectSpreads returns all fragment names spread within a selection list.
func collectSpreads(sels []rawSelection) []string {
	var out []string
	for _, s := range sels {
		if s.Spread != nil {
			out = append(out, s.Spread.Name)
		}
		if s.Field != nil {
			out = append(out, collectSpreads(s.Field.Children)...)
		}
		if s.Inline != nil {
			out = append(out, collectSpreads(s.Inline.Children)...)
		}
	}
	return out
}

func collectAllSpreads(frags map[string]rawFragDef, opBody []rawSelection) (opSpreads []string, fragSpreads map[string][]string) {
	opSpreads = collectSpreads(opBody)
	fragSpreads = map[string][]string{}
	for name, f := range frags {
		fragSpreads[name] = collectSpreads(f.Children)
	}
	return opSpreads, fragSpreads
}

// reachable returns the set of fragment names reachable from startSpreads.
func reachable(startSpreads []string, fragSpreads map[string][]string) (map[string]bool, error) {
	visited := map[string]bool{}
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		for _, p := range path {
			if p == name {
				return fmt.Errorf("graphql: fragment cycle detected: %s", strings.Join(append(path, name), " -> "))
			}
		}
		if visited[name] {
			return nil
		}
		visited[name] = true
		for _, child := range fragSpreads[name] {
			if err := visit(child, append(path, name)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range startSpreads {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return visited, nil
}

func validateFragments(opBody []rawSelection, frags map[string]rawFragDef) error {
	opSpreads, fragSpreads := collectAllSpreads(frags, opBody)

	// build full reachable set
	allSpreads := make([]string, len(opSpreads))
	copy(allSpreads, opSpreads)
	for _, ss := range fragSpreads {
		allSpreads = append(allSpreads, ss...)
	}

	used, err := reachable(opSpreads, fragSpreads)
	if err != nil {
		return err
	}

	// undefined: a spread that isn't in frags
	for name := range used {
		if _, ok := frags[name]; !ok {
			return fmt.Errorf("graphql: undefined fragment %q", name)
		}
	}

	// unused: a fragment in frags that isn't used
	for name := range frags {
		if !used[name] {
			return fmt.Errorf("graphql: unused fragment %q", name)
		}
	}
	return nil
}

// ── variable validation ───────────────────────────────────────────────────────

func collectVarUses(sels []rawSelection) []string {
	var out []string
	for _, s := range sels {
		if s.Field != nil {
			for _, a := range s.Field.Args {
				out = append(out, varUsesInValue(a.Value)...)
			}
			out = append(out, collectVarUses(s.Field.Children)...)
		}
		if s.Inline != nil {
			out = append(out, collectVarUses(s.Inline.Children)...)
		}
		// spreads: we'll resolve them when we inline
	}
	return out
}

func collectVarUsesInFrags(frags map[string]rawFragDef) []string {
	var out []string
	for _, f := range frags {
		out = append(out, collectVarUses(f.Children)...)
	}
	return out
}

func varUsesInValue(v gqlValue) []string {
	switch v.Kind {
	case gqlVariable:
		return []string{v.Raw}
	case gqlList:
		var out []string
		for _, item := range v.List {
			out = append(out, varUsesInValue(item)...)
		}
		return out
	case gqlObject:
		var out []string
		for _, f := range v.Fields {
			out = append(out, varUsesInValue(f.Value)...)
		}
		return out
	}
	return nil
}

func validateVars(op rawOpDef, frags map[string]rawFragDef) error {
	declared := map[string]bool{}
	for _, d := range op.VarDefs {
		declared[d.Name] = true
	}
	used := collectVarUses(op.Body)
	used = append(used, collectVarUsesInFrags(frags)...)
	for _, name := range used {
		if !declared[name] {
			return fmt.Errorf("graphql: undeclared variable $%s", name)
		}
	}
	return nil
}

// ── inlining + merging ────────────────────────────────────────────────────────

type inliner struct {
	frags       map[string]rawFragDef
	fragInline  map[string][]*gqlField
	totalFields int
	depth       int
}

func (in *inliner) inlineSels(sels []rawSelection) ([]*gqlField, error) {
	if in.depth > maxDepth {
		return nil, fmt.Errorf("graphql: selection nesting depth exceeds %d", maxDepth)
	}
	var fields []*gqlField
	for _, s := range sels {
		switch {
		case s.Field != nil:
			rf := s.Field
			in.depth++
			var children []*gqlField
			if len(rf.Children) > 0 {
				var err error
				children, err = in.inlineSels(rf.Children)
				if err != nil {
					in.depth--
					return nil, err
				}
			}
			in.depth--
			in.totalFields++
			if in.totalFields > maxFields {
				return nil, fmt.Errorf("graphql: total field limit exceeded (%d)", maxFields)
			}
			fields = append(fields, &gqlField{
				Alias:    rf.Alias,
				Name:     rf.Name,
				Args:     rf.Args,
				Children: children,
			})
		case s.Spread != nil:
			frag, ok := in.frags[s.Spread.Name]
			if !ok {
				return nil, fmt.Errorf("graphql: undefined fragment %q", s.Spread.Name)
			}
			inlined, err := in.inlineSels(frag.Children)
			if err != nil {
				return nil, err
			}
			fields = append(fields, inlined...)
		case s.Inline != nil:
			inlined, err := in.inlineSels(s.Inline.Children)
			if err != nil {
				return nil, err
			}
			fields = append(fields, inlined...)
		}
	}
	return fields, nil
}

// argsEqual does deep equality of two argument slices (order-insensitive).
func argsEqual(a, b []gqlArgument) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]gqlValue, len(a))
	for _, arg := range a {
		aMap[arg.Name] = arg.Value
	}
	for _, arg := range b {
		av, ok := aMap[arg.Name]
		if !ok {
			return false
		}
		if !valueEqual(av, arg.Value) {
			return false
		}
	}
	return true
}

func valueEqual(a, b gqlValue) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case gqlVariable, gqlInt, gqlFloat, gqlString, gqlBoolean, gqlEnum:
		return a.Raw == b.Raw
	case gqlNull:
		return true
	case gqlList:
		if len(a.List) != len(b.List) {
			return false
		}
		for i := range a.List {
			if !valueEqual(a.List[i], b.List[i]) {
				return false
			}
		}
		return true
	case gqlObject:
		if len(a.Fields) != len(b.Fields) {
			return false
		}
		bMap := make(map[string]gqlValue, len(b.Fields))
		for _, f := range b.Fields {
			bMap[f.Name] = f.Value
		}
		for _, f := range a.Fields {
			bv, ok := bMap[f.Name]
			if !ok {
				return false
			}
			if !valueEqual(f.Value, bv) {
				return false
			}
		}
		return true
	}
	return false
}

// mergeFields merges a list of gqlField (already inlined) by ResponseKey.
// Fields with the same response key must have identical Name and Args.
// Their Children are merged recursively.
func mergeFields(fields []*gqlField) ([]*gqlField, error) {
	type entry struct {
		field *gqlField
		idx   int
	}
	order := []string{}
	byKey := map[string]*entry{}
	for _, f := range fields {
		key := f.ResponseKey()
		if e, ok := byKey[key]; ok {
			if e.field.Name != f.Name {
				return nil, fmt.Errorf("graphql: conflicting fields for response key %q: name %q vs %q", key, e.field.Name, f.Name)
			}
			if !argsEqual(e.field.Args, f.Args) {
				return nil, fmt.Errorf("graphql: conflicting fields for response key %q: argument mismatch", key)
			}
			merged, err := mergeFields(append(e.field.Children, f.Children...))
			if err != nil {
				return nil, err
			}
			e.field.Children = merged
		} else {
			e := &entry{field: f}
			byKey[key] = e
			order = append(order, key)
		}
	}
	out := make([]*gqlField, 0, len(order))
	for _, key := range order {
		f := byKey[key].field
		if len(f.Children) > 0 {
			childMerged, err := mergeFields(f.Children)
			if err != nil {
				return nil, err
			}
			f.Children = childMerged
		}
		out = append(out, f)
	}
	return out, nil
}

// parseGraphQLOperation parses a GraphQL document string and returns the
// single operation with all fragments inlined and fields merged.
func parseGraphQLOperation(query string) (*gqlOperation, error) {
	op, frags, err := parseDocument(query)
	if err != nil {
		return nil, err
	}
	if err := validateFragments(op.Body, frags); err != nil {
		return nil, err
	}
	if err := validateVars(op, frags); err != nil {
		return nil, err
	}
	in := &inliner{frags: frags}
	root, err := in.inlineSels(op.Body)
	if err != nil {
		return nil, err
	}
	merged, err := mergeFields(root)
	if err != nil {
		return nil, err
	}
	return &gqlOperation{
		Kind:    op.Kind,
		Name:    op.Name,
		VarDefs: op.VarDefs,
		Root:    merged,
	}, nil
}
