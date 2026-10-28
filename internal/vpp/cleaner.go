package vpp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

const (
	dirPerm  fs.FileMode = 0o755
	filePerm fs.FileMode = 0o644
)

type Action uint8

const (
	ActionKeep Action = iota
	ActionEdit
	ActionRemove
)

func (a Action) String() string {
	switch a {
	case ActionEdit:
		return "edited"
	case ActionRemove:
		return "removed"
	default:
		return "kept"
	}
}

type FileOutcome struct {
	Path    string `json:"path"`
	Action  Action `json:"action"`
	Edits   []Edit `json:"edits,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Guard   string `json:"guard,omitempty"`
	Round   int    `json:"round,omitempty"`
	SizeIn  int    `json:"size_in"`
	SizeOut int    `json:"size_out"`
}

type CleanResult struct {
	Root     string        `json:"root"`
	Out      string        `json:"out"`
	Rounds   int           `json:"rounds"`
	Outcomes []FileOutcome `json:"outcomes"`
	Warnings []string      `json:"warnings,omitempty"`
	DryRun   bool          `json:"dry_run"`
}

func (r *CleanResult) Removed() []FileOutcome { return r.filter(ActionRemove) }
func (r *CleanResult) Edited() []FileOutcome  { return r.filter(ActionEdit) }

func (r *CleanResult) filter(a Action) []FileOutcome {
	out := make([]FileOutcome, 0, len(r.Outcomes))
	for _, o := range r.Outcomes {
		if o.Action == a {
			out = append(out, o)
		}
	}
	return out
}

type CleanOptions struct {
	Out               string
	Force             bool
	DryRun            bool
	ExternallyDefined []string
	Protected         []string
	Files             []string
}

type Cleaner struct {
	fs      FileSystem
	sink    FileSink
	scanner *Scanner
}

func NewCleaner(filesystem FileSystem, sink FileSink, scanner *Scanner) *Cleaner {
	return &Cleaner{fs: filesystem, sink: sink, scanner: scanner}
}

type fileState struct {
	path    string
	orig    []byte
	cur     []byte
	base    FileReport
	report  FileReport
	edits   []Edit
	removed bool
	reason  string
	guard   string
	round   int
}

func (st *fileState) recompute() {
	st.edits = normalizeEdits(st.orig, st.edits)
	st.cur = ApplyEdits(st.orig, st.edits)
	st.report = ScanSource(st.path, st.cur)
}

func (st *fileState) covered(offset int) bool {
	for _, e := range st.edits {
		if offset >= e.Start && offset < e.End && e.Replacement == "" {
			return true
		}
	}
	return false
}

func (st *fileState) includeTargets() []string {
	out := make([]string, 0, 4)
	for _, d := range st.report.Directives {
		if d.Commented || d.Kind != KindInclude || d.Name == "" {
			continue
		}
		out = append(out, d.Name)
	}
	return out
}

// Clean — главная точка очистки: сканирует вход, гоняет каскад раундов,
// чистит макросы и выдаёт результат (при не-dry-run ещё и пишет копию).
func (c *Cleaner) Clean(ctx context.Context, root string, opts CleanOptions) (*CleanResult, error) {
	if opts.Out == "" {
		return nil, errors.New("clean: output directory is required")
	}
	if err := c.validateOut(root, opts); err != nil {
		return nil, err
	}

	var reports []FileReport
	var err error
	if len(opts.Files) > 0 {
		// В .files-режиме раскрываем include-замыкание: неподключённые в
		// список заголовки несут определения макросов, без них живые ветки
		// ошибочно считаются мёртвыми.
		opts.Files, err = c.includeClosure(root, opts.Files)
		if err != nil {
			return nil, err
		}
		reports, err = c.scanner.ScanFiles(ctx, root, opts.Files)
	} else {
		reports, err = c.scanner.ScanDir(ctx, root)
	}
	if err != nil {
		return nil, err
	}

	protected := toBoolSet(opts.Protected)
	for _, name := range ToolDefines {
		protected[name] = true
	}
	states, paths, err := buildStates(reports, protected)
	if err != nil {
		return nil, err
	}

	idx := newIncludeIndex(root, paths)
	rounds := c.cascade(states, paths, idx, opts.ExternallyDefined, protected)
	c.scrubRemovedMacros(states, paths, protected)

	result := &CleanResult{Root: root, Out: opts.Out, Rounds: rounds, DryRun: opts.DryRun}
	for _, p := range paths {
		st := states[p]
		o := FileOutcome{
			Path:    p,
			Edits:   st.edits,
			Reason:  st.reason,
			Guard:   st.guard,
			Round:   st.round,
			SizeIn:  len(st.orig),
			SizeOut: len(st.cur),
		}
		switch {
		case st.removed:
			o.Action = ActionRemove
			o.SizeOut = 0
		case len(st.edits) > 0:
			o.Action = ActionEdit
		default:
			o.Action = ActionKeep
		}
		result.Outcomes = append(result.Outcomes, o)
	}

	if opts.DryRun {
		return result, nil
	}
	if len(opts.Files) > 0 {
		if err := c.materializeFiles(root, opts, states); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := c.materialize(root, opts, states, &result.Warnings); err != nil {
		return result, err
	}
	return result, nil
}

// buildStates превращает отчёты сканирования в состояния файлов и сразу
// планирует удаление закомментированных директив.
func buildStates(reports []FileReport, protected map[string]bool) (map[string]*fileState, []string, error) {
	states := make(map[string]*fileState, len(reports))
	paths := make([]string, 0, len(reports))
	for _, r := range reports {
		if r.Err != "" {
			return nil, nil, fmt.Errorf("clean: read %s: %s", r.Path, r.Err)
		}
		st := &fileState{path: r.Path, orig: r.Source, cur: r.Source, base: r, report: r}
		st.edits = planCommentedDirectives(r, r.Source, protected)
		if len(st.edits) > 0 {
			st.recompute()
		}
		states[r.Path] = st
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)
	return states, paths, nil
}

// cascade повторяет раунды, пока что-то меняется: сначала режет мёртвые
// ветки, затем удаляет ставшие ненужными файлы.
func (c *Cleaner) cascade(states map[string]*fileState, paths []string, idx *includeIndex, external []string, protected map[string]bool) int {
	everReferenced := collectEverReferenced(states, paths, idx)
	round := 0
	for {
		round++
		changed := c.pruneDeadBranches(states, paths, external, protected, round)
		if c.removeUselessFiles(states, paths, idx, everReferenced, round) {
			changed = true
		}
		if !changed {
			return round - 1
		}
	}
}

// includeClosure дополняет список .files транзитивным замыканием `include:
// для каждого перечисленного файла находит цели его включений по всему
// дереву и добавляет их к списку, пока не перестанут появляться новые файлы.
func (c *Cleaner) includeClosure(root string, files []string) ([]string, error) {
	var all []string
	err := c.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := c.scanner.skipDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}
		if c.scanner.matches(path) {
			all = append(all, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx := newIncludeIndex(root, all)

	listed := make(map[string]bool, len(files))
	seen := make(map[string]bool, len(files))
	queue := make([]string, 0, len(files))
	for _, rel := range files {
		p := filepath.Join(root, rel)
		listed[p] = true
		if !seen[p] {
			seen[p] = true
			queue = append(queue, p)
		}
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		src, err := c.fs.ReadFile(p)
		if err != nil {
			// недоступный файл из списка — ошибка всплывёт при ScanFiles
			continue
		}
		for _, d := range ScanSource(p, src).Directives {
			if d.Commented || d.Kind != KindInclude || d.Name == "" {
				continue
			}
			for _, resolved := range idx.resolve(p, d.Name) {
				if resolved != p && !seen[resolved] {
					seen[resolved] = true
					queue = append(queue, resolved)
				}
			}
		}
	}

	out := make([]string, 0, len(seen))
	out = append(out, files...)
	for p := range seen {
		if !listed[p] {
			rel, err := filepath.Rel(root, p)
			if err == nil {
				out = append(out, rel)
			}
		}
	}
	return out, nil
}

// collectEverReferenced находит файлы, на которые в исходном коде ссылались
// через `include: это отличает осиротевшие заголовки от модулей верхнего
// уровня, которые никто никогда не включал.
func collectEverReferenced(states map[string]*fileState, paths []string, idx *includeIndex) map[string]bool {
	ever := make(map[string]bool)
	for _, p := range paths {
		for _, d := range states[p].base.Directives {
			if d.Commented || d.Kind != KindInclude || d.Name == "" {
				continue
			}
			for _, resolved := range idx.resolve(p, d.Name) {
				if resolved != p {
					ever[resolved] = true
				}
			}
		}
	}
	return ever
}

func toBoolSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	return set
}

// ToolDefines — макросы, которые определяют сами инструменты (симуляторы и
// синтезаторы, например Verilator задаёт `VERILATOR). В дереве их
// определения нет, но ветки `ifdef по ним мёртвыми считать нельзя.
var ToolDefines = []string{
	"VERILATOR", "YOSYS", "SYNTHESIS", "SIMULATION", "VCS", "VCSMX",
	"QUESTA", "MODELSIM", "XCELIUM", "IUS", "IVERILOG",
}

// pruneDeadBranches вырезает из каждого файла ветки ifdef, чьи макросы
// не определены, и отмечает номер раунда первой правки файла. Условия
// вычисляются пофайлово: собственные define/undef/undefineall файла — по
// позиции в тексте, макросы из других файлов, внешние и защищённые —
// консервативно считаются определёнными.
func (c *Cleaner) pruneDeadBranches(states map[string]*fileState, paths []string, external []string, protected map[string]bool, round int) bool {
	definedIn := make(map[string]map[string]bool)
	for _, p := range paths {
		st := states[p]
		if st.removed {
			continue
		}
		for _, d := range st.base.Directives {
			if d.Commented || d.Kind != KindDefine || d.Name == "" {
				continue
			}
			if st.covered(d.Offset) {
				continue
			}
			if definedIn[d.Name] == nil {
				definedIn[d.Name] = make(map[string]bool, 1)
			}
			definedIn[d.Name][p] = true
		}
	}

	changed := false
	for _, p := range paths {
		st := states[p]
		if st.removed {
			continue
		}

		env := CondEnv{Elsewhere: make(map[string]bool, len(definedIn)+len(external)+len(protected))}
		for name, files := range definedIn {
			if !files[p] {
				env.Elsewhere[name] = true
			}
		}
		for _, name := range external {
			env.Elsewhere[name] = true
		}
		for name := range protected {
			env.Elsewhere[name] = true
		}

		planned := PlanDeadBranches(st.base, st.orig, env, st.covered)
		fresh := make([]Edit, 0, len(planned))
		for _, e := range planned {
			if st.covered(e.Start) && st.covered(e.End-1) {
				continue
			}
			fresh = append(fresh, e)
		}
		if len(fresh) == 0 {
			continue
		}

		st.edits = append(st.edits, fresh...)
		st.recompute()
		if st.round == 0 {
			st.round = round
		}
		changed = true
	}
	return changed
}

// countIncoming считает, сколько живых файлов включают каждый файл.
func countIncoming(states map[string]*fileState, paths []string, idx *includeIndex) map[string]int {
	incoming := make(map[string]int, len(paths))
	for _, p := range paths {
		st := states[p]
		if st.removed {
			continue
		}
		seen := make(map[string]bool, 4)
		for _, target := range st.includeTargets() {
			for _, resolved := range idx.resolve(p, target) {
				dep := states[resolved]
				if dep == nil || dep.removed || resolved == p || seen[resolved] {
					continue
				}
				seen[resolved] = true
				incoming[resolved]++
			}
		}
	}
	return incoming
}

// removeUselessFiles удаляет файлы, которые больше никто не включает: пустые
// либо осиротевшие из-за удалённого `include из define-ветки.
func (c *Cleaner) removeUselessFiles(states map[string]*fileState, paths []string, idx *includeIndex, everReferenced map[string]bool, round int) bool {
	incoming := countIncoming(states, paths, idx)

	removed := false
	for _, p := range paths {
		st := states[p]
		if st.removed || incoming[p] > 0 {
			continue
		}
		e := AnalyzeEmptiness(st.report, st.cur)
		st.guard = e.Guard
		orphaned := everReferenced[p]
		if !e.Empty && !orphaned {
			continue
		}
		st.removed = true
		st.round = round
		if e.Empty {
			st.reason = "no active defines left and nothing includes it"
		} else {
			st.reason = "only referenced via a removed define branch"
		}
		removed = true
	}
	return removed
}

// validateOut проверяет, что выход лежит вне сканируемой папки и его либо
// нет, либо разрешено перезаписать (force).
func (c *Cleaner) validateOut(root string, opts CleanOptions) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(opts.Out)
	if err != nil {
		return err
	}
	if absRoot == absOut {
		return errors.New("clean: output must differ from the scanned directory")
	}
	rel, err := filepath.Rel(absRoot, absOut)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("clean: output %s must live outside %s", opts.Out, root)
	}

	if opts.DryRun {
		return nil
	}
	exists, err := c.sink.Exists(opts.Out)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !opts.Force {
		return fmt.Errorf("clean: %s already exists (use force to overwrite)", opts.Out)
	}
	return c.sink.RemoveAll(opts.Out)
}

