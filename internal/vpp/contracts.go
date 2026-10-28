package vpp

import "io/fs"

type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	Readlink(path string) (string, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

type FileSink interface {
	Exists(path string) (bool, error)
	MkdirAll(path string, perm fs.FileMode) error
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Symlink(oldname, newname string) error
	RemoveAll(path string) error
}
