package cfb

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf16"
)

//go:generate go run ./internal/casetablegen

var errInvalidName = errors.New("cfb: invalid name")

// encodeName returns name's UTF-16 LE encoding. It rejects empty names,
// names containing '/', '\\', ':', '!', or U+0000, and names longer than
// 31 UTF-16 code units.
func encodeName(name string) ([]uint16, error) {
	if name == "" || strings.ContainsAny(name, "/\\:!\x00") {
		return nil, errInvalidName
	}
	enc := utf16.Encode([]rune(name))
	if len(enc) > 31 {
		return nil, errInvalidName
	}
	return enc, nil
}

// compareNames orders two UTF-16 names by length first, then by
// case-insensitive code-unit comparison.
func compareNames(a, b []uint16) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	for i := range a {
		if d := int(toUpper(a[i])) - int(toUpper(b[i])); d != 0 {
			return d
		}
	}
	return 0
}

// compareNamesStr orders two string names like [compareNames].
func compareNamesStr(a, b string) int {
	var ab, bb [nameMaxUnits]uint16
	return compareNames(appendName(ab[:0], a), appendName(bb[:0], b))
}

// appendName appends UTF-16 code units of the string s to dst and returns the
// extended buffer.
func appendName(dst []uint16, s string) []uint16 {
	for _, r := range s {
		dst = utf16.AppendRune(dst, r)
	}
	return dst
}

// caseRange maps a [Lo, Hi] code-unit range to uppercase by adding Delta.
// When Delta is [upperLower], the range uses an alternating-pair pattern instead.
type caseRange struct {
	Lo, Hi uint16
	Delta  int16
}

// upperLower is a sentinel Delta value indicating an alternating-pair pattern.
const upperLower int16 = 1 << 14

// toUpper returns the MS-CFB spec-conformant uppercase form of c.
func toUpper(c uint16) uint16 {
	if c < 0x80 {
		if c >= 'a' && c <= 'z' {
			return c - 0x20
		}
		return c
	}
	if c >= 0xD800 && c <= 0xDFFF {
		return c
	}
	i, ok := slices.BinarySearchFunc(caseTable, c, func(r caseRange, c uint16) int {
		switch {
		case c < r.Lo:
			return 1
		case c > r.Hi:
			return -1
		default:
			return 0
		}
	})
	if !ok {
		return c
	}
	r := caseTable[i]
	if r.Delta == upperLower {
		return r.Lo + (c-r.Lo)&^1
	}
	return uint16(int32(c) + int32(r.Delta))
}
