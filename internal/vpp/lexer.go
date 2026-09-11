package vpp

import "strings"

const maxNestedDirectives = 16

type lexer struct {
	src       []byte
	pos       int
	line      int
	lineStart int
	depth     int
	inBody    bool
	out       []Directive
	comments  []CommentSpan
}

// ScanSource разбирает исходник и возвращает найденные директивы
// препроцессора и диапазоны комментариев.
func ScanSource(path string, src []byte) FileReport {
	l := &lexer{src: src, line: 1}
	l.out = make([]Directive, 0, 1+len(src)/256)
	l.run()
	return FileReport{Path: path, Directives: l.out, Comments: l.comments}
}

func (l *lexer) run() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.newline()
		case c == '/' && l.peek(1) == '/':
			l.lineComment()
		case c == '/' && l.peek(1) == '*':
			l.blockComment()
		case c == '"':
			l.stringLit()
		case c == '\\':
			l.escapedIdent()
		case c == '`':
			l.directive(CommentNone, -1)
		default:
			l.pos++
		}
	}
}

func (l *lexer) peek(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *lexer) newline() {
	l.pos++
	l.line++
	l.lineStart = l.pos
}

func (l *lexer) openComment(kind CommentKind) int {
	l.comments = append(l.comments, CommentSpan{
		Kind:      kind,
		Start:     l.pos,
		StartLine: l.line,
		StartCol:  l.pos - l.lineStart + 1,
	})
	return len(l.comments) - 1
}

func (l *lexer) closeComment(idx int) {
	l.comments[idx].End = l.pos
	l.comments[idx].EndLine = l.line
}

func (l *lexer) lineComment() {
	idx := l.openComment(CommentLine)
	l.pos += 2
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\n':
			l.closeComment(idx)
			return
		case '`':
			l.directive(CommentLine, idx)
		default:
			l.pos++
		}
	}
	l.closeComment(idx)
}

func (l *lexer) blockComment() {
	idx := l.openComment(CommentBlock)
	l.pos += 2
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '*' && l.peek(1) == '/':
			l.pos += 2
			l.closeComment(idx)
			return
		case c == '\n':
			l.newline()
		case c == '`':
			l.directive(CommentBlock, idx)
		default:
			l.pos++
		}
	}
	l.closeComment(idx)
}

func (l *lexer) stringLit() {
	l.pos++
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			if l.peek(1) == '\n' {
				l.pos++
				l.newline()
				continue
			}
			l.pos += 2
		case '"':
			l.pos++
			return
		case '\n':
			l.newline()
			return
		default:
			l.pos++
		}
	}
}

func (l *lexer) escapedIdent() {
	l.pos++
	for l.pos < len(l.src) && !isSpace(l.src[l.pos]) {
		l.pos++
	}
}

// directive разбирает одну директиву препроцессора, начиная с позиции `.
// ck/commentIdx учитывают, что директива лежит внутри комментария.
func (l *lexer) directive(ck CommentKind, commentIdx int) {
	d := Directive{
		Offset:       l.pos,
		Line:         l.line,
		Column:       l.pos - l.lineStart + 1,
		Commented:    ck != CommentNone,
		CommentKind:  ck,
		CommentIndex: commentIdx,
		InMacroBody:  l.inBody,
	}
	l.pos++

	name := l.readIdent()
	if name == "" {
		return
	}

	kind, known := directiveKinds[name]
	if !known {
		kind = KindMacroUsage
	}
	d.Kind = kind
	d.Directive = name

	switch kind {
	case KindMacroUsage:
		d.Name = name
	case KindDefine:
		l.skipInlineSpace()
		d.Name = l.readIdent()
		d.Params = l.readParams()
		idx := len(l.out)
		l.emit(d)
		body := l.readBody(ck, true)
		l.out[idx].Body = body
		l.out[idx].End = l.pos
		l.out[idx].EndLine = l.line
		return
	case KindUndef, KindIfdef, KindIfndef, KindElsif:
		l.skipInlineSpace()
		d.Name = l.readIdent()
	case KindInclude:
		l.skipInlineSpace()
		d.Name = l.readIncludeTarget(ck)
	case KindTimescale, KindPragma, KindLine, KindKeywords, KindOther:
		l.skipInlineSpace()
		d.Body = l.readBody(ck, false)
	}

	d.End = l.pos
	d.EndLine = l.line
	l.emit(d)
}

