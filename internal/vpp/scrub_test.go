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
