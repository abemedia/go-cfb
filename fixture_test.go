//go:build windows

package cfb_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-cfb/internal/cfbtest"
	"github.com/abemedia/go-cfb/internal/istorage"
)

func loadFixtureNames(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures.json: %v", err)
	}
	var out []struct{ Name string }
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse fixtures.json: %v", err)
	}
	names := make([]string, len(out))
	for i, f := range out {
		names[i] = f.Name
	}
	return names
}

func TestReader(t *testing.T) {
	for _, name := range loadFixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			cfbPath := filepath.Join("testdata", name)

			want, err := istorage.Open(cfbPath)
			if err != nil {
				t.Fatalf("istorage.Open: %v", err)
			}
			defer want.Close()

			got, err := cfb.OpenReader(cfbPath)
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer got.Close()

			cfbtest.Equal(t, want, got.Storage)
		})
	}
}

func TestWriter(t *testing.T) {
	for _, name := range loadFixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			cfbPath := filepath.Join("testdata", name)

			rc, err := cfb.OpenReader(cfbPath)
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer rc.Close()

			tmp, err := os.CreateTemp(t.TempDir(), "test-*.cfb")
			if err != nil {
				t.Fatal(err)
			}
			defer tmp.Close()

			var w *cfb.Writer
			if rc.Version == 4 {
				w = cfb.NewWriterV4(tmp)
			} else {
				w = cfb.NewWriterV3(tmp)
			}
			w.CLSID = rc.CLSID
			w.StateBits = rc.StateBits
			w.Modified = rc.Modified
			if err := w.AddFS(rc); err != nil {
				t.Fatalf("AddFS: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := tmp.Close(); err != nil {
				t.Fatal(err)
			}

			want, err := istorage.Open(cfbPath)
			if err != nil {
				t.Fatalf("istorage.Open(%q): %v", cfbPath, err)
			}
			defer want.Close()

			got, err := istorage.Open(tmp.Name())
			if err != nil {
				t.Fatalf("istorage.Open(%q): %v", tmp.Name(), err)
			}
			defer got.Close()

			cfbtest.Equal(t, want, got)
		})
	}
}
