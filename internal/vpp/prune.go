package vpp

import (
	"bytes"
	"sort"
	"strings"
)

type EditReason uint8

const (
	ReasonCommentedDefine EditReason = iota
	ReasonCommentedBlock
	ReasonDeadBranch
	ReasonBranchPromoted
	ReasonRemovedMacro
)

func (r EditReason) String() string {
	switch r {
	case ReasonCommentedBlock:
		return "commented-block"
	case ReasonDeadBranch:
		return "dead-branch"
	case ReasonBranchPromoted:
		return "branch-promoted"
	case ReasonRemovedMacro:
		return "removed-macro"
	default:
		return "commented-define"
	}
}

type Edit struct {
	Start       int        `json:"start"`
	End         int        `json:"end"`
	Line        int        `json:"line"`
	Lines       int        `json:"lines"`
	Reason      EditReason `json:"reason"`
	Detail      string     `json:"detail"`
	Replacement string     `json:"replacement,omitempty"`
}

func PlanCommentedDirectives(report FileReport, src []byte) []Edit {
	return planCommentedDirectives(report, src, nil)
}

// planCommentedDirectives планирует удаление закомментированных директив
// (define/undef и целых ifdef..endif блоков), кроме защищённых.
func planCommentedDirectives(report FileReport, src []byte, protected map[string]bool) []Edit {
	commented := make([]int, 0, len(report.Directives))
	for i, d := range report.Directives {
		if d.Commented && d.CommentIndex >= 0 {
			commented = append(commented, i)
		}
	}
	if len(commented) == 0 {
		return nil
	}

	consumed := make(map[int]bool, len(commented))
	edits := make([]Edit, 0, len(commented))

	var stack []int
	for _, i := range commented {
		d := report.Directives[i]
		if d.Kind.IsConditional() && !startsCommentLine(src, d) {
			// слово `endif/`ifdef в середине текста комментария — проза,
			// а не закомментированная директива
			continue
		}
		switch d.Kind {
		case KindIfdef, KindIfndef:
			stack = append(stack, i)
		case KindEndif:
			if len(stack) == 0 {
				if d.CommentKind == CommentLine {
					edits = append(edits, loneConditionalEdit(report, src, i))
				}
				continue
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				continue
			}
			if !commentsOnlyBetween(report, src, open, i) {
				// между закомментированными ifdef и endif есть активный код —
				// это не один закомментированный блок, целиком удалять нельзя
				if report.Directives[open].CommentKind == CommentLine {
					edits = append(edits, loneConditionalEdit(report, src, open))
				}
				if d.CommentKind == CommentLine {
					edits = append(edits, loneConditionalEdit(report, src, i))
				}
				continue
			}
			if blockHasProtected(report, open, i, protected) {
				continue
			}
			edits = append(edits, blockEdit(report, src, open, i, consumed))
		case KindElsif, KindElse:
			if len(stack) == 0 && d.CommentKind == CommentLine {
				edits = append(edits, loneConditionalEdit(report, src, i))
			}
		}
	}
	for _, i := range stack {
		// одиночные строки-директивы удаляем только у построчных комментариев:
		// в блочном комментарии `endif — чаще всего проза документации
		if report.Directives[i].CommentKind == CommentLine {
			edits = append(edits, loneConditionalEdit(report, src, i))
		}
	}

	for _, i := range commented {
		if consumed[i] {
			continue
		}
		d := report.Directives[i]
		if d.Kind != KindDefine && d.Kind != KindUndef && d.Kind != KindUndefineAll {
			continue
		}
		if protected[d.Name] {
			continue
		}
		span := report.Comments[d.CommentIndex]
		end, endLine := extendContinuedComment(report, src, d.CommentIndex)
		edits = append(edits, Edit{
			Start:  span.Start,
			End:    end,
			Line:   span.StartLine,
			Lines:  endLine - span.StartLine + 1,
			Reason: ReasonCommentedDefine,
			Detail: "`" + d.Directive + " " + d.Name,
		})
	}

	return normalizeEdits(src, edits)
}

