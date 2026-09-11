package vpp

import (
	"bytes"
	"sort"
)

// scrubRemovedMacros — финальный проход после каскада: собирает имена
// удалённых макросов и вычищает их из выживших файлов, даже как подстроку
// (например внутри комментария).
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
		planned := planMacroScrub(st.orig, st.edits, removed)
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
// другими правками, и планирует их удаление. Использования `NAME теряют
// ведущий бэктик, а строки, где после удаления осталась лишь пунктуация
// комментария, удаляются целиком.
func planMacroScrub(src []byte, edits []Edit, names []string) []Edit {
	var out []Edit

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
				rmStart, rmEnd := start, end
				if start > lineStart && src[start-1] == '`' && !overlapsEdits(edits, start-1, start) {
					rmStart = start - 1
				}
				for i := rmStart; i < rmEnd; i++ {
					removed[i] = true
				}
				occ = append(occ, Edit{
					Start:  rmStart,
					End:    rmEnd,
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
				Reason: ReasonRemovedMacro,
				Detail: "removed macro reference",
			})
		} else {
			out = append(out, occ...)
		}

		lineStart = nextLine
	}

	return normalizeEdits(src, out)
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

func overlapsEdits(edits []Edit, start, end int) bool {
	for _, e := range edits {
		if start < e.End && end > e.Start {
			return true
		}
	}
	return false
}
