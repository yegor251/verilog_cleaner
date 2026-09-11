package vpp

import (
	"context"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

var DefaultExtensions = []string{".v", ".vh", ".sv", ".svh", ".svi", ".vp", ".svp"}

var DefaultSkipDirs = []string{".git", ".idea", "node_modules", "vendor"}

type Options struct {
	Extensions []string
	SkipDirs   []string
	Workers    int
	KeepSource bool
}

type Scanner struct {
	fs         FileSystem
	exts       map[string]struct{}
	skipDirs   map[string]struct{}
	workers    int
	keepSource bool
}

func NewScanner(filesystem FileSystem, opts Options) *Scanner {
	exts := opts.Extensions
	if len(exts) == 0 {
		exts = DefaultExtensions
	}
	skip := opts.SkipDirs
	if skip == nil {
		skip = DefaultSkipDirs
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	s := &Scanner{
		fs:         filesystem,
		exts:       make(map[string]struct{}, len(exts)),
		skipDirs:   make(map[string]struct{}, len(skip)),
		workers:    workers,
		keepSource: opts.KeepSource,
	}
	for _, e := range exts {
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		s.exts[strings.ToLower(e)] = struct{}{}
	}
	for _, d := range skip {
		s.skipDirs[d] = struct{}{}
	}
	return s
}

func (s *Scanner) ScanDir(ctx context.Context, root string) ([]FileReport, error) {
	paths := make(chan string, 512)

	var walkErr error
	go func() {
		defer close(paths)
		walkErr = s.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if _, skip := s.skipDirs[d.Name()]; skip {
					return fs.SkipDir
				}
				return nil
			}
			if !s.matches(path) {
				return nil
			}
			select {
			case paths <- path:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()

	reports := s.scanParallel(ctx, paths)
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })

	if walkErr != nil {
		return reports, walkErr
	}
	return reports, ctx.Err()
}

// scanParallel гоняет s.workers воркеров над каналом путей: каждый файл
// разбирается в своей горутине, результаты собираются по каналу.
func (s *Scanner) scanParallel(ctx context.Context, paths <-chan string) []FileReport {
	results := make(chan FileReport, 512)
	var wg sync.WaitGroup
	wg.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go func() {
			defer wg.Done()
			for path := range paths {
				results <- s.scanFile(path)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	reports := make([]FileReport, 0, 64)
	for r := range results {
		reports = append(reports, r)
	}
	return reports
}

func (s *Scanner) scanFile(path string) FileReport {
	src, err := s.fs.ReadFile(path)
	if err != nil {
		return FileReport{Path: path, Err: err.Error()}
	}
	report := ScanSource(path, src)
	if s.keepSource {
		report.Source = src
	}
	return report
}

// ScanFiles сканирует явный список файлов вместо обхода дерева. root — базовая
// папка, files — пути относительно неё (та же раскладка, что у сканера папок).
// Список файлов разбирается параллельно пулом воркеров.
func (s *Scanner) ScanFiles(ctx context.Context, root string, files []string) ([]FileReport, error) {
	// Сначала подготовим и отфильтруем пути (дёшево, без разбора).
	resolved := make([]string, 0, len(files))
	for _, rel := range files {
		p := filepath.Join(root, rel)
		if s.matches(p) {
			resolved = append(resolved, p)
		}
	}

	paths := make(chan string, 512)
	go func() {
		defer close(paths)
		for _, p := range resolved {
			select {
			case paths <- p:
			case <-ctx.Done():
				return
			}
		}
	}()

	reports := s.scanParallel(ctx, paths)
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })

	if err := ctx.Err(); err != nil {
		return reports, err
	}
	return reports, nil
}

func (s *Scanner) matches(path string) bool {
	_, ok := s.exts[strings.ToLower(filepath.Ext(path))]
	return ok
}
