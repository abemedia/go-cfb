package cfb_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cfb"
)

// FuzzReader feeds mutated CFBs to NewReader and asserts the parser never
// panics, hangs, or OOMs. Non-nil error returns are expected for malformed
// inputs and are not failures.
func FuzzReader(f *testing.F) {
	matches, err := filepath.Glob("testdata/*.cfb")
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		r, err := cfb.NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		var walk func(*cfb.Storage)
		walk = func(s *cfb.Storage) {
			for _, e := range s.Entries {
				switch v := e.(type) {
				case *cfb.Storage:
					walk(v)
				case *cfb.Stream:
					_, _ = io.Copy(io.Discard, v.Open())
				}
			}
		}
		walk(r.Storage)
	})
}
