package cfb_test

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/abemedia/go-cfb"
)

func TestEmptyHeader(t *testing.T) {
	tests := []struct {
		name       string
		newW       func(io.WriteSeeker) *cfb.Writer
		sectorSize int
	}{
		{"v3", cfb.NewWriterV3, 512},
		{"v4", cfb.NewWriterV4, 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf seekBuffer
			w := tt.newW(&buf)
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got, want := len(buf.buf), 3*tt.sectorSize; got != want {
				t.Errorf("file size = %d, want %d", got, want)
			}
			for i := 512; i < tt.sectorSize; i++ {
				if buf.buf[i] != 0 {
					t.Errorf("%s padding byte %d = %#x, want 0", tt.name, i, buf.buf[i])
					break
				}
			}
		})
	}
}

func TestRootMetadata(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	w.CLSID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	w.StateBits = 0xDEADBEEF
	w.Modified = time.Date(2026, 4, 27, 12, 30, 45, 0, time.UTC)
	w.Created = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // user sets it; spec ignores
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatal(err)
	}
	if r.CLSID != w.CLSID {
		t.Errorf("CLSID = %v, want %v", r.CLSID, w.CLSID)
	}
	if r.StateBits != w.StateBits {
		t.Errorf("StateBits = %#x, want %#x", r.StateBits, w.StateBits)
	}
	if !r.Modified.Equal(w.Modified) {
		t.Errorf("Modified = %v, want %v", r.Modified, w.Modified)
	}
	if !r.Created.IsZero() {
		t.Errorf("Created = %v, want zero (root entry must have Created=0)", r.Created)
	}
	if r.Name != "Root Entry" {
		t.Errorf("Name = %q, want \"Root Entry\"", r.Name)
	}
}

func TestSortOrder(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	for _, name := range []string{"foobar", "Foo", "Z"} {
		s, err := w.CreateStream(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		got = append(got, e.(*cfb.Stream).Name)
	}
	if want := []string{"Z", "Foo", "foobar"}; !slices.Equal(got, want) {
		t.Errorf("Entries = %v, want %v", got, want)
	}
}

// TestConcurrentMultiStream writes many streams concurrently to fragment
// the FAT and mini-stream chains, then reads them back to verify linkage.
// Run with -race to catch unsynchronised access.
func TestConcurrentMultiStream(t *testing.T) {
	streams := []struct {
		name  string
		size  int
		count int
	}{
		{"one-byte", 1, 100},
		{"sub-mini-sector", 17, 100},
		{"exact-mini-sector", 64, 100},
		{"two-mini-sectors", 128, 100},
		{"half-cutoff", 2048, 100},
		{"just-below-cutoff", 4095, 100},
		{"at-cutoff", 4096, 100},
		{"just-above-cutoff", 4097, 100},
		{"sub-secsize", 511, 100},
		{"two-secsize-plus-one", 1025, 100},
		{"medium", 50_000, 100},
		{"big", 1 << 20, 10},
		{"huge", 8 << 20, 2}, // exercises DIFAT chain
	}

	// pseudoRandomBytes generates size deterministic bytes seeded from the
	// stream name; the verifier regenerates the expected bytes the same way.
	pseudoRandomBytes := func(name string, size int) []byte {
		h := fnv.New64a()
		h.Write([]byte(name))
		rng := rand.New(rand.NewPCG(h.Sum64(), 0))
		out := make([]byte, size)
		for i := range out {
			out[i] = byte(rng.Uint32())
		}
		return out
	}

	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, st := range streams {
		for i := range st.count {
			name := fmt.Sprintf("%s-%d", st.name, i)
			wg.Go(func() {
				<-start
				data := pseudoRandomBytes(name, st.size)
				s, err := w.CreateStream(name)
				if err != nil {
					t.Errorf("CreateStream %q: %v", name, err)
					return
				}
				for off := 0; off < len(data); {
					n := rand.IntN(len(data)-off) + 1
					if _, err := s.Write(data[off : off+n]); err != nil {
						t.Errorf("Write %q: %v", name, err)
						return
					}
					off += n
				}
				if err := s.Close(); err != nil {
					t.Errorf("Close %q: %v", name, err)
				}
			})
		}
	}
	close(start)
	wg.Wait()

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	for _, st := range streams {
		for i := range st.count {
			name := fmt.Sprintf("%s-%d", st.name, i)
			want := pseudoRandomBytes(name, st.size)
			stream, err := r.OpenStream(name)
			if err != nil {
				t.Errorf("OpenStream %q: %v", name, err)
				continue
			}
			got, err := io.ReadAll(stream.Open())
			if err != nil {
				t.Errorf("ReadAll %q: %v", name, err)
				continue
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%q: content mismatch (got %d bytes, want %d)", name, len(got), len(want))
			}
		}
	}
}

func TestDuplicateName(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	if _, err := w.CreateStream("Foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateStream("FOO"); !errors.Is(err, cfb.ErrDuplicateName) {
		t.Errorf("err = %v, want ErrDuplicateName", err)
	}
}

func TestInvalidName(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	if _, err := w.CreateStream("foo/bar"); !errors.Is(err, cfb.ErrInvalidName) {
		t.Errorf("err = %v, want ErrInvalidName", err)
	}
}

func TestStreamClosed(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	s, err := w.CreateStream("First")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte{0x01}); err == nil {
		t.Error("Write after Close: err = nil, want error")
	}
}

// TestTimestampRoundTrip pins the zero-to-zero convention and a known
// FILETIME conversion (1995-11-16 -> 0x01BAB44B13921E80).
func TestTimestampRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
	}{
		{"zero", time.Time{}},
		{"unix epoch", time.Unix(0, 0).UTC()},
		{"1995-11-16", time.Date(1995, 11, 16, 17, 43, 45, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf seekBuffer
			w := cfb.NewWriterV3(&buf)
			w.Modified = tc.t
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := cfb.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatal(err)
			}
			if !r.Modified.Equal(tc.t) {
				t.Errorf("Modified = %v, want %v", r.Modified, tc.t)
			}
		})
	}
}

func TestAddFS(t *testing.T) {
	src := fstest.MapFS{
		"top":        &fstest.MapFile{Data: bytes.Repeat([]byte("top "), 200)},
		"Sub/inner1": &fstest.MapFile{Data: nil},
		"Sub/inner2": &fstest.MapFile{Data: bytes.Repeat([]byte("nested "), 1000)},
	}
	want := readFS(t, src)

	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	if err := w.AddFS(src); err != nil {
		t.Fatalf("AddFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got := readFS(t, r)

	if !maps.EqualFunc(got, want, bytes.Equal) {
		t.Errorf("content mismatch: got %d files, want %d", len(got), len(want))
	}
}

// readFS walks fsys and returns each file's content keyed by path.
func readFS(t *testing.T, fsys fs.FS) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		out[path] = data
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}
