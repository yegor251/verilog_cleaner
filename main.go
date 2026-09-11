package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"pocp/internal/vpp"
)

type config struct {
	input         string
	exts          string
	workers       int
	asJSON        bool
	commentedOnly bool
	activeOnly    bool
	kinds         string
	noSummary     bool
	clean         string
	force         bool
	dryRun        bool
	defined       string
}

func main() {
	cfg := parseFlags()

	if cfg.clean != "" || cfg.dryRun {
		if err := runClean(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}

	scanner := vpp.NewScanner(vpp.OSFileSystem{}, vpp.Options{
		Extensions: splitList(cfg.exts),
		Workers:    cfg.workers,
	})

	start := time.Now()
	_, reports, err := loadReports(context.Background(), scanner, cfg.input)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan %s: %v\n", cfg.input, err)
		os.Exit(1)
	}

	reports = filterReports(reports, cfg)
	summary := vpp.BuildSummary(reports)

	if cfg.asJSON {
		if err := printJSON(reports, summary, elapsed); err != nil {
			fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
			os.Exit(1)
		}
		return
	}

	printText(reports, summary, elapsed, cfg)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.input, "dir", ".", "input: a directory or a .files file to scan/clean")
	flag.StringVar(&cfg.exts, "ext", strings.Join(vpp.DefaultExtensions, ","), "comma-separated file extensions")
	flag.IntVar(&cfg.workers, "workers", 0, "number of parallel workers (0 = NumCPU)")
	flag.BoolVar(&cfg.asJSON, "json", false, "emit JSON instead of text")
	flag.BoolVar(&cfg.commentedOnly, "commented", false, "report only commented-out directives")
	flag.BoolVar(&cfg.activeOnly, "active", false, "report only active directives")
	flag.StringVar(&cfg.kinds, "kind", "", "comma-separated kinds to keep (define,ifdef,usage,...)")
	flag.BoolVar(&cfg.noSummary, "no-summary", false, "skip the aggregated summary")
	flag.StringVar(&cfg.clean, "clean", "", "write a cleaned copy to this directory")
	flag.BoolVar(&cfg.force, "force", false, "overwrite the clean output directory if it exists")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "report what -clean would change without writing anything")
	flag.StringVar(&cfg.defined, "defined", "", "comma-separated macros defined outside the tree (build flags)")
	flag.Parse()

	if args := flag.Args(); len(args) > 0 {
		cfg.input = args[0]
	}
	return cfg
}

// runClean cleans either a directory tree or the files listed in a .files file.
func runClean(cfg config) error {
	info, err := os.Stat(cfg.input)
	if err != nil {
		return err
	}

	root := cfg.input
	var fileList []string
	if !info.IsDir() {
		fileList, err = readFileList(cfg.input)
		if err != nil {
			return err
		}
		root = filepath.Dir(cfg.input)
	}

	out := cfg.clean
	if out == "" {
		out = strings.TrimRight(root, string(os.PathSeparator)) + "-clean"
	}

	filesystem := vpp.OSFileSystem{}
	scanner := vpp.NewScanner(filesystem, vpp.Options{
		Extensions: splitList(cfg.exts),
		Workers:    cfg.workers,
		KeepSource: true,
	})
	cleaner := vpp.NewCleaner(filesystem, filesystem, scanner)

	start := time.Now()
	result, err := cleaner.Clean(context.Background(), root, vpp.CleanOptions{
		Out:               out,
		Force:             cfg.force,
		DryRun:            cfg.dryRun,
		ExternallyDefined: splitList(cfg.defined),
		Protected:         ProtectedDefines,
		Files:             fileList,
	})
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	if cfg.asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	printClean(result, elapsed)
	return nil
}

// loadReports scans the input path: a directory is walked, a .files file is
// read and its listed files (relative to the .files location) are scanned.
func loadReports(ctx context.Context, scanner *vpp.Scanner, input string) (string, []vpp.FileReport, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		reports, err := scanner.ScanDir(ctx, input)
		return input, reports, err
	}
	entries, err := readFileList(input)
	if err != nil {
		return "", nil, err
	}
	root := filepath.Dir(input)
	reports, err := scanner.ScanFiles(ctx, root, entries)
	return root, reports, err
}

