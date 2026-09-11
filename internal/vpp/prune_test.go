package vpp

import (
	"strings"
	"testing"
)

func cleanSource(t *testing.T, src string) string {
	t.Helper()
	report := ScanSource("f.svh", []byte(src))
	edits := PlanCommentedDirectives(report, []byte(src))
	return string(ApplyEdits([]byte(src), edits))
}

func TestPlanCommentedDirectives(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single commented define drops whole line",
			src:  "`define A 1\n// `define B 2\n`define C 3\n",
			want: "`define A 1\n`define C 3\n",
		},
		{
			name: "commented define after code keeps the code",
			src:  "assign x = 1; // `define OLD 2\n",
			want: "assign x = 1;\n",
		},
		{
			name: "commented multiline define drops every line",
			src:  "// `define WIDE a + \\\n//     b + \\\n//     c\n`define KEEP 1\n",
			want: "`define KEEP 1\n",
		},
		{
			name: "commented ifdef block drops body",
			src: "`define KEEP 1\n" +
				"// `ifdef LEGACY\n" +
				"//   `define OLD_A 1\n" +
				"//   wire junk;\n" +
				"//   `define OLD_B 2\n" +
				"// `endif\n" +
				"`define ALSO_KEEP 2\n",
			want: "`define KEEP 1\n`define ALSO_KEEP 2\n",
		},
		{
			name: "commented nested ifdef drops outer range once",
			src: "// `ifdef A\n" +
				"//   `ifdef B\n" +
				"//     `define INNER 1\n" +
				"//   `endif\n" +
				"// `endif\n" +
				"`define KEEP 1\n",
			want: "`define KEEP 1\n",
		},
		{
			name: "block comment with define is removed entirely",
			src:  "/*\n`ifdef OLD\n`define X 1\n`endif\n*/\n`define KEEP 1\n",
			want: "`define KEEP 1\n",
		},
		{
			name: "inline block comment keeps surrounding code",
			src:  "wire a; /* `define Z 9 */ wire b;\n",
			want: "wire a; wire b;\n",
		},
		{
			name: "active ifdef with commented define inside",
			src:  "`ifdef DEBUG\n  // `define TRACE 1\n  `define LOG 1\n`endif\n",
			want: "`ifdef DEBUG\n  `define LOG 1\n`endif\n",
		},
		{
			name: "plain comments are untouched",
			src:  "// just a note\n`define A 1\n/* another */\n",
			want: "// just a note\n`define A 1\n/* another */\n",
		},
		{
			name: "commented undef is removed",
			src:  "`define A 1\n// `undef A\n",
			want: "`define A 1\n",
		},
		{
			name: "commented usage alone is kept",
			src:  "// see `WIDTH for details\n`define A 1\n",
			want: "// see `WIDTH for details\n`define A 1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanSource(t, tt.src); got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestPlanCommentedDirectivesReportsReason(t *testing.T) {
	src := "// `ifdef LEGACY\n//   `define OLD 1\n// `endif\n// `define SOLO 2\n"
	report := ScanSource("f.svh", []byte(src))
	edits := PlanCommentedDirectives(report, []byte(src))

	if len(edits) != 2 {
		t.Fatalf("got %d edits, want 2: %+v", len(edits), edits)
	}
	if edits[0].Reason != ReasonCommentedBlock || edits[0].Lines != 3 {
		t.Errorf("block edit = %+v", edits[0])
	}
	if !strings.Contains(edits[0].Detail, "OLD") {
		t.Errorf("block detail = %q, want it to mention OLD", edits[0].Detail)
	}
	if edits[1].Reason != ReasonCommentedDefine || edits[1].Line != 4 {
		t.Errorf("define edit = %+v", edits[1])
	}
}

func TestAnalyzeEmptiness(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantEmpty bool
		wantGuard string
	}{
		{
			name:      "guard only",
			src:       "`ifndef G\n`define G\n`endif\n",
			wantEmpty: true,
			wantGuard: "G",
		},
		{
			name:      "guard with comments only",
			src:       "`ifndef G\n`define G\n// note\n`endif\n",
			wantEmpty: true,
			wantGuard: "G",
		},
		{
			name:      "guard with a real define",
			src:       "`ifndef G\n`define G\n`define W 8\n`endif\n",
			wantEmpty: false,
			wantGuard: "G",
		},
		{
			name:      "guard with a package body",
			src:       "`ifndef G\n`define G\npackage p;\nendpackage\n`endif\n",
			wantEmpty: false,
			wantGuard: "G",
		},
		{
			name:      "empty file",
			src:       "\n\n",
			wantEmpty: true,
		},
		{
			name:      "only comments",
			src:       "// nothing here\n",
			wantEmpty: true,
		},
		{
			name:      "module without guard",
			src:       "module m; endmodule\n",
			wantEmpty: false,
		},
		{
			name:      "guard plus include only is empty",
			src:       "`ifndef G\n`define G\n`include \"x.vh\"\n`endif\n",
			wantEmpty: true,
			wantGuard: "G",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnalyzeEmptiness(ScanSource("f.svh", []byte(tt.src)), []byte(tt.src))
			if got.Empty != tt.wantEmpty {
				t.Errorf("empty = %v, want %v", got.Empty, tt.wantEmpty)
			}
			if got.Guard != tt.wantGuard {
				t.Errorf("guard = %q, want %q", got.Guard, tt.wantGuard)
			}
		})
	}
}
