package cfb

import (
	"io/fs"
	"syscall"
	"time"
)

// birthtime returns the creation time of the file described by info,
// falling back to [fs.FileInfo.ModTime] when no birth time is available.
func birthtime(info fs.FileInfo) time.Time {
	sys, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || sys.CreationTime.LowDateTime == 0 && sys.CreationTime.HighDateTime == 0 {
		return info.ModTime()
	}
	return time.Unix(0, sys.CreationTime.Nanoseconds()).UTC()
}
