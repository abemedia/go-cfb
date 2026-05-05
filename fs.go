package cfb

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Open opens the named file in the CFB file using the semantics of [fs.FS.Open].
// Paths are always slash separated, with no leading / or ../ elements.
func (r *Reader) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return &fsDirHandle{storage: r.Storage}, nil
	}
	storage := r.Storage
	dir, base := splitPath(name)
	if dir != "" {
		for part := range strings.SplitSeq(dir, "/") {
			sub, ok := findEntry[*Storage](storage, part)
			if !ok {
				return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
			}
			storage = sub
		}
	}
	e, ok := findEntry[Entry](storage, base)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	switch v := e.(type) {
	case *Stream:
		return &fsFileHandle{stream: v, cursor: v.Open()}, nil
	case *Storage:
		return &fsDirHandle{storage: v}, nil
	}
	panic("unreachable")
}

// fileInfo implements [fs.FileInfo] and [fs.DirEntry].
type fileInfo struct {
	name    string
	size    int64
	isDir   bool
	modTime time.Time
	sys     any
}

func (fi fileInfo) Name() string { return fi.name }
func (fi fileInfo) Size() int64  { return fi.size }
func (fi fileInfo) Mode() fs.FileMode {
	if fi.isDir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (fi fileInfo) ModTime() time.Time         { return fi.modTime }
func (fi fileInfo) IsDir() bool                { return fi.isDir }
func (fi fileInfo) Sys() any                   { return fi.sys }
func (fi fileInfo) Type() fs.FileMode          { return fi.Mode().Type() }
func (fi fileInfo) Info() (fs.FileInfo, error) { return fi, nil }

func streamInfo(s *Stream) fileInfo {
	return fileInfo{name: path.Base(s.Name), size: s.Size, sys: s}
}

func storageInfo(s *Storage) fileInfo {
	return fileInfo{name: path.Base(s.Name), isDir: true, modTime: s.Modified, sys: s}
}

// fsFileHandle wraps a [*Stream] as an [fs.File].
type fsFileHandle struct {
	stream *Stream
	cursor io.Reader
}

func (h *fsFileHandle) Stat() (fs.FileInfo, error) { return streamInfo(h.stream), nil }
func (h *fsFileHandle) Read(p []byte) (int, error) { return h.cursor.Read(p) }
func (h *fsFileHandle) Close() error               { return nil }

// fsDirHandle wraps a [*Storage] as an [fs.ReadDirFile].
type fsDirHandle struct {
	storage *Storage
	pos     int
}

func (h *fsDirHandle) Stat() (fs.FileInfo, error) { return storageInfo(h.storage), nil }
func (h *fsDirHandle) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: h.storage.Name, Err: errors.New("is a directory")}
}
func (h *fsDirHandle) Close() error { return nil }

func (h *fsDirHandle) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := h.storage.Entries[h.pos:]
	take := len(remaining)
	if n > 0 {
		take = min(take, n)
		if take == 0 {
			return nil, io.EOF
		}
	}
	out := make([]fs.DirEntry, take)
	for i, e := range remaining[:take] {
		switch v := e.(type) {
		case *Stream:
			out[i] = streamInfo(v)
		case *Storage:
			out[i] = storageInfo(v)
		}
	}
	h.pos += take
	return out, nil
}

func splitPath(p string) (dir, base string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}
