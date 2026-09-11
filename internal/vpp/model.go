package vpp

type Kind uint8

const (
	KindMacroUsage Kind = iota
	KindDefine
	KindUndef
	KindUndefineAll
	KindIfdef
	KindIfndef
	KindElsif
	KindElse
	KindEndif
	KindInclude
	KindTimescale
	KindPragma
	KindLine
	KindKeywords
	KindOther
)

var kindNames = map[Kind]string{
	KindMacroUsage:  "usage",
	KindDefine:      "define",
	KindUndef:       "undef",
	KindUndefineAll: "undefineall",
	KindIfdef:       "ifdef",
	KindIfndef:      "ifndef",
	KindElsif:       "elsif",
	KindElse:        "else",
	KindEndif:       "endif",
	KindInclude:     "include",
	KindTimescale:   "timescale",
	KindPragma:      "pragma",
	KindLine:        "line",
	KindKeywords:    "keywords",
	KindOther:       "other",
}

func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "unknown"
}

func (k Kind) IsConditional() bool {
	switch k {
	case KindIfdef, KindIfndef, KindElsif, KindElse, KindEndif:
		return true
	}
	return false
}

var directiveKinds = map[string]Kind{
	"define":                  KindDefine,
	"undef":                   KindUndef,
	"undefineall":             KindUndefineAll,
	"ifdef":                   KindIfdef,
	"ifndef":                  KindIfndef,
	"elsif":                   KindElsif,
	"else":                    KindElse,
	"endif":                   KindEndif,
	"include":                 KindInclude,
	"timescale":               KindTimescale,
	"pragma":                  KindPragma,
	"line":                    KindLine,
	"begin_keywords":          KindKeywords,
	"end_keywords":            KindKeywords,
	"resetall":                KindOther,
	"celldefine":              KindOther,
	"endcelldefine":           KindOther,
	"default_nettype":         KindOther,
	"unconnected_drive":       KindOther,
	"nounconnected_drive":     KindOther,
	"default_decay_time":      KindOther,
	"default_trireg_strength": KindOther,
	"delay_mode_distributed":  KindOther,
	"delay_mode_path":         KindOther,
	"delay_mode_unit":         KindOther,
	"delay_mode_zero":         KindOther,
	"accelerate":              KindOther,
	"noaccelerate":            KindOther,
	"protect":                 KindOther,
	"endprotect":              KindOther,
	"protected":               KindOther,
	"endprotected":            KindOther,
	"expand_vectornets":       KindOther,
	"noexpand_vectornets":     KindOther,
	"autoexpand_vectornets":   KindOther,
	"remove_gatenames":        KindOther,
	"noremove_gatenames":      KindOther,
	"remove_netnames":         KindOther,
	"noremove_netnames":       KindOther,
	"__FILE__":                KindOther,
	"__LINE__":                KindOther,
}

type CommentKind uint8

const (
	CommentNone CommentKind = iota
	CommentLine
	CommentBlock
)

func (c CommentKind) String() string {
	switch c {
	case CommentLine:
		return "line"
	case CommentBlock:
		return "block"
	default:
		return "none"
	}
}

type Directive struct {
	Kind         Kind        `json:"kind"`
	Directive    string      `json:"directive"`
	Name         string      `json:"name,omitempty"`
	Params       []string    `json:"params,omitempty"`
	Body         string      `json:"body,omitempty"`
	Line         int         `json:"line"`
	Column       int         `json:"column"`
	EndLine      int         `json:"end_line"`
	Offset       int         `json:"offset"`
	End          int         `json:"end"`
	Commented    bool        `json:"commented"`
	CommentKind  CommentKind `json:"comment_kind"`
	CommentIndex int         `json:"-"`
	InMacroBody  bool        `json:"in_macro_body,omitempty"`
}

type CommentSpan struct {
	Kind      CommentKind `json:"kind"`
	Start     int         `json:"start"`
	End       int         `json:"end"`
	StartLine int         `json:"start_line"`
	StartCol  int         `json:"start_col"`
	EndLine   int         `json:"end_line"`
}

type FileReport struct {
	Path       string        `json:"path"`
	Directives []Directive   `json:"directives"`
	Comments   []CommentSpan `json:"-"`
	Source     []byte        `json:"-"`
	Err        string        `json:"error,omitempty"`
}

type Location struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Commented bool   `json:"commented"`
}

type MacroInfo struct {
	Name        string     `json:"name"`
	Definitions []Location `json:"definitions,omitempty"`
	Undefs      []Location `json:"undefs,omitempty"`
	Usages      []Location `json:"usages,omitempty"`
	Conditions  []Location `json:"conditions,omitempty"`
}

func (m *MacroInfo) activeCount(locs []Location) int {
	n := 0
	for _, l := range locs {
		if !l.Commented {
			n++
		}
	}
	return n
}

func (m *MacroInfo) DefinedActive() bool {
	return m.activeCount(m.Definitions) > 0
}

func (m *MacroInfo) DefinedOnlyCommented() bool {
	return len(m.Definitions) > 0 && m.activeCount(m.Definitions) == 0
}

func (m *MacroInfo) ReferencedActive() bool {
	return m.activeCount(m.Usages)+m.activeCount(m.Conditions)+m.activeCount(m.Undefs) > 0
}

func (m *MacroInfo) Referenced() bool {
	return len(m.Usages)+len(m.Conditions)+len(m.Undefs) > 0
}
