package vpp

import (
	"context"
	"path/filepath"
	"testing"
)

// A header that is only referenced from a dead (removed) define branch must be
// deleted even though it still contains active content.
func TestCleanerRemovesNonEmptyHeaderOrphanedByDeadBranch(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"unused.svh": "`ifndef UNUSED\n`define UNUSED\n`define SETTING 4\n`endif\n",
		"top.sv": "module top;\n" +
			"`ifdef NEVER_DEFINED\n" +
			"  `include \"unused.svh\"\n" +
			"`endif\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/unused.svh", ActionRemove)
	assertAction(t, result, "root/top.sv", ActionEdit)

	if _, ok := sink.written[filepath.Join("out", "unused.svh")]; ok {
		t.Errorf("orphaned non-empty header must be removed from the clean copy")
	}
}

// A header orphaned only after its includer is itself deleted is removed too
// (the orphaning cascades through the include graph).
func TestCleanerCascadesOrphanedNonEmptyHeaders(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"mid.svh":  "`ifndef MID\n`define MID\n`include \"leaf.svh\"\n`endif\n",
		"leaf.svh": "`ifndef LEAF\n`define LEAF\n`define DEPTH 2\n`endif\n",
		"top.sv": "module top;\n" +
			"`ifdef NEVER_DEFINED\n" +
			"  `include \"mid.svh\"\n" +
			"`endif\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	if result.Rounds < 2 {
		t.Errorf("rounds = %d, want at least 2 for cascading header removal", result.Rounds)
	}
	assertAction(t, result, "root/mid.svh", ActionRemove)
	assertAction(t, result, "root/leaf.svh", ActionRemove)

	for _, p := range []string{"mid.svh", "leaf.svh"} {
		if _, ok := sink.written[filepath.Join("out", p)]; ok {
			t.Errorf("%s must not be written to the clean copy", p)
		}
	}
}

// A top-level module that is never included by anything stays, because it is
// not "orphaned by a removed define branch".
func TestCleanerKeepsUnreferencedModule(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"top.sv": "module top; endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/top.sv", ActionKeep)
	if _, ok := sink.written[filepath.Join("out", "top.sv")]; !ok {
		t.Errorf("top-level module must be kept in the clean copy")
	}
}
