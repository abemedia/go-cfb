package cfb_test

import (
	"bytes"
	"io"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cfb"
)

// discardSeeker is an [io.WriteSeeker] that discards every byte. Used by
// write benchmarks to exclude the destination buffer's allocation cost
// so the timer measures the writer's own work.
type discardSeeker struct{ pos int64 }

func (d *discardSeeker) Write(p []byte) (int, error) {
	d.pos += int64(len(p))
	return len(p), nil
}

func (d *discardSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		d.pos = off
	case io.SeekCurrent:
		d.pos += off
	}
	return d.pos, nil
}

func benchData(size int) []byte {
	return bytes.Repeat([]byte("0123456789ABCDEF"), size/16+1)[:size]
}

func writeCFB(b *testing.B, size int) []byte {
	b.Helper()
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	s, err := w.CreateStream("payload")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := s.Write(benchData(size)); err != nil {
		b.Fatal(err)
	}
	if err := s.Close(); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	return buf.buf
}

func BenchmarkRead(b *testing.B) {
	for _, tt := range []struct {
		name string
		size int
	}{
		{"100B", 100}, // mini-stream
		{"100KB", 100 * 1024},
		{"10MB", 10 * 1024 * 1024},
	} {
		b.Run(tt.name, func(b *testing.B) {
			src := writeCFB(b, tt.size)
			b.SetBytes(int64(tt.size))
			for b.Loop() {
				r, err := cfb.NewReader(bytes.NewReader(src))
				if err != nil {
					b.Fatal(err)
				}
				s, err := r.OpenStream("payload")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, s.Open()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWrite(b *testing.B) {
	for _, tt := range []struct {
		name string
		size int
	}{
		{"100B", 100}, // mini-stream path with partial trailing sector
		{"100KB", 100 * 1024},
		{"10MB", 10 * 1024 * 1024},
	} {
		b.Run(tt.name, func(b *testing.B) {
			data := benchData(tt.size)
			b.SetBytes(int64(tt.size))
			for b.Loop() {
				w := cfb.NewWriterV3(&discardSeeker{})
				s, err := w.CreateStream("payload")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := s.Write(data); err != nil {
					b.Fatal(err)
				}
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAddStreams(b *testing.B) {
	// N=100 caps the per-iter checkDuplicate scan at a realistic
	// per-storage count, so the per-op number stays comparable
	// across runs instead of drifting with b.N.
	const n = 100
	names := make([]string, n)
	for i := range names {
		names[i] = strconv.Itoa(i)
	}
	for _, tt := range []struct {
		name string
		size int
	}{
		{"100B", 100},
		{"100KB", 100 * 1024},
		{"10MB", 10 * 1024 * 1024},
	} {
		b.Run(tt.name, func(b *testing.B) {
			data := benchData(tt.size)
			b.SetBytes(int64(tt.size) * n)
			for b.Loop() {
				w := cfb.NewWriterV3(&discardSeeker{})
				for _, name := range names {
					s, err := w.CreateStream(name)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := s.Write(data); err != nil {
						b.Fatal(err)
					}
					if err := s.Close(); err != nil {
						b.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAddFS(b *testing.B) {
	src := fstest.MapFS{
		"top":        &fstest.MapFile{Data: benchData(50 * 1024)},
		"Sub/inner1": &fstest.MapFile{Data: benchData(20 * 1024)},
		"Sub/inner2": &fstest.MapFile{Data: benchData(30 * 1024)},
	}
	b.SetBytes(int64((50 + 20 + 30) * 1024))
	for b.Loop() {
		w := cfb.NewWriterV3(&discardSeeker{})
		if err := w.AddFS(src); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
