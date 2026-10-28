package vpp

import (
	"bytes"
	"sort"
)

// scrubRemovedMacros — финальный проход после каскада: собирает имена
// удалённых макросов и вычищает их из выживших файлов, даже как подстроку
// (например внутри комментария). Строковые литералы не трогаются.
func (c *Cleaner) scrubRemovedMacros(states map[string]*fileState, paths []string, protected map[string]bool) {
	removed := removedDefineNames(states, paths, protected)
	if len(removed) == 0 {
		return
	}

	for _, p := range paths {
		st := states[p]
		if st.removed {
			continue
		}
		planned := planMacroScrub(st.orig, st.base, st.edits, removed)
		if len(planned) == 0 {
			continue
		}
		st.edits = append(st.edits, planned...)
		st.recompute()
	}
}

// removedDefineNames возвращает имена макросов, чей `define удалён и у которых
// не осталось активного определения ни в одном выжившем файле. Ещё
// определённые и защищённые макросы не трогаем.
func removedDefineNames(states map[string]*fileState, paths []string, protected map[string]bool) []string {
	removed := make(map[string]bool)
	alive := make(map[string]bool)

	for _, p := range paths {
		st := states[p]
		for _, d := range st.base.Directives {
			if d.Kind != KindDefine || d.Name == "" {
				continue
			}
			if st.removed || st.covered(d.Offset) {
				removed[d.Name] = true
			}
		}
		if st.removed {
			continue
		}
		for _, d := range st.base.Directives {
			if d.Commented || d.Kind != KindDefine || d.Name == "" {
				continue
			}
			if !st.covered(d.Offset) {
				alive[d.Name] = true
			}
		}
	}

	out := make([]string, 0, len(removed))
	for name := range removed {
		if alive[name] || protected[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// planMacroScrub находит вхождения имени удалённого макроса, не перекрытые
// другими правками, и планирует их удаление. Вхождения внутри строковых
// литералов пропускаются; в коде имя удаляется только целым токеном (не как
// часть более длинного идентификатора); в комментарях — как подстроку.
// Строки, где после удаления осталась лишь пунктуация комментария, удаляются
// целиком.
func planMacroScrub(src []byte, report FileReport, edits []Edit, names []string) []Edit {
	var out []Edit

	operand := conditionalOperandRanges(report)
	lineNo := 1
	lineStart := 0
	for lineStart <= len(src) {
		lineEnd := bytes.IndexByte(src[lineStart:], '\n')
		var lineEndByte, nextLine int
		if lineEnd < 0 {
			lineEndByte = len(src)
			nextLine = len(src) + 1
		} else {
			lineEndByte = lineStart + lineEnd
			nextLine = lineEndByte + 1
		}

		var occ []Edit
		removed := make(map[int]bool)
		for _, name := range names {
			from := lineStart
			for {
				idx := bytes.Index(src[from:lineEndByte], []byte(name))
				if idx < 0 {
					break
				}
				start := from + idx
				end := start + len(name)
				from = start + len(name)

				if overlapsEdits(edits, start, end) {
					continue
				}
				if inString(report.Strings, start) {
					continue
				}
				if inString(operand, start) {
					continue
				}
				if !scrubbableAt(src, report, start, end) {
					continue
				}
				rmStart, rmEnd := start, end
				if start > 0 && src[start-1] == '`' && !overlapsEdits(edits, start-1, start) {
					rmStart = start - 1
				}
				for i := rmStart; i < rmEnd; i++ {
					removed[i] = true
				}
				occ = append(occ, Edit{
					Start:  rmStart,
					End:    rmEnd,
					Line:   lineNo,
					Lines:  1,
					Reason: ReasonRemovedMacro,
					Detail: "`" + name,
				})
			}
		}

		if len(occ) > 0 && lineInert(src, lineStart, lineEndByte, removed) {
			wholeEnd := lineEndByte
			if lineEndByte < len(src) {
				wholeEnd++
			}
			out = append(out, Edit{
				Start:  lineStart,
				End:    wholeEnd,
				Line:   lineNo,
				Lines:  1,
				Reason: ReasonRemovedMacro,
				Detail: "removed macro reference",
			})
		} else {
			out = append(out, occ...)
		}

		lineNo++
		lineStart = nextLine
	}

	return normalizeEdits(src, out)
}

// scrubbableAt решает, можно ли вычистить вхождение имени: в комментарии —
// всегда (как подстрока), в коде — только если это целый токен.
func scrubbableAt(src []byte, report FileReport, start, end int) bool {
	if inComment(report.Comments, start) {
		return true
	}
	if start > 0 && isIdentPart(src[start-1]) {
		return false
	}
	if end < len(src) && isIdentPart(src[end]) {
		return false
	}
	return true
}

// lineInert — true, если после удаления указанных позиций в строке осталась
// только пунктуация комментария и пробелы.
func lineInert(src []byte, lineStart, lineEnd int, removed map[int]bool) bool {
	for i := lineStart; i < lineEnd; i++ {
		if removed[i] {
			continue
		}
		c := src[i]
		if isSpace(c) || c == '/' || c == '*' {
			continue
		}
		return false
	}
	return true
}

// conditionalOperandRanges — диапазоны условных директив и `undef: имя макроса
// в них — операнд директивы, а не использование, и вычищать его нельзя,
// иначе останется «голый» `ifdef.
func conditionalOperandRanges(report FileReport) []Span {
	var out []Span
	for _, d := range report.Directives {
		if d.Commented || d.Name == "" {
			continue
		}
		switch d.Kind {
		case KindIfdef, KindIfndef, KindElsif, KindUndef:
			out = append(out, Span{Start: d.Offset, End: d.End})
		}
	}
	return out
}

// inString — находится ли позиция внутри отсортированного диапазона спанов
// (строковые литералы, операнды директив).
func inString(spans []Span, pos int) bool {
	lo, hi := 0, len(spans)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case pos < spans[mid].Start:
			hi = mid - 1
		case pos >= spans[mid].End:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// inComment — находится ли позиция внутри комментария.
func inComment(spans []CommentSpan, pos int) bool {
	lo, hi := 0, len(spans)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case pos < spans[mid].Start:
			hi = mid - 1
		case pos >= spans[mid].End:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

func overlapsEdits(edits []Edit, start, end int) bool {
	for _, e := range edits {
		if start < e.End && end > e.Start {
			return true
		}
	}
	return false
}
