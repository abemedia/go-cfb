package cfb_test

import (
	"cmp"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/abemedia/go-cfb"
)

func TestEncodeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"reserved-prefix system stream allowed", "\x05SummaryInformation", nil},
		{"exactly 31 code units (boundary)", strings.Repeat("a", 31), nil},
		{"15 emoji = 30 code units (under limit)", strings.Repeat("📊", 15), nil},
		{"empty", "", cfb.ErrInvalidName},
		{"forward slash", "foo/bar", cfb.ErrInvalidName},
		{"backslash", "foo\\bar", cfb.ErrInvalidName},
		{"colon", "foo:bar", cfb.ErrInvalidName},
		{"bang", "foo!bar", cfb.ErrInvalidName},
		{"null", "foo\x00bar", cfb.ErrInvalidName},
		{"32 code units (one over limit)", strings.Repeat("a", 32), cfb.ErrInvalidName},
		{"16 emoji, 32 UTF-16 code units (one over limit)", strings.Repeat("📊", 16), cfb.ErrInvalidName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := cfb.EncodeName(tt.input); !errors.Is(err, tt.wantErr) {
				t.Errorf("EncodeName(%q) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCompareNames(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"shorter wins (length-first)", "Z", "Foo", -1},
		{"shorter wins even when first byte > peer", "Foo", "foobar", -1},
		{"longer loses", "foobar", "Z", 1},
		{"case-insensitive equal", "foo", "FOO", 0},
		{"third position decides after case-fold", "foo", "FOZ", -1},
		{"pairwise comparison", "abc", "abd", -1},
		{"both empty", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfb.CompareNames(utf16.Encode([]rune(tt.a)), utf16.Encode([]rune(tt.b)))
			if cmp.Compare(got, 0) != tt.want {
				t.Errorf("CompareNames(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestUpperASCII(t *testing.T) {
	for c := range uint16(0x80) {
		want := c
		if c >= 'a' && c <= 'z' {
			want -= 0x20
		}
		if got := cfb.ToUpper(c); got != want {
			t.Errorf("ToUpper(%#x) = %#x, want %#x", c, got, want)
		}
	}
}

func TestUpperSurrogatePassThrough(t *testing.T) {
	for c := uint16(0xD800); c <= 0xDFFF; c++ {
		if got := cfb.ToUpper(c); got != c {
			t.Errorf("ToUpper(%#x) = %#x, want unchanged (surrogate)", c, got)
		}
	}
}

func TestUpper(t *testing.T) {
	cases := []struct {
		name     string
		in, want uint16
	}{
		{"Latin-1 constant delta", 'à', 'À'},
		{"Latin-1 irregular delta", 'ÿ', 'Ÿ'},
		{"Greek constant delta", 'α', 'Α'},
		{"sharp s, no fold", 'ß', 'ß'},
		{"division sign, no fold", '÷', '÷'},
		{"alternating even-up self-map", 'Ā', 'Ā'},
		{"alternating even-up pair", 'ā', 'Ā'},
		{"alternating odd-up self-map", 'Ļ', 'Ļ'},
		{"alternating odd-up pair", 'ļ', 'Ļ'},
		{"Vista exception, final sigma", 'ς', 'Σ'},
		{"Vista exception, capital A with stroke", 'Ⱥ', 'ⱥ'},
		{"Vista exception, small A with stroke", 'ⱥ', 'ⱥ'},
		{"Vista keeps IE with grave", 'ѐ', 'Ѐ'},
		{"diverges from unicode.ToUpper", 'ı', 'ı'},
		{"outside any fold range", 0x4E2D, 0x4E2D},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cfb.ToUpper(c.in); got != c.want {
				t.Errorf("ToUpper(%#x) = %#x, want %#x", c.in, got, c.want)
			}
		})
	}
}
