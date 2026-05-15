package cfbtest

import (
	"testing"

	"github.com/abemedia/go-cfb"
)

func fromReader(t *testing.T, s *cfb.Storage) entry {
	t.Helper()
	out := entry{
		Name:      s.Name,
		Type:      entryTypeStorage,
		CLSID:     s.CLSID,
		StateBits: s.StateBits,
		Created:   s.Created,
		Modified:  s.Modified,
	}
	for _, e := range s.Entries {
		switch e := e.(type) {
		case *cfb.Storage:
			out.Children = append(out.Children, fromReader(t, e))
		case *cfb.Stream:
			out.Children = append(out.Children, entry{
				Name:        e.Name,
				Type:        entryTypeStream,
				StateBits:   e.StateBits,
				Size:        e.Size,
				ContentHash: hashStream(t, e.Open()),
			})
		}
	}
	return out
}
