//go:build !windows

package cfbtest

import (
	"testing"

	"github.com/abemedia/go-cfb"
)

type storage interface {
	*cfb.Storage
}

func buildEntry[T storage](t *testing.T, src T) entry {
	t.Helper()
	return fromReader(t, src)
}
