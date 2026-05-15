//go:build darwin || netbsd || freebsd

package cfb

import (
	"io/fs"
	"syscall"
	"time"
)

// birthtime returns the creation time of the file described by info,
// falling back to [fs.FileInfo.ModTime] when no birth time is available.
func birthtime(info fs.FileInfo) time.Time {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || sys.Birthtimespec.Sec == 0 && sys.Birthtimespec.Nsec == 0 {
		return info.ModTime()
	}
	return time.Unix(sys.Birthtimespec.Sec, sys.Birthtimespec.Nsec).UTC()
}
