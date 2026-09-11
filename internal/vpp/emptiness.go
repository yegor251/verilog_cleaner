package vpp

type Emptiness struct {
	Empty bool   `json:"empty"`
	Guard string `json:"guard,omitempty"`
}

func AnalyzeEmptiness(report FileReport, src []byte) Emptiness {
	active := make([]int, 0, len(report.Directives))
	for i, d := range report.Directives {
		if !d.Commented {
			active = append(active, i)
		}
	}

	guard, guardSet := detectGuard(report, active)

	inert := make([]int, 0, len(active))
	for _, i := range active {
		if guardSet[i] || report.Directives[i].Kind == KindInclude {
			inert = append(inert, i)
			continue
		}
		return Emptiness{Empty: false, Guard: guard}
	}

	masked := make([]Edit, 0, len(report.Comments)+len(inert))
	for _, span := range report.Comments {
		masked = append(masked, Edit{Start: span.Start, End: span.End})
	}
	for _, i := range inert {
		d := report.Directives[i]
		masked = append(masked, Edit{Start: d.Offset, End: d.End})
	}

	rest := ApplyEdits(src, normalizeEdits(src, masked))
	return Emptiness{Empty: isBlank(rest), Guard: guard}
}

func detectGuard(report FileReport, active []int) (string, map[int]bool) {
	if len(active) < 3 {
		return "", map[int]bool{}
	}

	first := report.Directives[active[0]]
	second := report.Directives[active[1]]
	last := report.Directives[active[len(active)-1]]

	if first.Kind != KindIfndef || second.Kind != KindDefine || last.Kind != KindEndif {
		return "", map[int]bool{}
	}
	if first.Name == "" || first.Name != second.Name {
		return "", map[int]bool{}
	}
	if second.Body != "" || len(second.Params) > 0 {
		return "", map[int]bool{}
	}

	return first.Name, map[int]bool{
		active[0]:             true,
		active[1]:             true,
		active[len(active)-1]: true,
	}
}
