package vpp

import "testing"

func pruneBranches(t *testing.T, src string, defined ...string) string {
	t.Helper()
	set := make(map[string]bool, len(defined))
	for _, name := range defined {
		set[name] = true
	}
	report := ScanSource("f.sv", []byte(src))
	edits := PlanDeadBranches(report, []byte(src), set, nil)
	return string(ApplyEdits([]byte(src), edits))
}

func TestPlanDeadBranches(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		defined []string
		want    string
	}{
		{
			name: "ifdef of unknown macro drops the whole block",
			src:  "wire a;\n`ifdef DEAD\n  wire dead;\n`endif\nwire b;\n",
			want: "wire a;\nwire b;\n",
		},
		{
			name:    "ifdef of defined macro is untouched",
			src:     "`ifdef LIVE\n  wire live;\n`endif\n",
			defined: []string{"LIVE"},
			want:    "`ifdef LIVE\n  wire live;\n`endif\n",
		},
		{
			name: "ifdef else keeps the else body",
			src:  "`ifdef DEAD\n  wire dead;\n`else\n  wire fallback;\n`endif\n",
			want: "  wire fallback;\n",
		},
		{
			name: "ifndef of unknown macro keeps its body",
			src:  "`ifndef DEAD\n  wire fallback;\n`endif\n",
			want: "  wire fallback;\n",
		},
		{
			name: "ifndef else drops the else body",
			src:  "`ifndef DEAD\n  wire fallback;\n`else\n  wire dead;\n`endif\n",
			want: "  wire fallback;\n",
		},
		{
			name:    "elsif of a live macro is promoted to ifdef",
			src:     "`ifdef DEAD\n  wire dead;\n`elsif LIVE\n  wire live;\n`endif\n",
			defined: []string{"LIVE"},
			want:    "`ifdef LIVE\n  wire live;\n`endif\n",
		},
		{
			name:    "dead elsif after a live ifdef is dropped",
			src:     "`ifdef LIVE\n  wire live;\n`elsif DEAD\n  wire dead;\n`endif\n",
			defined: []string{"LIVE"},
			want:    "`ifdef LIVE\n  wire live;\n`endif\n",
		},
		{
			name:    "dead elsif before else keeps the else",
			src:     "`ifdef LIVE\n  wire live;\n`elsif DEAD\n  wire dead;\n`else\n  wire other;\n`endif\n",
			defined: []string{"LIVE"},
			want:    "`ifdef LIVE\n  wire live;\n`else\n  wire other;\n`endif\n",
		},
		{
			name: "all branches dead without else drops everything",
			src:  "`ifdef A\n x;\n`elsif B\n y;\n`endif\n",
			want: "",
		},
		{
			name:    "nested dead block inside a live branch",
			src:     "`ifdef LIVE\n  wire live;\n  `ifdef DEAD\n    wire dead;\n  `endif\n`endif\n",
			defined: []string{"LIVE"},
			want:    "`ifdef LIVE\n  wire live;\n`endif\n",
		},
		{
			name: "nested block inside a taken else branch",
			src:  "`ifdef DEAD\n x;\n`else\n  `ifndef ALSO_DEAD\n  wire kept;\n  `endif\n`endif\n",
			want: "  wire kept;\n",
		},
		{
			name: "include guard is preserved",
			src:  "`ifndef GUARD\n`define GUARD\n`define W 8\n`endif\n",
			// GUARD is defined inside the file, so the guard is unknown, not dead
			defined: []string{"GUARD", "W"},
			want:    "`ifndef GUARD\n`define GUARD\n`define W 8\n`endif\n",
		},
		{
			name: "commented block is left to the comment pass",
			src:  "// `ifdef DEAD\n// `endif\nwire a;\n",
			want: "// `ifdef DEAD\n// `endif\nwire a;\n",
		},
		{
			name: "trailing comment after endif goes with the line",
			src:  "wire a;\n`ifdef DEAD\n  wire dead;\n`endif // DEAD\nwire b;\n",
			want: "wire a;\nwire b;\n",
		},
		{
			name: "trailing comments on else and endif go with their lines",
			src:  "`ifdef DEAD\n x;\n`else // fallback\n y;\n`endif /* DEAD */\n",
			want: " y;\n",
		},
		{
			name: "code after endif on the same line is kept",
			src:  "`ifdef DEAD\n x;\n`endif wire a;\n",
			want: " wire a;\n",
		},
		{
			name: "unterminated block comment after endif is not swallowed",
			src:  "`ifdef DEAD\n x;\n`endif /* start\n   end */\nwire a;\n",
			want: " /* start\n   end */\nwire a;\n",
		},
		{
			name: "dead block with a long body",
			src: "wire keep;\n`ifdef DEAD\n" +
				"  `define X 1\n  `define Y 2\n  module m;\n  endmodule\n" +
				"`endif\nwire also_keep;\n",
			want: "wire keep;\nwire also_keep;\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pruneBranches(t, tt.src, tt.defined...); got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestPlanDeadBranchesReasons(t *testing.T) {
	src := "`ifdef DEAD\n x;\n`elsif LIVE\n y;\n`endif\n"
	report := ScanSource("f.sv", []byte(src))
	edits := PlanDeadBranches(report, []byte(src), map[string]bool{"LIVE": true}, nil)

	if len(edits) != 2 {
		t.Fatalf("got %d edits, want 2: %+v", len(edits), edits)
	}
	if edits[0].Reason != ReasonDeadBranch || edits[0].Line != 1 {
		t.Errorf("edit[0] = %+v", edits[0])
	}
	if edits[1].Reason != ReasonBranchPromoted || edits[1].Replacement != "ifdef" {
		t.Errorf("edit[1] = %+v", edits[1])
	}
}
