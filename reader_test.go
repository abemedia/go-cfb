package cfb_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cfb"
)

func TestFS(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)

	fsys := fstest.MapFS{
		"a.txt":       &fstest.MapFile{Data: []byte("root file")},
		"sub/b.txt":   &fstest.MapFile{Data: []byte("sub file")},
		"sub/c.txt":   &fstest.MapFile{Data: []byte("another sub")},
		"sub/d/e.txt": &fstest.MapFile{Data: []byte("nested")},
	}
	if err := w.AddFS(fsys); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if err := fstest.TestFS(r, "a.txt", "sub/b.txt", "sub/c.txt", "sub/d/e.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestStorage_OpenNotFound(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	s, err := w.CreateStream("Present")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cfb.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.OpenStream("does-not-exist"); !errors.Is(err, cfb.ErrNotFound) {
		t.Errorf("OpenStream: err = %v, want cfb.ErrNotFound", err)
	}
	if _, err := r.OpenStorage("does-not-exist"); !errors.Is(err, cfb.ErrNotFound) {
		t.Errorf("OpenStorage: err = %v, want cfb.ErrNotFound", err)
	}
}

func TestNewReader_Error(t *testing.T) {
	var buf seekBuffer
	w := cfb.NewWriterV3(&buf)
	for _, name := range []string{"Stream1", "Stream2"} {
		s, err := w.CreateStream(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(bytes.Repeat([]byte{0xAA}, 5000)); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.buf

	const (
		dirSector = (1 + 20) * 512  // header + 20 data sectors
		entry1    = dirSector + 128 // skip past root entry
		fat       = dirSector + 512 // skip past dir sector
	)

	tests := []struct {
		name    string
		offset  int
		data    any
		wantMsg string
	}{
		{"bad signature", 0x00, uint8(0), "invalid header signature"},
		{"unsupported major version", 0x1A, uint16(5), "unsupported major version"},
		{"bad byte order", 0x1C, uint16(65535), "invalid byte order"},
		{"v3 sector shift = v4", 0x1E, uint16(12), "invalid sector shift"},
		{"mini-sector shift wrong", 0x20, uint16(7), "invalid mini sector shift"},
		{"v3 NumDirSectors > 0", 0x28, uint32(1), "non-zero directory sector count"},
		{"no FAT sectors", 0x2C, uint32(0), "no FAT sectors"},
		{"mini-stream cutoff wrong", 0x38, uint32(2048), "invalid mini stream cutoff"},
		{"mini-FAT chain without count", 0x3C, uint32(2), "mini FAT starting sector without sector count"},
		{"mini-FAT count without chain", 0x40, uint32(1), "mini FAT sector count without starting sector"},
		{"inflated mini-FAT count", 0x3C, [2]uint32{2, 200}, "mini FAT sector count 200 exceeds FAT capacity"},
		{"inflated DIFAT count", 0x48, uint32(100), "DIFAT sector count"},
		// DIFAT[0] holds the real FAT sector; patch DIFAT[1].
		{"inline DIFAT exceeds MAXREGSECT", 0x50, uint32(0xFFFFFFFE), "inline DIFAT entry exceeds MAXREGSECT"},
		{"FAT count exceeds file size", 0x2C, uint32(0x08000000), "exceeds file size"},
		{"empty name", entry1 + 64 /* 32 name units * 2 */, uint16(2), "invalid directory entry name length"},
		{"odd nameLen", entry1 + 64, uint16(15), "invalid directory entry name length"},
		{"nameLen too large", entry1 + 64, uint16(66), "invalid directory entry name length"},
		{"name not NUL terminated", entry1 + 14 /* len("Stream1") * 2 */, uint8(1), "not NUL terminated"},
		{"embedded NUL in name", entry1 + 2, uint8(0), "invalid directory entry name"},
		{"reserved char in name", entry1 + 2, uint8('/'), "invalid directory entry name"},
		{"sibling ID out of bounds", entry1 + 68, uint32(999), "child or sibling ID references non-existent directory entry"},
		{"cyclic BST", entry1 + 68, uint32(1), "duplicate directory entry reference"},
		{"invalid object type", entry1 + 66, uint8(0x07), "invalid object type"},
		{"stream with endOfChain start", entry1 + 116, uint32(0xFFFFFFFE), "stream object has size but no sector chain"},
		{"FAT chain length mismatch", fat, uint32(0), "sector chain longer than specified"},
		{"cyclic dir chain", fat + 20*4 /* FAT[20] e.g. dir sector */, uint32(20), "cyclic sector chain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := bytes.Clone(src)
			if _, err := binary.Encode(data[tt.offset:], binary.LittleEndian, tt.data); err != nil {
				t.Fatal(err)
			}
			_, err := cfb.NewReader(bytes.NewReader(data))
			if !errors.Is(err, cfb.ErrFormat) {
				t.Fatalf("err = %v, want cfb.ErrFormat", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want message containing %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestNewReader_TruncatedHeader(t *testing.T) {
	if _, err := cfb.NewReader(bytes.NewReader(make([]byte, 100))); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}
