package vpp

// CondEnv — макро-окружение для планировщика веток. Elsewhere — макросы,
// определённые вне данного файла (другие файлы, теневые include-заголовки,
// внешние флаги сборки, защищённые): их условия консервативно считаются
// живыми, потому что порядок компиляции файлов неизвестен.
type CondEnv struct {
	Elsewhere map[string]bool
}

type condValue uint8

const (
	condFalse condValue = iota
	condTrue
	condUnknown
)

type condSection struct {
	dir       int
	bodyStart int
	bodyEnd   int
}

type condBlock struct {
	sections []condSection
	start    int
	end      int
	children []*condBlock
}

// parseCondBlocks разбирает вложенные ifdef..endif в дерево условных блоков.
// Директивы внутри тел макросов (InMacroBody) пропускаются: при раскрытии
// макроса их условия зависят от аргументов, статически они не вычисляются, и
// они не должны спариваться с endif'ами уровня файла.
func parseCondBlocks(report FileReport) []*condBlock {
	var roots []*condBlock
	var stack []*condBlock

	for i, d := range report.Directives {
		if d.Commented || d.InMacroBody {
			continue
		}
		switch d.Kind {
		case KindIfdef, KindIfndef:
			b := &condBlock{start: d.Offset}
			b.sections = append(b.sections, condSection{dir: i, bodyStart: d.End})
			stack = append(stack, b)
		case KindElsif, KindElse:
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			top.sections[len(top.sections)-1].bodyEnd = d.Offset
			top.sections = append(top.sections, condSection{dir: i, bodyStart: d.End})
		case KindEndif:
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			top.sections[len(top.sections)-1].bodyEnd = d.Offset
			top.end = d.End
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, top)
			} else {
				roots = append(roots, top)
			}
		}
	}
	return roots
}

// condTimeline отслеживает, какие макросы убиты в самом файле его собственными
// `undef/`undefineall. Условие на макрос заведомо ложно (или истинно для
// `ifndef), только если макрос не определён вне файла и к точке условия убит
// его же `undef либо не определён вовсе; в остальных случаях значение
// неизвестно (ветка сохраняется).
type condTimeline struct {
	elsewhere map[string]bool
	defined   map[string]int // имя -> смещение последнего `define
	killedAt  map[string]int // имя -> смещение `undef/`undefineall, убившего макрос
}

func newCondTimeline(report FileReport, env CondEnv, skip func(offset int) bool) *condTimeline {
	t := &condTimeline{
		elsewhere: env.Elsewhere,
		defined:   make(map[string]int),
		killedAt:  make(map[string]int),
	}
	for _, d := range report.Directives {
		if d.Commented {
			continue
		}
		removed := skip != nil && skip(d.Offset)
		switch d.Kind {
		case KindDefine:
			if d.Name == "" || removed {
				continue
			}
			t.defined[d.Name] = d.Offset
			delete(t.killedAt, d.Name) // define после undef оживляет макрос
		case KindUndef:
			if d.Name == "" || d.InMacroBody || removed || t.elsewhere[d.Name] {
				continue
			}
			if at, ok := t.defined[d.Name]; !ok || at < d.Offset {
				t.killedAt[d.Name] = d.Offset
			}
		case KindUndefineAll:
			if d.InMacroBody || removed {
				continue
			}
			for name, at := range t.defined {
				if !t.elsewhere[name] && at < d.Offset {
					t.killedAt[name] = d.Offset
				}
			}
		}
	}
	return t
}

// value вычисляет значение условия на макрос d.
func (t *condTimeline) value(d Directive) condValue {
	if d.Kind == KindElse {
		return condTrue
	}
	if d.Name == "" || t.elsewhere[d.Name] {
		return condUnknown
	}
	if at, killed := t.killedAt[d.Name]; killed && at <= d.Offset {
		return deadValue(d.Kind)
	}
	if _, alive := t.defined[d.Name]; alive {
		return condUnknown
	}
	return deadValue(d.Kind)
}

func deadValue(kind Kind) condValue {
	switch kind {
	case KindIfdef, KindElsif:
		return condFalse
	case KindIfndef:
		return condTrue
	}
	return condUnknown
}

