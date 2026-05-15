//go:build windows

package cfb_test

import (
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	cfb "github.com/abemedia/go-cfb"
	"github.com/abemedia/go-cfb/internal/istorage"
)

// TestNameRules verifies our BMP-wide name rules agree with ole32 on
// validity, equivalence, canonical, and sort order.
func TestNameRules(t *testing.T) {
	cfbPath := filepath.Join(t.TempDir(), "bmp.cfb")
	stg, err := istorage.Create(cfbPath, istorage.V3)
	if err != nil {
		t.Fatalf("create CFB: %v", err)
	}
	defer stg.Close()

	firstByUpper := make(map[uint16]uint16)
	for code := uint32(1); code <= 0xFFFF; code++ {
		c := uint16(code)
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		name := string(rune(c))
		u := cfb.ToUpper(c)
		_, seen := firstByUpper[u]
		if !seen {
			firstByUpper[u] = c
		}

		nameValid := true

		stm, err := stg.CreateStream(name)
		switch err {
		case nil:
			stm.Close()
			if seen {
				t.Errorf("U+%04X: want new class, got duplicate of U+%04X (Upper=U+%04X)", c, firstByUpper[u], u)
			}
		case syscall.Errno(0x80030050): // STG_E_FILEALREADYEXISTS
			if !seen {
				t.Errorf("U+%04X: want duplicate, got new class (Upper=U+%04X)", c, u)
			}
			// Verify the canonical entry's Upper matches our Upper class.
			probe, err := stg.OpenStream(name)
			if err != nil {
				t.Errorf("U+%04X: rejected as duplicate but OpenStream failed: %v", c, err)
				break
			}
			info, err := probe.Stat()
			probe.Close()
			if err != nil {
				t.Errorf("U+%04X: stream Stat failed: %v", c, err)
				break
			}
			canonical := uint16([]rune(info.Name)[0])
			if cfb.ToUpper(canonical) != u {
				t.Errorf(
					"U+%04X: want Upper=U+%04X, got Upper=U+%04X (canonical U+%04X)",
					c, cfb.ToUpper(canonical), u, canonical,
				)
			}
		case syscall.Errno(0x800300FC): // STG_E_INVALIDNAME
			nameValid = false
			if !seen {
				delete(firstByUpper, u)
			}
		default:
			t.Fatalf("create 0x%04X: %v", c, err)
		}
		if _, err := cfb.EncodeName(name); nameValid != (err == nil) {
			t.Errorf("U+%04X: want name valid=%v, got name valid=%v", c, nameValid, err == nil)
		}
	}

	wantEntries, err := stg.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	want := make([]uint16, 0, len(wantEntries))
	for _, e := range wantEntries {
		want = append(want, uint16([]rune(e.Name)[0]))
	}
	got := slices.Clone(want)
	slices.SortFunc(got, func(a, b uint16) int { return cfb.CompareNames([]uint16{a}, []uint16{b}) })
	for i, c := range want {
		if got[i] != c {
			t.Errorf(
				"sort %d: want U+%04X (Upper=U+%04X), got U+%04X (Upper=U+%04X)",
				i, c, cfb.ToUpper(c), got[i], cfb.ToUpper(got[i]),
			)
		}
	}
}
