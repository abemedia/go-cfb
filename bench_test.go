package cfb_test

import (
	"bytes"
	"fmt"
	"io"
	"testing"

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

type stream struct {
	name string
	data []byte
}

func fixture() ([]stream, int64) {
	buf := bytes.Repeat([]byte("0123456789ABCDEF"), 5*1024*1024/16+1)
	small, large := buf[:2*1024], buf[:5*1024*1024]
	out := make([]stream, 0, 53)
	var total int64
	for range 50 {
		out = append(out, stream{fmt.Sprintf("%05d", len(out)), small})
		total += int64(len(small))
	}
	for range 3 {
		out = append(out, stream{fmt.Sprintf("%05d", len(out)), large})
		total += int64(len(large))
	}
	return out, total
}

func BenchmarkReader(b *testing.B) {
	streams, total := fixture()
	b.SetBytes(total)
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	for _, s := range streams {
		sw, err := w.CreateStream(s.name)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := sw.Write(s.data); err != nil {
			b.Fatal(err)
		}
		if err := sw.Close(); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	rd := bytes.NewReader(buf.buf)
	for b.Loop() {
		r, err := cfb.NewReader(rd)
		if err != nil {
			b.Fatal(err)
		}
		for _, st := range streams {
			s, err := r.OpenStream(st.name)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, s.Open()); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkWriter(b *testing.B) {
	streams, total := fixture()
	b.SetBytes(total)
	var sink discardSeeker
	for b.Loop() {
		w := cfb.NewWriterV3(&sink)
		for _, s := range streams {
			sw, err := w.CreateStream(s.name)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := sw.Write(s.data); err != nil {
				b.Fatal(err)
			}
			if err := sw.Close(); err != nil {
				b.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
