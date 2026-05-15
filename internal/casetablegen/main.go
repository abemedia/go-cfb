// Generator for casetable.go in the cfb package root.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

const urlVista = "https://www.unicode.org/Public/5.0.0/ucd/CaseFolding.txt"

// upperLower mirrors cfb.upperLower so the generator package stays independent.
const upperLower int16 = 1 << 14

type pair struct{ lo, up uint16 }

type rangeEntry struct {
	Lo, Hi uint16
	Delta  int16
}

// exception is one row of the MS-CFB section 5 footnote 3 exception table:
// a (uppercase, lowercase) pair that's either added to or subtracted from
// the base Unicode case-folding data before inversion.
type exception struct {
	add  bool
	from uint16 // uppercase code point
	to   uint16 // lowercase code point
}

var vistaExceptions = []exception{
	{true, 0x023A, 0x2C65},  // LATIN SMALL LETTER A WITH STROKE
	{false, 0x023A, 0x023A}, // LATIN CAPITAL LETTER A WITH STROKE
	{true, 0x2C65, 0x2C65},  // LATIN SMALL LETTER A WITH STROKE
	{false, 0x2C65, 0x023A}, // LATIN CAPITAL LETTER A WITH STROKE
	{true, 0x023E, 0x2C66},  // LATIN SMALL LETTER T WITH DIAGONAL STROKE
	{false, 0x023E, 0x023E}, // LATIN CAPITAL LETTER T WITH DIAGONAL STROKE
	{true, 0x2C66, 0x2C66},  // LATIN SMALL LETTER T WITH DIAGONAL STROKE
	{false, 0x2C66, 0x023E}, // LATIN CAPITAL LETTER T WITH DIAGONAL STROKE
	{true, 0x03C2, 0x03A3},  // GREEK CAPITAL LETTER SIGMA
	{false, 0x03C2, 0x03C2}, // GREEK SMALL LETTER FINAL SIGMA
	{true, 0x03C3, 0x03A3},  // GREEK CAPITAL LETTER SIGMA
	{false, 0x03C3, 0x03C2}, // GREEK SMALL LETTER FINAL SIGMA
	{true, 0x1FC3, 0x1FC3},  // GREEK SMALL LETTER ETA WITH PROSGEGRAMMENI
	{false, 0x1FC3, 0x1FCC}, // GREEK CAPITAL LETTER ETA WITH PROSGEGRAMMENI
	{true, 0x1FCC, 0x1FC3},  // GREEK SMALL LETTER ETA WITH PROSGEGRAMMENI
	{false, 0x1FCC, 0x1FCC}, // GREEK CAPITAL LETTER ETA WITH PROSGEGRAMMENI

	// Not in the spec table but ole32 applies the same treatment as 1FC3/1FCC.
	{true, 0x1FF3, 0x1FF3},  // GREEK SMALL LETTER OMEGA WITH YPOGEGRAMMENI
	{false, 0x1FF3, 0x1FFC}, // GREEK CAPITAL LETTER OMEGA WITH PROSGEGRAMMENI
	{true, 0x1FFC, 0x1FF3},  // GREEK SMALL LETTER OMEGA WITH YPOGEGRAMMENI
	{false, 0x1FFC, 0x1FFC}, // GREEK CAPITAL LETTER OMEGA WITH PROSGEGRAMMENI
}

func main() {
	body, err := fetch(urlVista)
	if err != nil {
		log.Fatalf("fetch %s: %v", urlVista, err)
	}
	m := parseFolding(body)
	pairs := apply(m, vistaExceptions)
	ranges := compress(pairs)
	if err := emit("casetable.go", ranges); err != nil {
		log.Fatalf("emit: %v", err)
	}
}