func (l *lexer) emit(d Directive) {
	l.out = append(l.out, d)
}

func (l *lexer) readIdent() string {
	if l.pos >= len(l.src) || !isIdentStart(l.src[l.pos]) {
		return ""
	}
	start := l.pos
	l.pos++
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.pos++
	}
	return string(l.src[start:l.pos])
}

func (l *lexer) readParams() []string {
	if l.pos >= len(l.src) || l.src[l.pos] != '(' {
		return nil
	}
	l.pos++
	var params []string
	var sb strings.Builder
	nesting := 0
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.newline()
			return appendParam(params, sb.String())
		case c == '(' || c == '[' || c == '{':
			nesting++
		case c == ')' && nesting == 0:
			l.pos++
			return appendParam(params, sb.String())
		case c == ')' || c == ']' || c == '}':
			nesting--
		case c == ',' && nesting == 0:
			params = appendParam(params, sb.String())
			sb.Reset()
			l.pos++
			continue
		}
		sb.WriteByte(c)
		l.pos++
	}
	return appendParam(params, sb.String())
}

func appendParam(params []string, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return params
	}
	return append(params, raw)
}

func (l *lexer) readIncludeTarget(ck CommentKind) string {
	if l.pos >= len(l.src) {
		return ""
	}
	switch l.src[l.pos] {
	case '"':
		return l.readDelimited('"', '"')
	case '<':
		return l.readDelimited('<', '>')
	case '`':
		l.directive(ck, -1)
		return ""
	}
	return ""
}

func (l *lexer) readDelimited(open, close byte) string {
	if l.src[l.pos] != open {
		return ""
	}
	l.pos++
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == close {
			target := string(l.src[start:l.pos])
			l.pos++
			return target
		}
		if c == '\n' {
			return string(l.src[start:l.pos])
		}
		l.pos++
	}
	return string(l.src[start:l.pos])
}

func (l *lexer) readBody(ck CommentKind, macroBody bool) string {
	prevBody := l.inBody
	l.inBody = l.inBody || macroBody
	defer func() { l.inBody = prevBody }()

	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && l.isLineContinuation():
			if ck != CommentNone {
				return strings.TrimSpace(sb.String())
			}
			l.pos++
			if l.src[l.pos] == '\r' {
				l.pos++
			}
			l.newline()
			sb.WriteByte('\n')
			continue
		case c == '\n':
			return strings.TrimSpace(sb.String())
		case c == '/' && l.peek(1) == '/':
			return strings.TrimSpace(sb.String())
		case c == '/' && l.peek(1) == '*' && ck == CommentNone:
			return strings.TrimSpace(sb.String())
		case c == '*' && l.peek(1) == '/' && ck == CommentBlock:
			return strings.TrimSpace(sb.String())
		case c == '`' && l.depth < maxNestedDirectives:
			start := l.pos
			l.depth++
			l.directive(ck, -1)
			l.depth--
			sb.Write(l.src[start:l.pos])
			continue
		}
		sb.WriteByte(c)
		l.pos++
	}
	return strings.TrimSpace(sb.String())
}

func (l *lexer) isLineContinuation() bool {
	if l.peek(1) == '\n' {
		return true
	}
	return l.peek(1) == '\r' && l.peek(2) == '\n'
}

func (l *lexer) skipInlineSpace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\r' {
			l.pos++
			continue
		}
		return
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$'
}
