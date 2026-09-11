package vpp

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

type stubSink struct {
	written map[string]string
	dirs    map[string]bool
	exists  map[string]bool
	removed []string
}

func newStubSink() *stubSink {
	return &stubSink{
		written: map[string]string{},
		dirs:    map[string]bool{},
		exists:  map[string]bool{},
	}
}

func (s *stubSink) Exists(path string) (bool, error) { return s.exists[path], nil }

func (s *stubSink) MkdirAll(path string, _ fs.FileMode) error {
	s.dirs[path] = true
	return nil
}

func (s *stubSink) WriteFile(path string, data []byte, _ fs.FileMode) error {
	s.written[path] = string(data)
	return nil
}

func (s *stubSink) RemoveAll(path string) error {
	s.removed = append(s.removed, path)
	delete(s.exists, path)
	return nil
}

func newTestCleaner(files map[string]string) (*Cleaner, *stubSink) {
	stub := newStub(withPrefix(files, "root"))
	sink := newStubSink()
	scanner := NewScanner(stub, Options{Workers: 4, KeepSource: true})
	return NewCleaner(stub, sink, scanner), sink
}

func TestCleanerRemovesEmptiedUnusedHeader(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"dead.vh": "`ifndef DEAD_VH\n`define DEAD_VH\n// `define LEGACY_MODE 1\n`endif\n",
		"live.vh": "`ifndef LIVE_VH\n`define LIVE_VH\n`define WIDTH 8\n`endif\n",
		"top.sv":  "`include \"live.vh\"\nmodule top; endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/dead.vh", ActionRemove)
	assertAction(t, result, "root/live.vh", ActionKeep)
	assertAction(t, result, "root/top.sv", ActionKeep)

	if _, ok := sink.written[filepath.Join("out", "dead.vh")]; ok {
		t.Error("dead.vh must not be written to the clean copy")
	}
	if got := sink.written[filepath.Join("out", "live.vh")]; !strings.Contains(got, "`define WIDTH 8") {
		t.Errorf("live.vh content = %q", got)
	}
}

func TestCleanerCascadesThroughDeadIncludes(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"orphan.vh": "`ifndef ORPHAN\n`define ORPHAN\n`include \"leaf.vh\"\n// `define GONE 1\n`endif\n",
		"leaf.vh":   "`ifndef LEAF\n`define LEAF\n/* `ifdef OLD\n`define ALSO_GONE 1\n`endif */\n`endif\n",
		"live.vh":   "`ifndef LIVE\n`define LIVE\n`define WIDTH 8\n`endif\n",
		"top.sv":    "`include \"live.vh\"\nmodule top; endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/orphan.vh", ActionRemove)
	assertAction(t, result, "root/leaf.vh", ActionRemove)
	assertAction(t, result, "root/live.vh", ActionKeep)
	assertAction(t, result, "root/top.sv", ActionKeep)

	if result.Rounds < 2 {
		t.Errorf("rounds = %d, want at least 2", result.Rounds)
	}
	if got := roundOf(result, "root/leaf.vh"); got != 2 {
		t.Errorf("leaf.vh removed in round %d, want 2", got)
	}

	top := sink.written[filepath.Join("out", "top.sv")]
	if !strings.Contains(top, "`include \"live.vh\"") || !strings.Contains(top, "module top; endmodule") {
		t.Errorf("top.sv must stay intact, got %q", top)
	}
}

func TestCleanerDropsIfdefOfCommentedOutDefine(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"arch.svh": "`ifndef ARCH\n`define ARCH\n" +
			"// `define TRGT_FPGA_INTEL_MAX10\n" +
			"`define TRGT_SIMULATION\n`endif\n",
		"core.sv": "`include \"arch.svh\"\nmodule core;\n" +
			"`ifdef TRGT_FPGA_INTEL_MAX10\n  wire intel_only;\n`endif\n" +
			"`ifdef TRGT_SIMULATION\n  wire sim;\n`endif\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/core.sv", ActionEdit)

	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "TRGT_FPGA_INTEL_MAX10") || strings.Contains(core, "intel_only") {
		t.Errorf("dead ifdef left in core.sv: %q", core)
	}
	if !strings.Contains(core, "`ifdef TRGT_SIMULATION") || !strings.Contains(core, "wire sim;") {
		t.Errorf("live ifdef must stay in core.sv: %q", core)
	}

	arch := sink.written[filepath.Join("out", "arch.svh")]
	if strings.Contains(arch, "TRGT_FPGA_INTEL_MAX10") {
		t.Errorf("commented define left in arch.svh: %q", arch)
	}
}

