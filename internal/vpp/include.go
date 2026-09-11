package vpp

import (
	"path"
	"path/filepath"
	"strings"
)

type includeIndex struct {
	byRelPath map[string]string
	byBase    map[string][]string
}

func newIncludeIndex(root string, paths []string) *includeIndex {
	idx := &includeIndex{
		byRelPath: make(map[string]string, len(paths)),
		byBase:    make(map[string][]string, len(paths)),
	}
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		idx.byRelPath[toSlash(rel)] = p
		base := path.Base(toSlash(p))
		idx.byBase[base] = append(idx.byBase[base], p)
	}
	return idx
}

func (idx *includeIndex) resolve(from, target string) []string {
	target = toSlash(strings.TrimSpace(target))
	if target == "" {
		return nil
	}

	local := path.Join(path.Dir(toSlash(from)), target)
	for rel, full := range idx.byRelPath {
		if full == local || rel == target {
			return []string{full}
		}
	}

	suffix := "/" + target
	var matches []string
	for _, full := range idx.byRelPath {
		if strings.HasSuffix(toSlash(full), suffix) {
			matches = append(matches, full)
		}
	}
	if len(matches) > 0 {
		return matches
	}

	return idx.byBase[path.Base(target)]
}

func toSlash(p string) string {
	return filepath.ToSlash(p)
}
