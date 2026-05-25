package oleps_test

import (
	"testing"

	"github.com/abemedia/go-cfb/oleps"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

func TestCodepageEncoding(t *testing.T) {
	tests := []struct {
		name     string
		cp       uint16
		expected encoding.Encoding
	}{
		{"CP_ACP", 0, charmap.Windows1252},
		{"Windows-1252", 1252, charmap.Windows1252},
		{"UTF-16LE", 1200, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)},
		{"UTF-8", 65001, unicode.UTF8},
		{"Shift_JIS", 932, japanese.ShiftJIS},
		{"GBK", 936, simplifiedchinese.GBK},
		{"ISO-8859-1", 28591, charmap.ISO8859_1},
		{"unsupported", 9999, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oleps.CodepageEncoding(tt.cp)
			if got != tt.expected {
				t.Errorf("CodepageEncoding(%d) = %v, want %v", tt.cp, got, tt.expected)
			}
		})
	}
}