// PlanDeadBranches строит правки для удаления мёртвых веток: выбранную живую
// ветку оставляет, остальные помечает на вырезку.
func PlanDeadBranches(report FileReport, src []byte, env CondEnv, skip func(offset int) bool) []Edit {
	if skip == nil {
		skip = func(int) bool { return false }
	}
	t := newCondTimeline(report, env, skip)
	values := make([]condValue, len(report.Directives))
	for i := range report.Directives {
		values[i] = t.value(report.Directives[i])
	}
	p := &branchPlanner{report: report, src: src, values: values, skip: skip}
	p.planBlocks(parseCondBlocks(report))
	return normalizeEdits(src, p.edits)
}

type branchPlanner struct {
	report FileReport
	src    []byte
	values []condValue
	skip   func(offset int) bool
	edits  []Edit
}

func (p *branchPlanner) planBlocks(blocks []*condBlock) {
	for _, b := range blocks {
		if p.skip(b.start) {
			continue
		}
		p.planBlock(b)
	}
}

func (p *branchPlanner) planBlock(b *condBlock) {
	chosen, unknown := -1, -1
	for i, s := range b.sections {
		switch p.values[s.dir] {
		case condFalse:
			continue
		case condTrue:
			chosen = i
		default:
			unknown = i
		}
		break
	}

	switch {
	case chosen >= 0:
		p.keepOnly(b, chosen)
	case unknown >= 0:
		p.dropLeading(b, unknown)
		p.dropFalseTail(b, unknown)
	default:
		p.drop(b, Edit{
			Start:  b.start,
			End:    b.end,
			Reason: ReasonDeadBranch,
			Detail: p.label(b, 0) + " is never defined",
		})
	}
}

func (p *branchPlanner) keepOnly(b *condBlock, idx int) {
	s := b.sections[idx]
	detail := p.label(b, idx) + " always taken"

	p.drop(b, Edit{Start: b.start, End: s.bodyStart, Reason: ReasonDeadBranch, Detail: detail})
	p.drop(b, Edit{Start: s.bodyEnd, End: b.end, Reason: ReasonDeadBranch, Detail: detail})
	p.planBlocks(childrenWithin(b, s))
}

func (p *branchPlanner) dropLeading(b *condBlock, idx int) {
	if idx == 0 {
		for i := idx; i < len(b.sections); i++ {
			p.planBlocks(childrenWithin(b, b.sections[i]))
		}
		return
	}

	d := p.report.Directives[b.sections[idx].dir]
	p.drop(b, Edit{
		Start:  b.start,
		End:    d.Offset,
		Reason: ReasonDeadBranch,
		Detail: p.label(b, 0) + " is never defined",
	})

	if d.Kind == KindElsif {
		p.edits = append(p.edits, Edit{
			Start:       d.Offset + 1,
			End:         d.Offset + 1 + len("elsif"),
			Line:        d.Line,
			Lines:       1,
			Reason:      ReasonBranchPromoted,
			Detail:      "`elsif " + d.Name + " -> `ifdef " + d.Name,
			Replacement: "ifdef",
		})
	}

	for i := idx; i < len(b.sections); i++ {
		p.planBlocks(childrenWithin(b, b.sections[i]))
	}
}

func (p *branchPlanner) dropFalseTail(b *condBlock, from int) {
	for i := from + 1; i < len(b.sections); i++ {
		s := b.sections[i]
		d := p.report.Directives[s.dir]
		if p.values[s.dir] != condFalse {
			continue
		}
		end := s.bodyEnd
		if i+1 < len(b.sections) {
			end = p.report.Directives[b.sections[i+1].dir].Offset
		}
		p.drop(b, Edit{
			Start:  d.Offset,
			End:    end,
			Reason: ReasonDeadBranch,
			Detail: "`" + d.Directive + " " + d.Name + " is never defined",
		})
	}
}

func (p *branchPlanner) drop(b *condBlock, e Edit) {
	if e.End <= e.Start {
		return
	}
	e.Line = lineAt(p.src, e.Start)
	e.Lines = countLines(p.src, e.Start, e.End)
	p.edits = append(p.edits, e)
}

func (p *branchPlanner) label(b *condBlock, idx int) string {
	d := p.report.Directives[b.sections[idx].dir]
	if d.Name == "" {
		return "`" + d.Directive
	}
	return "`" + d.Directive + " " + d.Name
}

func childrenWithin(b *condBlock, s condSection) []*condBlock {
	out := make([]*condBlock, 0, len(b.children))
	for _, child := range b.children {
		if child.start >= s.bodyStart && child.end <= s.bodyEnd {
			out = append(out, child)
		}
	}
	return out
}

func lineAt(src []byte, offset int) int {
	line := 1
	for i := 0; i < offset && i < len(src); i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}
