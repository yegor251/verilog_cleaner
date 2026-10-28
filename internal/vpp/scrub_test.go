package vpp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanerScrubsRemovedMacroFromComment(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define TRGT_FPGA_INTEL_MAX10\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  // legacy: TRGT_FPGA_INTEL_MAX10 no longer supported\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/core.sv", ActionEdit)

	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "TRGT_FPGA_INTEL_MAX10") {
		t.Errorf("removed macro still referenced in core.sv: %q", core)
	}
	if !strings.Contains(core, "no longer supported") {
		t.Errorf("surrounding comment text must survive the scrub: %q", core)
	}
}

func TestCleanerScrubsDedicatedCommentLine(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define TRGT_FPGA_INTEL_MAX10\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  // TRGT_FPGA_INTEL_MAX10\n" +
			"endmodule\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "TRGT_FPGA_INTEL_MAX10") || strings.Contains(core, "//") {
		t.Errorf("comment-only line must be dropped entirely: %q", core)
	}
	if !strings.Contains(core, "module core;") || !strings.Contains(core, "endmodule") {
		t.Errorf("module body must survive: %q", core)
	}
}

func TestCleanerScrubsRemovedMacroUsageWithBacktick(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"`ifdef DEAD\n`define FOO 1\n`endif\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  assign x = `FOO + 1;\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/core.sv", ActionEdit)
	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "FOO") || strings.Contains(core, "`FOO") {
		t.Errorf("macro usage must be scrubbed together with its backtick: %q", core)
	}
}

func TestCleanerKeepsAliveMacroReferences(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define LEGACY 1\n" +
			"`define LIVE 1\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  // uses LIVE here\n" +
			"  // old: LEGACY\n" +
			"endmodule\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if !strings.Contains(core, "LIVE") {
		t.Errorf("still-defined macro must NOT be scrubbed: %q", core)
	}
	if strings.Contains(core, "LEGACY") {
		t.Errorf("removed macro must be scrubbed: %q", core)
	}
}

func TestCleanerScrubsRemovedMacroAsSubstring(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define PORT\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  // PORT_A and PORT_B pins\n" +
			"endmodule\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "PORT") {
		t.Errorf("removed macro must be scrubbed as a substring: %q", core)
	}
}

// Имя удалённого макроса на строке выжившего `ifdef — операнд директивы,
// а не использование: вычищать его нельзя, иначе останется «голый» `ifdef.
func TestScrubKeepsNameOnSurvivingIfdefLine(t *testing.T) {
	src := "`ifdef FEATURE\nwire f;\n`else\nwire g;\n`endif\n"
	report := ScanSource("f.sv", []byte(src))
	edits := planMacroScrub([]byte(src), report, nil, []string{"FEATURE"})
	if got := string(ApplyEdits([]byte(src), edits)); got != src {
		t.Errorf("ifdef operand must not be scrubbed, got:\n%q", got)
	}

	src2 := "`undef FEATURE\nwire keep;\n"
	report2 := ScanSource("f.sv", []byte(src2))
	edits2 := planMacroScrub([]byte(src2), report2, nil, []string{"FEATURE"})
	if got := string(ApplyEdits([]byte(src2), edits2)); got != src2 {
		t.Errorf("undef operand must not be scrubbed, got:\n%q", got)
	}
}

func TestCleanerKeepsMacroNameInsideStringLiteral(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define MODE_A\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  initial $display(\"MODE_A is enabled\");\n" +
			"  initial $display(\"state = MODE_A;\");\n" +
			"endmodule\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if !strings.Contains(core, `$display("MODE_A is enabled")`) {
		t.Errorf("macro name inside a string literal must be kept: %q", core)
	}
	if !strings.Contains(core, `"state = MODE_A;"`) {
		t.Errorf("macro name inside a string literal must be kept: %q", core)
	}
}

func TestCleanerDoesNotCorruptIdentifiersContainingMacroName(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define MODE_A\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  wire MODE_A_bus;\n" +
			"endmodule\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if !strings.Contains(core, "wire MODE_A_bus;") {
		t.Errorf("identifier containing a removed macro name must not be corrupted: %q", core)
	}
}
