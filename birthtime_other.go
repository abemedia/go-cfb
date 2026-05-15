//go:build !darwin && !netbsd && !freebsd && !windows

package cfb

import (
	"io/fs"
	"time"
)

// birthtime returns the creation time of the file described by info,
// falling back to [fs.FileInfo.ModTime] when no birth time is available.
func birthtime(info fs.FileInfo) time.Time {
	return info.ModTime()
}