// startsCommentLine — true, если директива является первым непробельным
// содержимым своей строки: тогда это закомментированная директива, а не
// упоминание `endif в середине прозы комментария.
func startsCommentLine(src []byte, d Directive) bool {
	lineStart := d.Offset
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	for i := lineStart; i < d.Offset; i++ {
		switch src[i] {
		case ' ', '\t', '\r', '/', '*':
		default:
			return false
		}
	}
	return true
}

// commentsOnlyBetween — true, если между закомментированными директивами open
// и close нет активного кода: только комментарии и пустые строки.
func commentsOnlyBetween(report FileReport, src []byte, open, close int) bool {
	oi := report.Directives[open].CommentIndex
	ci := report.Directives[close].CommentIndex
	if oi == ci {
		return true // обе директивы внутри одного комментария
	}
	if oi > ci {
		oi, ci = ci, oi
	}
	pos := report.Comments[oi].End
	for k := oi + 1; k <= ci; k++ {
		span := report.Comments[k]
		if span.Start < pos {
			continue // пересекающийся комментарий, уже покрыт
		}
		if !isBlank(src[pos:span.Start]) {
			return false
		}
		pos = span.End
	}
	return true
}

// loneConditionalEdit удаляет строку комментария с одиночной (непарной)
// условной директивой — это осиротевший остаток закомментированного блока.
func loneConditionalEdit(report FileReport, src []byte, i int) Edit {
	d := report.Directives[i]
	span := report.Comments[d.CommentIndex]
	end, endLine := extendContinuedComment(report, src, d.CommentIndex)
	return Edit{
		Start:  span.Start,
		End:    end,
		Line:   span.StartLine,
		Lines:  endLine - span.StartLine + 1,
		Reason: ReasonCommentedBlock,
		Detail: "`" + d.Directive + " (unpaired)",
	}
}

func blockHasProtected(report FileReport, open, close int, protected map[string]bool) bool {
	if len(protected) == 0 {
		return false
	}
	openSpan := report.Comments[report.Directives[open].CommentIndex]
	closeSpan := report.Comments[report.Directives[close].CommentIndex]
	start, end := openSpan.Start, closeSpan.End
	if closeSpan.Start < openSpan.Start {
		start, end = closeSpan.Start, openSpan.End
	}
	for _, d := range report.Directives {
		if d.Offset >= start && d.End <= end && d.Kind == KindDefine && protected[d.Name] {
			return true
		}
	}
	return false
}

func extendContinuedComment(report FileReport, src []byte, commentIdx int) (int, int) {
	span := report.Comments[commentIdx]
	if span.Kind != CommentLine {
		return span.End, span.EndLine
	}

	for {
		text := strings.TrimRight(string(src[span.Start:span.End]), " \t\r")
		if !strings.HasSuffix(text, "\\") {
			return span.End, span.EndLine
		}
		if commentIdx+1 >= len(report.Comments) {
			return span.End, span.EndLine
		}
		next := report.Comments[commentIdx+1]
		if next.Kind != CommentLine || next.StartLine != span.EndLine+1 {
			return span.End, span.EndLine
		}
		if !isBlank(src[span.End:next.Start]) {
			return span.End, span.EndLine
		}
		commentIdx++
		span = next
	}
}

func blockEdit(report FileReport, src []byte, open, close int, consumed map[int]bool) Edit {
	openSpan := report.Comments[report.Directives[open].CommentIndex]
	closeSpan := report.Comments[report.Directives[close].CommentIndex]

	start, end := openSpan.Start, closeSpan.End
	if closeSpan.Start < openSpan.Start {
		start, end = closeSpan.Start, openSpan.End
	}

	names := make([]string, 0, 4)
	for i, d := range report.Directives {
		if d.Offset >= start && d.End <= end {
			consumed[i] = true
			if d.Kind == KindDefine {
				names = append(names, d.Name)
			}
		}
	}

	detail := "`" + report.Directives[open].Directive + " " + report.Directives[open].Name
	if len(names) > 0 {
		detail += " {" + strings.Join(names, ", ") + "}"
	}

	return Edit{
		Start:  start,
		End:    end,
		Line:   openSpan.StartLine,
		Lines:  closeSpan.EndLine - openSpan.StartLine + 1,
		Reason: ReasonCommentedBlock,
		Detail: detail,
	}
}

