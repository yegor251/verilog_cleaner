package vpp

import "testing"

func TestScanSource(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Directive
	}{
		{
			name: "active define with body",
			src:  "`define WIDTH 8\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "WIDTH", Body: "8", Line: 1, Column: 1},
			},
		},
		{
			name: "define with params",
			src:  "`define MAX(a, b) ((a) > (b) ? (a) : (b))\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "MAX", Params: []string{"a", "b"},
					Body: "((a) > (b) ? (a) : (b))", Line: 1, Column: 1},
			},
		},
		{
			name: "define commented by line comment",
			src:  "// `define DEBUG 1\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "DEBUG", Body: "1", Line: 1, Column: 4,
					Commented: true, CommentKind: CommentLine},
			},
		},
		{
			name: "define commented by block comment",
			src:  "/*\n `define DEBUG 1\n*/\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "DEBUG", Body: "1", Line: 2, Column: 2,
					Commented: true, CommentKind: CommentBlock},
			},
		},
		{
			name: "trailing comment is not part of body",
			src:  "`define WIDTH 8 // bits\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "WIDTH", Body: "8", Line: 1, Column: 1},
			},
		},
		{
			name: "conditional block",
			src:  "`ifdef A\n`elsif B\n`else\n`endif\n",
			want: []Directive{
				{Kind: KindIfdef, Directive: "ifdef", Name: "A", Line: 1, Column: 1},
				{Kind: KindElsif, Directive: "elsif", Name: "B", Line: 2, Column: 1},
				{Kind: KindElse, Directive: "else", Line: 3, Column: 1},
				{Kind: KindEndif, Directive: "endif", Line: 4, Column: 1},
			},
		},
		{
			name: "usage inside macro body",
			src:  "`define MAXV ((1 << `WIDTH) - 1)\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "MAXV", Body: "((1 << `WIDTH) - 1)", Line: 1, Column: 1},
				{Kind: KindMacroUsage, Directive: "WIDTH", Name: "WIDTH", Line: 1, Column: 21, InMacroBody: true},
			},
		},
		{
			name: "multiline define keeps continuation",
			src:  "`define A x + \\\n    y\n`B\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "A", Body: "x + \n    y", Line: 1, Column: 1},
				{Kind: KindMacroUsage, Directive: "B", Name: "B", Line: 3, Column: 1},
			},
		},
		{
			name: "backtick inside string literal is ignored",
			src:  "initial $display(\"`define NOPE\");\n`REAL\n",
			want: []Directive{
				{Kind: KindMacroUsage, Directive: "REAL", Name: "REAL", Line: 2, Column: 1},
			},
		},
		{
			name: "include quoted and bracketed",
			src:  "`include \"defs.vh\"\n`include <std.svh>\n",
			want: []Directive{
				{Kind: KindInclude, Directive: "include", Name: "defs.vh", Line: 1, Column: 1},
				{Kind: KindInclude, Directive: "include", Name: "std.svh", Line: 2, Column: 1},
			},
		},
		{
			name: "undef and timescale",
			src:  "`timescale 1ns / 1ps\n`undef WIDTH\n`undefineall\n",
			want: []Directive{
				{Kind: KindTimescale, Directive: "timescale", Body: "1ns / 1ps", Line: 1, Column: 1},
				{Kind: KindUndef, Directive: "undef", Name: "WIDTH", Line: 2, Column: 1},
				{Kind: KindUndefineAll, Directive: "undefineall", Line: 3, Column: 1},
			},
		},
		{
			name: "escaped identifier does not swallow directive",
			src:  "wire \\bus`name ;\n`OK\n",
			want: []Directive{
				{Kind: KindMacroUsage, Directive: "OK", Name: "OK", Line: 2, Column: 1},
			},
		},
		{
			name: "unterminated block comment marks everything commented",
			src:  "/* `define X 1\n`define Y 2\n",
			want: []Directive{
				{Kind: KindDefine, Directive: "define", Name: "X", Body: "1", Line: 1, Column: 4,
					Commented: true, CommentKind: CommentBlock},
				{Kind: KindDefine, Directive: "define", Name: "Y", Body: "2", Line: 2, Column: 1,
					Commented: true, CommentKind: CommentBlock},
			},
		},
		{
			name: "lone backtick produces nothing",
			src:  "a ` ` b\n",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSource("test.sv", []byte(tt.src)).Directives
			if len(got) != len(tt.want) {
				t.Fatalf("got %d directives, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				assertDirective(t, i, got[i], tt.want[i])
			}
		})
	}
}

