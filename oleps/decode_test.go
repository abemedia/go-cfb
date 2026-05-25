package oleps_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/abemedia/go-cfb/oleps"
)

func TestUnmarshal_Errors(t *testing.T) {
	src, err := oleps.Marshal(oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: fmtidSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.I4(0x01020304)},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	tests := []struct {
		name    string
		offset  int
		data    any
		want    error
		wantMsg string
	}{
		{"bad byte order", 0, uint16(0), oleps.ErrFormat, "invalid byte order"},
		{"bad version", 2, uint16(2), oleps.ErrFormat, "invalid property set version 2"},
		{"zero sets", 24, uint32(0), oleps.ErrFormat, "invalid property set count"},
		{"three sets", 24, uint32(3), oleps.ErrFormat, "invalid property set count"},
		{"two sets unsupported", 24, uint32(2), oleps.ErrUnsupported, "more than one property set"},
		{"property set offset into header", 44, uint32(0), oleps.ErrFormat, "invalid property set offset"},
		{"property set offset past end", 44, uint32(0xFFFFFF), io.ErrUnexpectedEOF, ""},
		{"property set size past end", 48, uint32(0xFFFFFF), io.ErrUnexpectedEOF, ""},
		{"dictionary unsupported", 64, uint32(0), oleps.ErrUnsupported, "named properties"},
		{"overlapping offsets", 68, uint32(20), oleps.ErrFormat, "invalid property offset"},
		{"code page wrong type", 72, uint16(0x03), oleps.ErrFormat, "invalid code page property"},
		{"unknown code page", 76, uint16(12000), oleps.ErrUnsupported, "code page 12000"},
		{"unregistered VT", 80, uint16(0x0099), oleps.ErrUnsupported, "property type 0x0099"},
		{"nonzero property padding", 82, uint16(0xFFFF), oleps.ErrFormat, "property padding is not zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(src)
			if _, err := binary.Encode(data[test.offset:], binary.LittleEndian, test.data); err != nil {
				t.Fatal(err)
			}
			_, err := oleps.Unmarshal(data)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Errorf("err = %q, want message containing %q", err, test.wantMsg)
			}
		})
	}
}

func TestUnmarshal_Truncated(t *testing.T) {
	s := oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: fmtidSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.LPSTR("hello world")},
				{ID: 3, Value: oleps.I4(0x01020304)},
				{ID: 4, Value: oleps.FileTime(time.Now())},
				{ID: 5, Value: oleps.UI4(0xDEADBEEF)},
			},
		}},
	}
	full, err := oleps.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for i := range full {
		if _, err := oleps.Unmarshal(full[:i:i]); err != io.ErrUnexpectedEOF {
			t.Fatalf("Unmarshal(full[:%d]) = %v, want io.ErrUnexpectedEOF", i, err)
		}
	}
}

func TestUnmarshal_EmptyLPSTRZeroSize(t *testing.T) {
	input := oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: fmtidSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.LPSTR("")},
				{ID: 3, Value: oleps.I4(0x01020304)},
			},
		}},
	}
	data, err := oleps.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Stream layout: header(48) + set_header(8) + property_table(24) + values.
	// Set-relative offsets: prop1=32, prop2=40, prop3=52.
	// Mutate LPSTR from cb=1 (12 value bytes incl. terminator+padding) to
	// cb=0 (4 value bytes): shrink the set size, shift prop3's table entry,
	// then delete the original cb=1 field. The trailing terminator+padding
	// (4 zero bytes) slides up and naturally becomes the new cb=0.
	if _, err := binary.Encode(data[48:], binary.LittleEndian, uint32(56)); err != nil {
		t.Fatal(err)
	}
	if _, err := binary.Encode(data[76:], binary.LittleEndian, uint32(48)); err != nil {
		t.Fatal(err)
	}
	data = slices.Delete(data, 92, 96)

	got, err := oleps.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if diff := cmp.Diff(input, got, cmpOpts); diff != "" {
		t.Errorf("decoded mismatch (-want +got):\n%s", diff)
	}
}
