package vpp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type OSFileSystem struct{}

func (OSFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (OSFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

func (OSFileSystem) Exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (OSFileSystem) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (OSFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}
