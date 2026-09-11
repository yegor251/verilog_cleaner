package vpp

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

type stubFS struct {
	files   map[string]string
	readErr map[string]error
	walkErr error
}

func (s *stubFS) ReadFile(p string) ([]byte, error) {
	if err, ok := s.readErr[p]; ok {
		return nil, err
	}
	content, ok := s.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(content), nil
}

func (s *stubFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	if s.walkErr != nil {
		return s.walkErr
	}

	mapFS := fstest.MapFS{}
	for p, content := range s.files {
		mapFS[strings.TrimPrefix(p, root+"/")] = &fstest.MapFile{Data: []byte(content)}
	}
	return fs.WalkDir(mapFS, ".", func(p string, d fs.DirEntry, err error) error {
		if p == "." {
			return fn(root, d, err)
		}
		return fn(path.Join(root, p), d, err)
	})
}

func newStub(files map[string]string) *stubFS {
	return &stubFS{files: files, readErr: map[string]error{}}
}

func TestScannerScanDirFiltersExtensions(t *testing.T) {
	stub := newStub(withPrefix(map[string]string{
		"a.sv":       "`define A 1\n",
		"b.vh":       "// `define B 2\n",
		"notes.txt":  "`define C 3\n",
		"sub/c.v":    "`ifdef A\n`endif\n",
		"sub/d.svh":  "`include \"a.svh\"\n",
		"sub/e.json": "{}\n",
	}, "root"))

	scanner := NewScanner(stub, Options{Workers: 4})
	reports, err := scanner.ScanDir(context.Background(), "root")
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}

	var paths []string
	for _, r := range reports {
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)

	want := []string{"root/a.sv", "root/b.vh", "root/sub/c.v", "root/sub/d.svh"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestScannerScanDirIsSortedAndParsed(t *testing.T) {
	stub := newStub(withPrefix(map[string]string{
		"z.sv": "`define Z 1\n",
		"a.sv": "// `define A 1\n",
		"m.sv": "`ifndef M\n`define M\n`endif\n",
	}, "root"))

	scanner := NewScanner(stub, Options{Workers: 8})
	reports, err := scanner.ScanDir(context.Background(), "root")
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}

	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	if reports[0].Path != "root/a.sv" || reports[2].Path != "root/z.sv" {
		t.Errorf("reports not sorted: %s ... %s", reports[0].Path, reports[2].Path)
	}
	if !reports[0].Directives[0].Commented {
		t.Error("a.sv define must be commented")
	}
	if len(reports[1].Directives) != 3 {
		t.Errorf("m.sv directives = %d, want 3", len(reports[1].Directives))
	}
}

func TestScannerScanDirReportsReadError(t *testing.T) {
	stub := newStub(withPrefix(map[string]string{
		"ok.sv":  "`define OK 1\n",
		"bad.sv": "`define BAD 1\n",
	}, "root"))
	stub.readErr["root/bad.sv"] = errors.New("permission denied")

	scanner := NewScanner(stub, Options{Workers: 2})
	reports, err := scanner.ScanDir(context.Background(), "root")
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}

	var bad *FileReport
	for i := range reports {
		if reports[i].Path == "root/bad.sv" {
			bad = &reports[i]
		}
	}
	if bad == nil {
		t.Fatal("bad.sv missing from reports")
	}
	if bad.Err != "permission denied" {
		t.Errorf("err = %q, want %q", bad.Err, "permission denied")
	}
	if len(bad.Directives) != 0 {
		t.Errorf("failed file must have no directives, got %d", len(bad.Directives))
	}

	if s := BuildSummary(reports); s.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1", s.FilesFailed)
	}
}

func TestScannerScanDirPropagatesWalkError(t *testing.T) {
	stub := newStub(nil)
	stub.walkErr = errors.New("walk failed")

	scanner := NewScanner(stub, Options{})
	if _, err := scanner.ScanDir(context.Background(), "root"); err == nil {
		t.Fatal("want walk error, got nil")
	}
}

func TestScannerScanDirHonoursCancelledContext(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 2000; i++ {
		files[string(rune('a'+i%26))+itoa(i)+".sv"] = "`define X 1\n"
	}
	stub := newStub(withPrefix(files, "root"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scanner := NewScanner(stub, Options{Workers: 2})
	if _, err := scanner.ScanDir(ctx, "root"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func withPrefix(files map[string]string, prefix string) map[string]string {
	out := make(map[string]string, len(files))
	for p, content := range files {
		out[path.Join(prefix, p)] = content
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
