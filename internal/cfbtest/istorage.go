//go:build windows

package cfbtest

import (
	"testing"

	"github.com/abemedia/go-cfb/internal/istorage"
)

func fromIStorage(t *testing.T, s *istorage.Storage) entry {
	t.Helper()
	info, err := s.Stat()
	if err != nil {
		t.Fatal(err)
	}
	out := entry{
		Name:      info.Name,
		Type:      entryTypeStorage,
		CLSID:     info.CLSID,
		StateBits: info.StateBits,
		Created:   info.Created.UTC(),
		Modified:  info.Modified.UTC(),
	}
	live, err := s.Entries()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range live {
		switch e.Type {
		case istorage.TypeStorage:
			sub, err := s.OpenStorage(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			out.Children = append(out.Children, fromIStorage(t, sub))
			sub.Close()
		case istorage.TypeStream:
			stm, err := s.OpenStream(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			out.Children = append(out.Children, entry{
				Name:        e.Name,
				Type:        entryTypeStream,
				StateBits:   e.StateBits,
				Size:        e.Size,
				ContentHash: hashStream(t, stm),
			})
			stm.Close()
		default:
			t.Fatalf("unknown entry type %d for %q", e.Type, e.Name)
		}
	}
	return out
}