// materializeFiles (режим .files) записывает только перечисленные файлы,
// повторяя их относительные пути, и не копирует остальное дерево.
func (c *Cleaner) materializeFiles(root string, opts CleanOptions, states map[string]*fileState) error {
	if err := c.sink.MkdirAll(opts.Out, dirPerm); err != nil {
		return err
	}
	for _, rel := range opts.Files {
		st := states[filepath.Join(root, rel)]
		if st == nil || st.removed {
			continue
		}
		target := filepath.Join(opts.Out, rel)
		if err := c.sink.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return err
		}
		if err := c.sink.WriteFile(target, st.cur, filePerm); err != nil {
			return err
		}
	}
	return nil
}

// materialize копирует всё дерево в выходную папку, подменяя очищенные файлы
// и пропуская удалённые. Нечитаемые файлы и битые symlink'и не прерывают
// очистку — они пропускаются с предупреждением.
func (c *Cleaner) materialize(root string, opts CleanOptions, states map[string]*fileState, warnings *[]string) error {
	if err := c.sink.MkdirAll(opts.Out, dirPerm); err != nil {
		return err
	}

	return c.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("skipped %s: %v", path, err))
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(opts.Out, rel)

		if d.IsDir() {
			if _, skip := c.scanner.skipDirs[d.Name()]; skip && rel != "." {
				return fs.SkipDir
			}
			return c.sink.MkdirAll(target, dirPerm)
		}

		perm := filePerm
		if info, infoErr := d.Info(); infoErr == nil {
			perm = info.Mode().Perm()
		}

		if st, ok := states[path]; ok {
			if st.removed {
				return nil
			}
			return c.sink.WriteFile(target, st.cur, perm)
		}

		if d.Type()&fs.ModeSymlink != 0 {
			return c.copySymlink(path, target, warnings)
		}
		if !d.Type().IsRegular() {
			*warnings = append(*warnings, fmt.Sprintf("skipped non-regular file %s", path))
			return nil
		}

		data, readErr := c.fs.ReadFile(path)
		if readErr != nil {
			*warnings = append(*warnings, fmt.Sprintf("skipped %s: %v", path, readErr))
			return nil
		}
		return c.sink.WriteFile(target, data, perm)
	})
}

// copySymlink воспроизводит символическую ссылку как есть; если создать её
// не удалось — пропускает с предупреждением.
func (c *Cleaner) copySymlink(path, target string, warnings *[]string) error {
	oldname, err := c.fs.Readlink(path)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("skipped symlink %s: %v", path, err))
		return nil
	}
	if err := c.sink.Symlink(oldname, target); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("skipped symlink %s: %v", path, err))
	}
	return nil
}