// readFileList reads a .files file: one file path per line, resolved relative
// to the .files file's directory. Blank lines and # comments are ignored.
func readFileList(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func printClean(result *vpp.CleanResult, elapsed time.Duration) {
	mode := "clean"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("%s: %s -> %s (%d cascade rounds, %.2fms)\n\n",
		mode, result.Root, result.Out, result.Rounds, float64(elapsed.Microseconds())/1000)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, o := range result.Outcomes {
		if o.Action == vpp.ActionKeep {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d -> %d bytes\t%s\n", o.Path, o.Action, o.SizeIn, o.SizeOut, o.Reason)
		for _, e := range o.Edits {
			fmt.Fprintf(w, "  line %d\t%s\t%d line(s)\t%s\n", e.Line, e.Reason, e.Lines, e.Detail)
		}
	}
	w.Flush()

	removed := result.Removed()
	edited := result.Edited()
	fmt.Printf("\nfiles: %d  edited: %d  removed: %d\n", len(result.Outcomes), len(edited), len(removed))
	if len(removed) > 0 {
		names := make([]string, 0, len(removed))
		for _, o := range removed {
			names = append(names, o.Path)
		}
		fmt.Printf("removed: %s\n", strings.Join(names, ", "))
	}
	if result.DryRun {
		fmt.Printf("\nnothing written (dry-run)\n")
	}
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func filterReports(reports []vpp.FileReport, cfg config) []vpp.FileReport {
	if !cfg.commentedOnly && !cfg.activeOnly && cfg.kinds == "" {
		return reports
	}

	var keepKinds map[string]struct{}
	if cfg.kinds != "" {
		keepKinds = make(map[string]struct{})
		for _, k := range splitList(cfg.kinds) {
			keepKinds[strings.ToLower(k)] = struct{}{}
		}
	}

	out := make([]vpp.FileReport, 0, len(reports))
	for _, r := range reports {
		kept := make([]vpp.Directive, 0, len(r.Directives))
		for _, d := range r.Directives {
			if cfg.commentedOnly && !d.Commented {
				continue
			}
			if cfg.activeOnly && d.Commented {
				continue
			}
			if keepKinds != nil {
				if _, ok := keepKinds[d.Kind.String()]; !ok {
					continue
				}
			}
			kept = append(kept, d)
		}
		r.Directives = kept
		out = append(out, r)
	}
	return out
}

type jsonOutput struct {
	DurationMS float64          `json:"duration_ms"`
	Summary    *vpp.Summary     `json:"summary"`
	Files      []vpp.FileReport `json:"files"`
}

func printJSON(reports []vpp.FileReport, summary *vpp.Summary, elapsed time.Duration) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonOutput{
		DurationMS: float64(elapsed.Microseconds()) / 1000,
		Summary:    summary,
		Files:      reports,
	})
}

func printText(reports []vpp.FileReport, summary *vpp.Summary, elapsed time.Duration, cfg config) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	for _, r := range reports {
		if r.Err != "" {
			fmt.Fprintf(w, "%s\tERROR: %s\n", r.Path, r.Err)
			continue
		}
		if len(r.Directives) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", r.Path)
		for _, d := range r.Directives {
			fmt.Fprintf(w, "  %d:%d\t%s\t%s\t%s\t%s\n",
				d.Line, d.Column,
				status(d),
				d.Kind,
				nameColumn(d),
				detail(d),
			)
		}
	}
	w.Flush()

	if cfg.noSummary {
		return
	}

	fmt.Printf("\n── summary ──────────────────────────────\n")
	fmt.Printf("files: %d (failed %d)  directives: %d  active: %d  commented: %d  in %.2fms\n",
		summary.Files, summary.FilesFailed, summary.Total, summary.Active, summary.Commented,
		float64(elapsed.Microseconds())/1000)

	sw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(sw, "\nkind\tactive\tcommented\n")
	for _, kind := range []string{"define", "undef", "undefineall", "ifdef", "ifndef", "elsif", "else", "endif", "include", "usage", "timescale", "pragma", "line", "keywords", "other"} {
		c, ok := summary.ByKind[kind]
		if !ok {
			continue
		}
		fmt.Fprintf(sw, "%s\t%d\t%d\n", kind, c.Active, c.Commented)
	}

	fmt.Fprintf(sw, "\nmacro\tdefs\tcommented defs\tusages\tconditions\n")
	for _, name := range summary.MacroNames() {
		m := summary.Macros[name]
		commentedDefs := 0
		for _, d := range m.Definitions {
			if d.Commented {
				commentedDefs++
			}
		}
		fmt.Fprintf(sw, "%s\t%d\t%d\t%d\t%d\n",
			name, len(m.Definitions)-commentedDefs, commentedDefs, len(m.Usages), len(m.Conditions))
	}
	sw.Flush()

	printList("used but never defined (active)", summary.UndefinedUsed)
	printList("defined only inside comments", summary.OnlyCommented)
	printList("defined but never referenced", summary.DefinedUnused)
	printList("referenced only by ifdef/ifndef/elsif", summary.ConditionalOnly)
}

func printList(title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Printf("\n%s: %s\n", title, strings.Join(items, ", "))
}

func status(d vpp.Directive) string {
	if !d.Commented {
		return "active"
	}
	return "commented(" + d.CommentKind.String() + ")"
}

func nameColumn(d vpp.Directive) string {
	if d.Name != "" {
		return d.Name
	}
	return "-"
}

func detail(d vpp.Directive) string {
	var parts []string
	if len(d.Params) > 0 {
		parts = append(parts, "("+strings.Join(d.Params, ", ")+")")
	}
	if d.Body != "" {
		body := strings.ReplaceAll(d.Body, "\n", " ⏎ ")
		if len(body) > 60 {
			body = body[:57] + "..."
		}
		parts = append(parts, "= "+body)
	}
	if d.InMacroBody {
		parts = append(parts, "[in macro body]")
	}
	return strings.Join(parts, " ")
}