// normalizeEdits сортирует правки по позиции, склеивает пересечения и
// расширяет пустые удаления до целых строк.
func normalizeEdits(src []byte, edits []Edit) []Edit {
	if len(edits) == 0 {
		return nil
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Start == edits[j].Start {
			return edits[i].End > edits[j].End
		}
		return edits[i].Start < edits[j].Start
	})

	merged := make([]Edit, 0, len(edits))
	for _, e := range edits {
		if e.Replacement == "" {
			e = expandToWholeLines(src, e)
		}
		if n := len(merged); n > 0 && e.Start < merged[n-1].End {
			if e.End > merged[n-1].End {
				merged[n-1].End = e.End
				merged[n-1].Lines = countLines(src, merged[n-1].Start, merged[n-1].End)
			}
			continue
		}
		merged = append(merged, e)
	}
	return merged
}

// expandToWholeLines расширяет удаление до всей строки, если вокруг только
// пробелы и комментарии — тогда строку можно вырезать целиком. Идемпотентна:
// уже выровненная по строкам правка не расширяется повторно (recompute
// нормализует правки заново каждый раунд).
func expandToWholeLines(src []byte, e Edit) Edit {
	alignedStart := e.Start == 0 || src[e.Start-1] == '\n'
	alignedEnd := e.End >= len(src) || src[e.End-1] == '\n'
	if alignedStart && alignedEnd {
		return e
	}

	lineStart := e.Start
	if !alignedStart {
		for lineStart > 0 && src[lineStart-1] != '\n' {
			lineStart--
		}
		if !isFiller(src[lineStart:e.Start], false) {
			for e.Start > lineStart && (src[e.Start-1] == ' ' || src[e.Start-1] == '\t') {
				e.Start--
			}
			return e
		}
	}

	lineEnd := e.End
	if !alignedEnd {
		for lineEnd < len(src) && src[lineEnd] != '\n' {
			lineEnd++
		}
		if !isFiller(src[e.End:lineEnd], true) {
			return e
		}
		if lineEnd < len(src) {
			lineEnd++
		}
	}

	e.Start = lineStart
	e.End = lineEnd
	e.Lines = countLines(src, lineStart, lineEnd)
	return e
}

func countLines(src []byte, start, end int) int {
	n := 1
	for i := start; i < end && i < len(src); i++ {
		if src[i] == '\n' {
			n++
		}
	}
	if end > start && end <= len(src) && src[end-1] == '\n' {
		n--
	}
	if n < 1 {
		n = 1
	}
	return n
}

func isBlank(b []byte) bool {
	for _, c := range b {
		if !isSpace(c) {
			return false
		}
	}
	return true
}

func isFiller(seg []byte, allowLineComment bool) bool {
	for i := 0; i < len(seg); {
		c := seg[i]
		switch {
		case isSpace(c):
			i++
		case allowLineComment && c == '/' && i+1 < len(seg) && seg[i+1] == '/':
			return true
		case c == '/' && i+1 < len(seg) && seg[i+1] == '*':
			closing := bytes.Index(seg[i+2:], []byte("*/"))
			if closing < 0 {
				return false
			}
			i += closing + 4
		default:
			return false
		}
	}
	return true
}

func ApplyEdits(src []byte, edits []Edit) []byte {
	if len(edits) == 0 {
		return src
	}
	out := make([]byte, 0, len(src))
	prev := 0
	for _, e := range edits {
		if e.Start > prev {
			out = append(out, src[prev:e.Start]...)
		}
		if e.Replacement != "" {
			out = append(out, e.Replacement...)
		}
		if e.End > prev {
			prev = e.End
		}
	}
	if prev < len(src) {
		out = append(out, src[prev:]...)
	}
	return out
}
