package vpp

import "sort"

type Summary struct {
	Files           int                   `json:"files"`
	FilesFailed     int                   `json:"files_failed"`
	Total           int                   `json:"total_directives"`
	Active          int                   `json:"active_directives"`
	Commented       int                   `json:"commented_directives"`
	ByKind          map[string]KindCounts `json:"by_kind"`
	Macros          map[string]*MacroInfo `json:"macros"`
	UndefinedUsed   []string              `json:"undefined_used"`
	OnlyCommented   []string              `json:"defined_only_commented"`
	DefinedUnused   []string              `json:"defined_unused"`
	ConditionalOnly []string              `json:"conditional_only"`
}

type KindCounts struct {
	Active    int `json:"active"`
	Commented int `json:"commented"`
}

// BuildSummary агрегирует отчёты файлов: счётчики по видам, данные по
// макросам и списки проблем (неопределённые, только в комментариях и т.п.).
func BuildSummary(reports []FileReport) *Summary {
	s := &Summary{
		ByKind: make(map[string]KindCounts),
		Macros: make(map[string]*MacroInfo),
	}

	for _, r := range reports {
		s.Files++
		if r.Err != "" {
			s.FilesFailed++
		}
		for _, d := range r.Directives {
			s.Total++
			counts := s.ByKind[d.Kind.String()]
			if d.Commented {
				s.Commented++
				counts.Commented++
			} else {
				s.Active++
				counts.Active++
			}
			s.ByKind[d.Kind.String()] = counts

			if d.Name == "" {
				continue
			}
			loc := Location{Path: r.Path, Line: d.Line, Column: d.Column, Commented: d.Commented}
			switch d.Kind {
			case KindDefine:
				m := s.macro(d.Name)
				m.Definitions = append(m.Definitions, loc)
			case KindUndef:
				m := s.macro(d.Name)
				m.Undefs = append(m.Undefs, loc)
			case KindIfdef, KindIfndef, KindElsif:
				m := s.macro(d.Name)
				m.Conditions = append(m.Conditions, loc)
			case KindMacroUsage:
				m := s.macro(d.Name)
				m.Usages = append(m.Usages, loc)
			}
		}
	}

	for name, m := range s.Macros {
		if !m.DefinedActive() && m.activeCount(m.Usages) > 0 {
			s.UndefinedUsed = append(s.UndefinedUsed, name)
		}
		if m.DefinedOnlyCommented() {
			s.OnlyCommented = append(s.OnlyCommented, name)
		}
		if m.DefinedActive() && !m.ReferencedActive() {
			s.DefinedUnused = append(s.DefinedUnused, name)
		}
		if len(m.Definitions) == 0 && len(m.Usages) == 0 && len(m.Conditions) > 0 {
			s.ConditionalOnly = append(s.ConditionalOnly, name)
		}
	}

	sort.Strings(s.UndefinedUsed)
	sort.Strings(s.OnlyCommented)
	sort.Strings(s.DefinedUnused)
	sort.Strings(s.ConditionalOnly)

	return s
}

func (s *Summary) macro(name string) *MacroInfo {
	m, ok := s.Macros[name]
	if !ok {
		m = &MacroInfo{Name: name}
		s.Macros[name] = m
	}
	return m
}

func (s *Summary) MacroNames() []string {
	names := make([]string, 0, len(s.Macros))
	for name := range s.Macros {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
