// Package cfb reads and writes Microsoft Compound File Binary (CFB) files,
// the container format used by .msg, .doc, .xls, .msi, and other COM
// Structured Storage consumers.
//
// The format is specified in [MS-CFB], "Compound File Binary File Format".
// This implementation supports both v3 (512-byte sectors) and v4 (4096-byte
// sectors).
//
// [MS-CFB]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-cfb/
package cfb

import "time"

// Sizes and limits.
const (
	headerSize       = 512
	direntrySize     = 128
	nameMaxUnits     = 32 // 64-byte name field / 2 bytes per UTF-16 code unit
	miniSectorSize   = 64
	miniStreamCutoff = 4096
	streamSizeMaxV3  = 1 << 31
	inlineDIFAT      = 109
)

// Header field magic numbers.
const (
	headerSignature uint64 = 0xE11AB1A1E011CFD0
	minorVersion    uint16 = 0x003E
	byteOrderLE     uint16 = 0xFFFE
	sectorShiftV3   uint16 = 0x0009
	sectorShiftV4   uint16 = 0x000C
	miniSectorShift uint16 = 0x0006
)

// Directory entry field values.
const (
	objectUnallocated uint8 = 0x00
	objectStorage     uint8 = 0x01
	objectStream      uint8 = 0x02
	objectRoot        uint8 = 0x05

	// colorBlack is the only colour we use; an all-black BST vacuously
	// satisfies the red-black invariants.
	colorBlack uint8 = 0x01

	noStream uint32 = 0xFFFFFFFF
)

// Special FAT sector values.
const (
	maxRegSect uint32 = 0xFFFFFFFA
	difSect    uint32 = 0xFFFFFFFC
	fatSect    uint32 = 0xFFFFFFFD
	endOfChain uint32 = 0xFFFFFFFE
	freeSect   uint32 = 0xFFFFFFFF
)

const (
	filetimeEpoch = 116444736000000000 // FILETIME of the Unix epoch
	filetimeTicks = 10_000_000         // FILETIME ticks per second
)

func filetimeToTime(ft uint64) time.Time {
	if ft == 0 {
		return time.Time{}
	}
	sec, rem := ft/filetimeTicks, ft%filetimeTicks
	return time.Unix(int64(sec)-filetimeEpoch/filetimeTicks, int64(rem)*100).UTC()
}

func timeToFiletime(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	if sec >= 0 {
		return filetimeEpoch + uint64(sec)*filetimeTicks + uint64(nsec/100)
	}
	return filetimeEpoch - uint64(-sec)*filetimeTicks + uint64(nsec/100)
}