func TestCleanerKeepsBranchesOfExternallyDefinedMacro(t *testing.T) {
	files := map[string]string{
		"core.sv": "module core;\n`ifdef SYNTHESIS\n  wire synth;\n`endif\nendmodule\n",
	}

	cleaner, sink := newTestCleaner(files)
	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{
		Out:               "out",
		ExternallyDefined: []string{"SYNTHESIS"},
	})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/core.sv", ActionKeep)
	if got := sink.written[filepath.Join("out", "core.sv")]; got != files["core.sv"] {
		t.Errorf("core.sv changed: %q", got)
	}
}

func TestCleanerCascadesDefinesInsideDeadBranch(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"cfg.svh": "`ifndef CFG\n`define CFG\n" +
			"`ifdef DEAD_ROOT\n`define INNER 1\n`endif\n" +
			"`define REAL 2\n`endif\n",
		"core.sv": "`include \"cfg.svh\"\nmodule core;\n" +
			"`ifdef INNER\n  wire inner;\n`endif\n" +
			"endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	if result.Rounds < 2 {
		t.Errorf("rounds = %d, want at least 2 (INNER dies with its enclosing branch)", result.Rounds)
	}

	core := sink.written[filepath.Join("out", "core.sv")]
	if strings.Contains(core, "INNER") || strings.Contains(core, "wire inner;") {
		t.Errorf("ifdef INNER must go away once its define is dropped: %q", core)
	}
	cfg := sink.written[filepath.Join("out", "cfg.svh")]
	if strings.Contains(cfg, "DEAD_ROOT") || strings.Contains(cfg, "INNER") {
		t.Errorf("dead branch left in cfg.svh: %q", cfg)
	}
	if !strings.Contains(cfg, "`define REAL 2") {
		t.Errorf("cfg.svh lost its live define: %q", cfg)
	}
}

func roundOf(result *CleanResult, path string) int {
	for _, o := range result.Outcomes {
		if o.Path == path {
			return o.Round
		}
	}
	return -1
}

func TestCleanerKeepsHeaderStillReferencedByInclude(t *testing.T) {
	cleaner, _ := newTestCleaner(map[string]string{
		"guard.vh": "`ifndef GUARD\n`define GUARD\n// `define UNUSED 1\n`endif\n",
		"top.sv":   "`include \"guard.vh\"\nmodule top; endmodule\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/guard.vh", ActionEdit)
}

func TestCleanerCopiesUnrelatedFiles(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"top.sv":     "module top; endmodule\n",
		"README.md":  "docs\n",
		"sub/x.json": "{}\n",
	})

	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"}); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	for _, want := range []string{"README.md", filepath.Join("sub", "x.json")} {
		if _, ok := sink.written[filepath.Join("out", want)]; !ok {
			t.Errorf("%s missing from the clean copy", want)
		}
	}
}

func TestCleanerDryRunWritesNothing(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{
		"dead.vh": "// `define GONE 1\n",
	})

	result, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out", DryRun: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}

	assertAction(t, result, "root/dead.vh", ActionRemove)
	if len(sink.written) != 0 || len(sink.dirs) != 0 {
		t.Errorf("dry-run wrote %d files and %d dirs", len(sink.written), len(sink.dirs))
	}
}

func TestCleanerRefusesExistingOutputWithoutForce(t *testing.T) {
	cleaner, sink := newTestCleaner(map[string]string{"top.sv": "module top; endmodule\n"})
	sink.exists["out"] = true

	_, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want an 'already exists' error", err)
	}

	sink.exists["out"] = true
	if _, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: "out", Force: true}); err != nil {
		t.Fatalf("Clean with force: %v", err)
	}
	if len(sink.removed) != 1 || sink.removed[0] != "out" {
		t.Errorf("removed = %v, want [out]", sink.removed)
	}
}

func TestCleanerRejectsOutputInsideRoot(t *testing.T) {
	cleaner, _ := newTestCleaner(map[string]string{"top.sv": "module top; endmodule\n"})

	_, err := cleaner.Clean(context.Background(), "root", CleanOptions{Out: filepath.Join("root", "clean")})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v, want an 'outside' error", err)
	}
}

func assertAction(t *testing.T, result *CleanResult, path string, want Action) {
	t.Helper()
	for _, o := range result.Outcomes {
		if o.Path != path {
			continue
		}
		if o.Action != want {
			t.Errorf("%s action = %s, want %s (reason %q, edits %+v)", path, o.Action, want, o.Reason, o.Edits)
		}
		return
	}
	t.Errorf("%s missing from outcomes", path)
}