func fetch(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// parseFolding returns a lowercase-to-uppercase map for CaseFolding.txt status
// C and S entries within the BMP.
func parseFolding(body []byte) map[uint16]uint16 {
	out := make(map[uint16]uint16)
	for line := range strings.SplitSeq(string(body), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ";")
		if len(fields) < 3 {
			continue
		}
		status := strings.TrimSpace(fields[1])
		if status != "C" && status != "S" {
			continue
		}
		src, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 16, 32)
		if err != nil {
			continue
		}
		dst, err := strconv.ParseUint(strings.TrimSpace(strings.Fields(fields[2])[0]), 16, 32)
		if err != nil {
			continue
		}
		if src > 0xFFFF || dst > 0xFFFF {
			continue
		}
		if _, ok := out[uint16(dst)]; !ok {
			out[uint16(dst)] = uint16(src)
		}
	}
	return out
}

// apply returns the base map with exceptions applied, as a sorted pair slice.
func apply(base map[uint16]uint16, exceptions []exception) []pair {
	for _, e := range exceptions {
		if e.add {
			if e.from == e.to {
				delete(base, e.from)
			} else {
				base[e.from] = e.to
			}
		} else if v, ok := base[e.from]; ok && v == e.to {
			delete(base, e.from)
		}
	}
	pairs := make([]pair, 0, len(base))
	for lo, up := range base {
		if lo == up {
			continue
		}
		pairs = append(pairs, pair{lo, up})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].lo < pairs[j].lo })
	return pairs
}

// compress encodes sorted pairs into rangeEntry values, detecting
// constant-delta runs and alternating upper/lower-pair runs.
func compress(pairs []pair) []rangeEntry {
	var out []rangeEntry
	i := 0
	for i < len(pairs) {
		if j := alternatingRunEnd(pairs, i); j-i >= 2 {
			out = append(out, rangeEntry{
				Lo:    pairs[i].up,
				Hi:    pairs[j-1].lo,
				Delta: upperLower,
			})
			i = j
			continue
		}
		j := constantRunEnd(pairs, i)
		out = append(out, rangeEntry{
			Lo:    pairs[i].lo,
			Hi:    pairs[j-1].lo,
			Delta: int16(int32(pairs[i].up) - int32(pairs[i].lo)),
		})
		i = j
	}
	return out
}

// alternatingRunEnd returns j such that pairs[i:j] form an alternating
// upper/lower-pair run (uc = lc-1, consecutive lcs at +2). Returns i if no
// run starts here.
func alternatingRunEnd(pairs []pair, i int) int {
	if pairs[i].lo != pairs[i].up+1 {
		return i
	}
	j := i + 1
	for j < len(pairs) &&
		pairs[j].lo == pairs[j-1].lo+2 &&
		pairs[j].up == pairs[j-1].up+2 &&
		pairs[j].lo == pairs[j].up+1 {
		j++
	}
	return j
}

// constantRunEnd returns j such that pairs[i:j] are consecutive and share
// pairs[i]'s delta.
func constantRunEnd(pairs []pair, i int) int {
	delta := int32(pairs[i].up) - int32(pairs[i].lo)
	j := i + 1
	for j < len(pairs) &&
		pairs[j].lo == pairs[j-1].lo+1 &&
		int32(pairs[j].up)-int32(pairs[j].lo) == delta {
		j++
	}
	return j
}

var tableTmpl = template.Must(template.New("table").Funcs(template.FuncMap{
	"hex": func(v uint16) string { return fmt.Sprintf("0x%04X", v) },
	"delta": func(v int16) string {
		if v == upperLower {
			return "upperLower"
		}
		return strconv.Itoa(int(v))
	},
}).Parse(`// Code generated by casetablegen; DO NOT EDIT.

package cfb

// caseTable is the Windows Vista / Server 2008+ uppercase mapping table:
// Unicode 5.0 simple case folding plus MS-CFB section 5 footnote 3 exceptions.
var caseTable = []caseRange{
{{- range .}}
	{Lo: {{hex .Lo}}, Hi: {{hex .Hi}}, Delta: {{delta .Delta}}},
{{- end}}
}
`))

func emit(path string, ranges []rangeEntry) error {
	var buf bytes.Buffer
	if err := tableTmpl.Execute(&buf, ranges); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format: %v\n%s", err, buf.String())
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
