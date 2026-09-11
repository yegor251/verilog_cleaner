package vpp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanerKeepsProtectedCommentedDefine(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"// `define TRGT_FPGA_INTEL_MAX10\n" +
			"// `define TRGT_SIMULATION\n" +
			"`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"  // legacy: TRGT_FPGA_INTEL_MAX10\n" +
			"  // sim: TRGT_SIMULATION\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{
		Out:       "out",
		Protected: []string{"TRGT_FPGA_INTEL_MAX10"},
	})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	cfg := sink.written[filepath.Join("out", "cfg.svh")]
	if !strings.Contains(cfg, "TRGT_FPGA_INTEL_MAX10") {
		t.Errorf("protected commented define must be kept: %q", cfg)
	}
	if strings.Contains(cfg, "TRGT_SIMULATION") {
		t.Errorf("non-protected commented define must still be removed: %q", cfg)
	}

	// The protected macro must not be scrubbed from other files either.
	core := sink.written[filepath.Join("out", "core.sv")]
	if !strings.Contains(core, "TRGT_FPGA_INTEL_MAX10") {
		t.Errorf("protected macro must not be scrubbed from references: %q", core)
	}
	if strings.Contains(core, "TRGT_SIMULATION") {
		t.Errorf("non-protected macro must still be scrubbed: %q", core)
	}

	// cfg.svh is edited (one commented define removed), core.sv is edited (scrub).
	assertAction(t, result, "root/core.sv", ActionEdit)
}

func TestCleanerKeepsBranchOfProtectedMacro(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"core.sv": "module core;\n" +
			"`ifdef PROFILE\n  wire prof;\n`endif\n" +
			"endmodule\n",
	})

	// PROFILE is never defined in the tree, but it is protected, so its
	// ifdef branch must be kept (as if it were externally defined).
	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{
		Out:       "out",
		Protected: []string{"PROFILE"},
	})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/core.sv", ActionKeep)
	if got := sink.written[filepath.Join("out", "core.sv")]; !strings.Contains(got, "`ifdef PROFILE") {
		t.Errorf("protected branch must be preserved: %q", got)
	}
}
