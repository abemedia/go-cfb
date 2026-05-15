package cfb_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/abemedia/go-cfb"
)

// seekBuffer is an in-memory io.WriteSeeker.
type seekBuffer struct {
	buf []byte
	pos int
}

func (b *seekBuffer) Write(p []byte) (int, error) {
	need := b.pos + len(p)
	if need > len(b.buf) {
		b.buf = append(b.buf, make([]byte, need-len(b.buf))...)
	}
	copy(b.buf[b.pos:], p)
	b.pos += len(p)
	return len(p), nil
}

func (b *seekBuffer) Seek(off int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = off
	case io.SeekCurrent:
		abs = int64(b.pos) + off
	case io.SeekEnd:
		abs = int64(len(b.buf)) + off
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("invalid offset")
	}
	b.pos = int(abs)
	return abs, nil
}

// TestRoundTrip writes streams and reads them back, verifying name + content
// across the writer's sector paths (mini-stream, regular, DIFAT chain) and
// version variants.
func TestRoundTrip(t *testing.T) {
	type stream struct {
		name string
		data []byte
	}
	tests := []struct {
		name    string
		newW    func(io.WriteSeeker) *cfb.Writer
		streams []stream
	}{
		{
			name: "v3 empty",
			newW: cfb.NewWriterV3,
		},
		{
			name: "v4 empty",
			newW: cfb.NewWriterV4,
		},
		{
			name: "v3 mini and regular",
			newW: cfb.NewWriterV3,
			streams: []stream{
				{"hello", []byte("hello world")},
				{"big.bin", bytes.Repeat([]byte{0x01, 0x02, 0x03}, 5000)},
				{"empty", nil},
			},
		},
		{
			name: "v4 big stream",
			newW: cfb.NewWriterV4,
			streams: []stream{
				{"BigStream", bytes.Repeat([]byte("v4 content "), 2000)},
			},
		},
		{
			name: "mini-stream cutoff edges",
			newW: cfb.NewWriterV3,
			streams: []stream{
				{"tiny", []byte("17-byte content!!")},             // < one mini-sector
				{"oneMini", bytes.Repeat([]byte{0x42}, 64)},       // exactly one mini-sector
				{"belowCutoff", bytes.Repeat([]byte{0x55}, 4095)}, // last mini
				{"atCutoff", bytes.Repeat([]byte{0x66}, 4096)},    // strict <4096 cutoff so this is regular
				{"aboveCutoff", bytes.Repeat([]byte{0x77}, 4097)}, // regular
			},
		},
		{
			name: "interleaved mini and regular",
			newW: cfb.NewWriterV3,
			streams: []stream{
				{"big1", bytes.Repeat([]byte{0xAA}, 6000)},
				{"small1", bytes.Repeat([]byte{0xBB}, 300)},
				{"big2", bytes.Repeat([]byte{0xCC}, 5000)},
				{"small2", bytes.Repeat([]byte{0xDD}, 600)},
			},
		},
		{
			// 8 MB > 7.1 MB threshold (109 * 128 sectors) forces DIFAT chain.
			name: "DIFAT chain",
			newW: cfb.NewWriterV3,
			streams: []stream{
				{"Big", bytes.Repeat([]byte{0xCD}, 8<<20)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf seekBuffer
			w := tt.newW(&buf)
			for _, s := range tt.streams {
				wr, err := w.CreateStream(s.name)
				if err != nil {
					t.Fatalf("CreateStream %q: %v", s.name, err)
				}
				if _, err := wr.Write(s.data); err != nil {
					t.Fatalf("Write %q: %v", s.name, err)
				}
				if err := wr.Close(); err != nil {
					t.Fatalf("Close %q: %v", s.name, err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			r, err := cfb.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			if got, want := len(r.Entries), len(tt.streams); got != want {
				t.Fatalf("got %d entries, want %d", got, want)
			}
			for _, s := range tt.streams {
				st, err := r.OpenStream(s.name)
				if err != nil {
					t.Fatalf("OpenStream %q: %v", s.name, err)
				}
				got, err := io.ReadAll(st.Open())
				if err != nil {
					t.Fatalf("ReadAll %q: %v", s.name, err)
				}
				if !bytes.Equal(got, s.data) {
					t.Errorf("%q: content mismatch (got %d bytes, want %d)", s.name, len(got), len(s.data))
				}
			}
		})
	}
}