func TestScanSourceCommentedDefineInsideActiveCode(t *testing.T) {
	src := "`define A 1\n// `define A 2\n`ifdef A\n  assign x = `A;\n`endif\n"
	report := ScanSource("mix.sv", []byte(src))

	summary := BuildSummary([]FileReport{report})
	macro := summary.Macros["A"]
	if macro == nil {
		t.Fatal("macro A not collected")
	}
	if !macro.DefinedActive() {
		t.Error("macro A must be active")
	}
	if len(macro.Definitions) != 2 {
		t.Fatalf("want 2 definitions, got %d", len(macro.Definitions))
	}
	if macro.Definitions[0].Commented || !macro.Definitions[1].Commented {
		t.Errorf("definition comment flags wrong: %+v", macro.Definitions)
	}
	if len(macro.Usages) != 1 || len(macro.Conditions) != 1 {
		t.Errorf("want 1 usage and 1 condition, got %d/%d", len(macro.Usages), len(macro.Conditions))
	}
}

func TestBuildSummaryClassification(t *testing.T) {
	reports := []FileReport{
		ScanSource("a.sv", []byte("`define USED 1\n`define UNUSED 2\nassign y = `USED;\n")),
		ScanSource("b.sv", []byte("// `define ONLY_COMMENTED 3\nassign z = `ONLY_COMMENTED;\n`ifdef FLAG\n`endif\n")),
	}

	s := BuildSummary(reports)

	assertContains(t, "UndefinedUsed", s.UndefinedUsed, "ONLY_COMMENTED")
	assertContains(t, "OnlyCommented", s.OnlyCommented, "ONLY_COMMENTED")
	assertContains(t, "DefinedUnused", s.DefinedUnused, "UNUSED")
	assertContains(t, "ConditionalOnly", s.ConditionalOnly, "FLAG")

	if s.Files != 2 {
		t.Errorf("files = %d, want 2", s.Files)
	}
	if s.ByKind["define"].Active != 2 || s.ByKind["define"].Commented != 1 {
		t.Errorf("define counts = %+v", s.ByKind["define"])
	}
}

func assertDirective(t *testing.T, i int, got, want Directive) {
	t.Helper()
	if got.Kind != want.Kind {
		t.Errorf("[%d] kind = %s, want %s", i, got.Kind, want.Kind)
	}
	if got.Directive != want.Directive {
		t.Errorf("[%d] directive = %q, want %q", i, got.Directive, want.Directive)
	}
	if got.Name != want.Name {
		t.Errorf("[%d] name = %q, want %q", i, got.Name, want.Name)
	}
	if got.Body != want.Body {
		t.Errorf("[%d] body = %q, want %q", i, got.Body, want.Body)
	}
	if got.Line != want.Line || got.Column != want.Column {
		t.Errorf("[%d] pos = %d:%d, want %d:%d", i, got.Line, got.Column, want.Line, want.Column)
	}
	if got.Commented != want.Commented || got.CommentKind != want.CommentKind {
		t.Errorf("[%d] commented = %v/%s, want %v/%s", i, got.Commented, got.CommentKind, want.Commented, want.CommentKind)
	}
	if got.InMacroBody != want.InMacroBody {
		t.Errorf("[%d] inMacroBody = %v, want %v", i, got.InMacroBody, want.InMacroBody)
	}
	if len(got.Params) != len(want.Params) {
		t.Fatalf("[%d] params = %v, want %v", i, got.Params, want.Params)
	}
	for j := range want.Params {
		if got.Params[j] != want.Params[j] {
			t.Errorf("[%d] param[%d] = %q, want %q", i, j, got.Params[j], want.Params[j])
		}
	}
}

func assertContains(t *testing.T, field string, items []string, want string) {
	t.Helper()
	for _, it := range items {
		if it == want {
			return
		}
	}
	t.Errorf("%s = %v, want to contain %q", field, items, want)
}
