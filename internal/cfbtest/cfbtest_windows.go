//go:build windows

package cfbtest

import (
	"testing"
	"time"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-cfb/internal/structuredstorage"
)

type storage interface {
	*cfb.Storage | *structuredstorage.Storage
}

func buildEntry[T storage](t *testing.T, src T) entry {
	t.Helper()
	switch s := any(src).(type) {
	case *cfb.Storage:
		return fromReader(t, s)
	case *structuredstorage.Storage:
		// IStorage::Stat synthesises root Name/Created/Modified from the
		// filesystem. Patch to match cfb.Reader's dir-entry view.
		out := fromStructuredStorage(t, s)
		out.Name = "Root Entry"
		out.Created = time.Time{}
		out.Modified = time.Time{}
		return out
	}
	panic("unreachable")
}
