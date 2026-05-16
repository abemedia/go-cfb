package cfb

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
)

// ErrFormat is returned when a CFB file's structure is invalid.
var ErrFormat = errors.New("cfb: not a valid CFB file")

// ErrNotFound is returned when a named stream or storage does not exist.
var ErrNotFound = errors.New("cfb: entry not found")

// A ReadCloser is a [Reader] that must be closed when no longer needed.
type ReadCloser struct {
	*Reader

	f *os.File
}

// OpenReader opens the named CFB file.
func OpenReader(name string) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	r, err := NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &ReadCloser{Reader: r, f: f}, nil
}

// Close closes the CFB file, rendering it unusable for I/O.
func (rc *ReadCloser) Close() error { return rc.f.Close() }

// A Reader serves content from a CFB file.
type Reader struct {
	*Storage

	// Version is the CFB major version (3 for 512-byte sectors, 4 for 4096).
	Version uint16

	r io.ReaderAt
}

// NewReader creates a new [Reader] reading from r.
func NewReader(r io.ReaderAt) (*Reader, error) {
	p := parser{r: r}
	if err := p.parseHeader(); err != nil {
		return nil, err
	}
	if err := p.checkFileSize(); err != nil {
		return nil, err
	}
	need := max(p.h.NumFATSectors, p.h.NumMiniFATSectors, p.h.NumDirSectors)
	p.buf = make([]byte, min(int64(need)*p.secSize, 64<<10))
	if err := p.loadFAT(); err != nil {
		return nil, err
	}
	if err := p.loadMiniFAT(); err != nil {
		return nil, err
	}
	if err := p.parseEntries(); err != nil {
		return nil, err
	}
	if err := p.loadMiniStream(); err != nil {
		return nil, err
	}
	root, err := p.buildStorage(0)
	if err != nil {
		return nil, err
	}
	return &Reader{Storage: root, Version: p.h.MajorVersion, r: r}, nil
}

// Entry is the sealed sum type for storage children. Implementations are
// [*Storage] and [*Stream]; type-switch to discriminate.
type Entry interface{ entryName() string }

// Storage is a directory-like CFB entry holding child Stream and Storage
// objects.
type Storage struct {
	// Name is the storage's name on its parent.
	Name string

	// CLSID is the GUID identifying the COM class of the entry.
	CLSID [16]byte

	// StateBits is the application-defined user flags word for the entry, opaque to CFB.
	StateBits uint32

	// Created is the time the entry was created.
	Created time.Time

	// Modified is the time the entry was last modified.
	Modified time.Time

	// Entries are sorted by length, then by case-insensitive UTF-16
	// code-unit comparison.
	Entries []Entry
}

func (s *Storage) entryName() string { return s.Name }

// OpenStream finds a child stream by name (case-insensitive).
// Returns [ErrNotFound] if the name is unknown or refers to a storage.
func (s *Storage) OpenStream(name string) (*Stream, error) {
	if v, ok := findEntry[*Stream](s, name); ok {
		return v, nil
	}
	return nil, ErrNotFound
}

// OpenStorage finds a child storage by name (case-insensitive).
// Returns [ErrNotFound] if the name is unknown or refers to a stream.
func (s *Storage) OpenStorage(name string) (*Storage, error) {
	if v, ok := findEntry[*Storage](s, name); ok {
		return v, nil
	}
	return nil, ErrNotFound
}

func findEntry[T Entry](s *Storage, name string) (T, bool) {
	target := utf16.Encode([]rune(name))
	for _, e := range s.Entries {
		if v, ok := e.(T); ok && compareNames(target, utf16.Encode([]rune(v.entryName()))) == 0 {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// Stream is a stream entry. ReadAt is stateless and safe for concurrent use.
type Stream struct {
	// Name is the stream's name on its parent.
	Name string

	// StateBits is the application-defined user flags word for the entry, opaque to CFB.
	StateBits uint32

	// Size is the length of the stream's content in bytes.
	Size int64

	r    io.ReaderAt
	runs []run
}

func (s *Stream) entryName() string { return s.Name }

// ReadAt reads up to len(p) bytes starting at off. Reads past Size return
// [io.EOF]; reads against a truncated chain return [io.ErrUnexpectedEOF].
func (s *Stream) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 {
		return 0, errors.New("cfb: negative offset")
	}
	if off >= s.Size {
		return 0, io.EOF
	}
	if avail := s.Size - off; int64(len(p)) > avail {
		p = p[:avail]
		err = io.EOF
	}
	// walkChain ensures runs cover [0, Size); off < Size guarantees a hit.
	i, _ := slices.BinarySearchFunc(s.runs, off, func(r run, target int64) int {
		switch {
		case r.start+r.length <= target:
			return -1
		case target < r.start:
			return 1
		default:
			return 0
		}
	})
	within := off - s.runs[i].start
	for ; i < len(s.runs) && n < len(p); i++ {
		r := s.runs[i]
		end := n + int(min(int64(len(p)-n), r.length-within))
		read, rerr := s.r.ReadAt(p[n:end], r.off+within)
		n += read
		if n < end {
			if rerr == nil || errors.Is(rerr, io.EOF) {
				return n, io.ErrUnexpectedEOF
			}
			return n, rerr
		}
		within = 0
	}
	if n < len(p) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// Open returns a fresh [io.ReadSeeker] positioned at the start of the stream.
func (s *Stream) Open() io.ReadSeeker {
	return io.NewSectionReader(s, 0, s.Size)
}

// run is a contiguous byte range in the file backing one stream's data.
type run struct{ off, length, start int64 }

// header mirrors the on-disk CFB header layout.
type header struct {
	Signature          uint64
	MinorVersion       uint16
	MajorVersion       uint16
	ByteOrder          uint16
	SectorShift        uint16
	MiniSectorShift    uint16
	NumDirSectors      uint32
	NumFATSectors      uint32
	FirstDirSector     uint32
	MiniStreamCutoff   uint32
	FirstMiniFATSector uint32
	NumMiniFATSectors  uint32
	FirstDIFATSector   uint32
	NumDIFATSectors    uint32
	DIFAT              [inlineDIFAT]uint32
}

// parser holds the state shared between NewReader's parsing phases.
type parser struct {
	r          io.ReaderAt
	h          header
	secSize    int64
	fat        []uint32
	miniFAT    []uint32
	miniStream []uint32
	entries    []rawEntry
	buf        []byte
}

// parseHeader reads and validates the CFB header.
func (p *parser) parseHeader() error {
	var buf [headerSize]byte
	if _, err := p.r.ReadAt(buf[:], 0); err != nil {
		return err
	}
	b := readBuf(buf[:])
	p.h.Signature = b.uint64()
	_ = b.sub(16) // CLSID (unused)
	p.h.MinorVersion = b.uint16()
	p.h.MajorVersion = b.uint16()
	p.h.ByteOrder = b.uint16()
	p.h.SectorShift = b.uint16()
	p.h.MiniSectorShift = b.uint16()
	_ = b.sub(6) // reserved
	p.h.NumDirSectors = b.uint32()
	p.h.NumFATSectors = b.uint32()
	p.h.FirstDirSector = b.uint32()
	_ = b.uint32() // TxnSignature (unused)
	p.h.MiniStreamCutoff = b.uint32()
	p.h.FirstMiniFATSector = b.uint32()
	p.h.NumMiniFATSectors = b.uint32()
	p.h.FirstDIFATSector = b.uint32()
	p.h.NumDIFATSectors = b.uint32()
	for i := range p.h.DIFAT {
		p.h.DIFAT[i] = b.uint32()
	}
	p.secSize = int64(1) << p.h.SectorShift
	return p.validateHeader()
}

// validateHeader rejects malformed headers.
func (p *parser) validateHeader() error {
	entriesPerFAT := uint64(p.secSize) / 4
	entriesPerDIFAT := entriesPerFAT - 1
	switch {
	case p.h.Signature != headerSignature:
		return fmt.Errorf("%w: invalid header signature %#016x", ErrFormat, p.h.Signature)
	case p.h.MajorVersion != 3 && p.h.MajorVersion != 4:
		return fmt.Errorf("%w: unsupported major version %d", ErrFormat, p.h.MajorVersion)
	case p.h.ByteOrder != byteOrderLE:
		return fmt.Errorf("%w: invalid byte order %#04x", ErrFormat, p.h.ByteOrder)
	case p.h.MajorVersion == 3 && p.h.SectorShift != sectorShiftV3:
		return fmt.Errorf("%w: invalid sector shift %#04x for version 3", ErrFormat, p.h.SectorShift)
	case p.h.MajorVersion == 4 && p.h.SectorShift != sectorShiftV4:
		return fmt.Errorf("%w: invalid sector shift %#04x for version 4", ErrFormat, p.h.SectorShift)
	case p.h.MiniSectorShift != miniSectorShift:
		return fmt.Errorf("%w: invalid mini sector shift %#04x", ErrFormat, p.h.MiniSectorShift)
	case p.h.MajorVersion == 3 && p.h.NumDirSectors != 0:
		return fmt.Errorf("%w: non-zero directory sector count %d for version 3", ErrFormat, p.h.NumDirSectors)
	case uint64(p.h.NumDirSectors) > uint64(p.h.NumFATSectors)*entriesPerFAT:
		return fmt.Errorf("%w: directory sector count %d exceeds FAT capacity", ErrFormat, p.h.NumDirSectors)
	case p.h.NumFATSectors == 0:
		return fmt.Errorf("%w: no FAT sectors", ErrFormat)
	case uint64(p.h.NumFATSectors) > uint64(math.MaxInt)/uint64(p.secSize):
		return fmt.Errorf("%w: FAT sector count too large", ErrFormat)
	case p.h.MiniStreamCutoff != miniStreamCutoff:
		return fmt.Errorf("%w: invalid mini stream cutoff size %d", ErrFormat, p.h.MiniStreamCutoff)
	case p.h.NumMiniFATSectors == 0 && p.h.FirstMiniFATSector != endOfChain:
		return fmt.Errorf("%w: mini FAT starting sector without sector count", ErrFormat)
	case p.h.NumMiniFATSectors > 0 && p.h.FirstMiniFATSector == endOfChain:
		return fmt.Errorf("%w: mini FAT sector count without starting sector", ErrFormat)
	case uint64(p.h.NumMiniFATSectors) > uint64(p.h.NumFATSectors)*entriesPerFAT:
		return fmt.Errorf("%w: mini FAT sector count %d exceeds FAT capacity", ErrFormat, p.h.NumMiniFATSectors)
	case uint64(p.h.NumDIFATSectors) > uint64(p.h.NumFATSectors)/entriesPerDIFAT+1:
		return fmt.Errorf("%w: DIFAT sector count %d exceeds FAT capacity", ErrFormat, p.h.NumDIFATSectors)
	}
	return nil
}

// checkFileSize verifies the input is large enough to hold the sectors the
// header declares, rejecting a malformed or hostile header before it can
// drive an oversized allocation.
func (p *parser) checkFileSize() error {
	// Below this the header is trusted, avoiding a probe syscall on typical files.
	const probeThreshold = 4 << 20
	sectors := 1 +
		int64(p.h.NumFATSectors) +
		int64(p.h.NumDIFATSectors) +
		int64(p.h.NumMiniFATSectors) +
		int64(p.h.NumDirSectors)
	size := sectors * p.secSize
	if size <= probeThreshold {
		return nil
	}
	var b [1]byte
	switch n, err := p.r.ReadAt(b[:], size-1); {
	case n >= 1:
		return nil
	case err == nil || errors.Is(err, io.EOF):
		return fmt.Errorf("%w: declared sector count exceeds file size", ErrFormat)
	default:
		return err
	}
}

// readSectors reads sectors into out, coalescing contiguous sector numbers
// into single ReadAt calls. Stops once out is full.
func (p *parser) readSectors(out []byte, sectors []uint32) error {
	first := 0
	for i := 1; i <= len(sectors); i++ {
		if i < len(sectors) && sectors[i] == sectors[i-1]+1 {
			continue
		}
		offset := (int64(sectors[first]) + 1) * p.secSize
		start := int64(first) * p.secSize
		end := int64(i) * p.secSize
		if _, err := p.r.ReadAt(out[start:end], offset); err != nil {
			return err
		}
		first = i
	}
	return nil
}

// readDIFAT returns the file-sector number of every FAT sector.
func (p *parser) readDIFAT() ([]uint32, error) {
	out := make([]uint32, 0, p.h.NumFATSectors)
	for _, s := range p.h.DIFAT {
		if s == freeSect {
			continue
		}
		if s > maxRegSect {
			return nil, fmt.Errorf("%w: inline DIFAT entry exceeds MAXREGSECT", ErrFormat)
		}
		out = append(out, s)
	}
	entriesPerDIFAT := int(p.secSize/4) - 1 // last 4 bytes hold next-DIFAT pointer
	buf := p.buf[:p.secSize]
	sec := p.h.FirstDIFATSector
	for range p.h.NumDIFATSectors {
		if sec == endOfChain {
			return nil, fmt.Errorf("%w: short DIFAT chain", ErrFormat)
		}
		if sec > maxRegSect {
			return nil, fmt.Errorf("%w: DIFAT entry exceeds MAXREGSECT", ErrFormat)
		}
		if _, err := p.r.ReadAt(buf, (int64(sec)+1)*p.secSize); err != nil {
			return nil, err
		}
		b := readBuf(buf)
		for range entriesPerDIFAT {
			v := b.uint32()
			if v == freeSect {
				continue
			}
			if v > maxRegSect {
				return nil, fmt.Errorf("%w: DIFAT sector entry exceeds MAXREGSECT", ErrFormat)
			}
			out = append(out, v)
		}
		sec = b.uint32()
	}
	if sec != endOfChain {
		return nil, fmt.Errorf("%w: DIFAT chain not terminated by ENDOFCHAIN", ErrFormat)
	}
	if len(out) != int(p.h.NumFATSectors) {
		return nil, fmt.Errorf("%w: DIFAT contains wrong number of FAT sectors", ErrFormat)
	}
	return out, nil
}

// loadFAT reads the FAT.
func (p *parser) loadFAT() error {
	fatSecs, err := p.readDIFAT()
	if err != nil {
		return err
	}
	entriesPer := int(p.secSize / 4)
	p.fat = make([]uint32, 0, len(fatSecs)*entriesPer)
	batch := len(p.buf) / int(p.secSize)
	for i := 0; i < len(fatSecs); i += batch {
		end := min(i+batch, len(fatSecs))
		buf := p.buf[:(end-i)*int(p.secSize)]
		if err := p.readSectors(buf, fatSecs[i:end]); err != nil {
			return err
		}
		b := readBuf(buf)
		for range len(buf) / 4 {
			p.fat = append(p.fat, b.uint32())
		}
	}
	return nil
}

// walkChain returns the sector numbers of the FAT chain starting at
// first. If numSectors > 0, the chain must contain exactly that many
// sectors; pass 0 to walk to the end with no length constraint.
func walkChain(fat []uint32, first uint32, numSectors int) ([]uint32, error) {
	chain := make([]uint32, 0, cmp.Or(numSectors, 64))
	for sec := first; sec != endOfChain; sec = fat[sec] {
		if sec > maxRegSect {
			return nil, fmt.Errorf("%w: sector chain entry exceeds MAXREGSECT", ErrFormat)
		}
		if int(sec) >= len(fat) {
			return nil, fmt.Errorf("%w: sector chain entry not represented in FAT", ErrFormat)
		}
		chain = append(chain, sec)
		if numSectors > 0 && len(chain) > numSectors {
			return nil, fmt.Errorf("%w: sector chain longer than specified", ErrFormat)
		}
		if len(chain) > len(fat) {
			return nil, fmt.Errorf("%w: cyclic sector chain", ErrFormat)
		}
	}
	if numSectors > 0 && len(chain) < numSectors {
		return nil, fmt.Errorf("%w: sector chain shorter than specified", ErrFormat)
	}
	return chain, nil
}

// loadMiniFAT reads the mini-FAT, if present.
func (p *parser) loadMiniFAT() error {
	if p.h.NumMiniFATSectors == 0 {
		return nil
	}
	chain, err := walkChain(p.fat, p.h.FirstMiniFATSector, int(p.h.NumMiniFATSectors))
	if err != nil {
		return err
	}
	p.miniFAT = make([]uint32, 0, len(chain)*int(p.secSize)/4)
	batch := len(p.buf) / int(p.secSize)
	for i := 0; i < len(chain); i += batch {
		end := min(i+batch, len(chain))
		buf := p.buf[:(end-i)*int(p.secSize)]
		if err := p.readSectors(buf, chain[i:end]); err != nil {
			return err
		}
		b := readBuf(buf)
		for range len(buf) / 4 {
			p.miniFAT = append(p.miniFAT, b.uint32())
		}
	}
	return nil
}

// parseEntries reads and parses the directory chain.
func (p *parser) parseEntries() error {
	chain, err := walkChain(p.fat, p.h.FirstDirSector, int(p.h.NumDirSectors))
	if err != nil {
		return err
	}
	totalBytes := int64(len(chain)) * p.secSize
	if totalBytes%direntrySize != 0 {
		return fmt.Errorf("%w: invalid directory entry array size", ErrFormat)
	}
	p.entries = make([]rawEntry, totalBytes/direntrySize)
	batch := len(p.buf) / int(p.secSize)
	id := 0
	for i := 0; i < len(chain); i += batch {
		end := min(i+batch, len(chain))
		buf := p.buf[:(end-i)*int(p.secSize)]
		if err := p.readSectors(buf, chain[i:end]); err != nil {
			return err
		}
		for j := 0; j < len(buf); j += direntrySize {
			if p.entries[id], err = parseEntry(buf[j:], p.h.MajorVersion); err != nil {
				return err
			}
			if p.entries[id].objectType == objectStream {
				sz := p.entries[id].streamSize
				if sz > math.MaxInt64 || p.h.MajorVersion == 3 && sz > streamSizeMaxV3 {
					return fmt.Errorf("%w: stream size exceeds maximum", ErrFormat)
				}
				maxBytes := uint64(len(p.fat)) * uint64(p.secSize)
				if sz < miniStreamCutoff {
					maxBytes = uint64(len(p.miniFAT)) * miniSectorSize
				}
				if sz > maxBytes {
					return fmt.Errorf("%w: stream size exceeds FAT capacity", ErrFormat)
				}
			}
			id++
		}
	}
	if len(p.entries) == 0 {
		return fmt.Errorf("%w: no directory entries", ErrFormat)
	}
	if p.entries[0].objectType != objectRoot {
		return fmt.Errorf("%w: first directory entry is not the root storage object", ErrFormat)
	}
	return nil
}

// rawEntry holds construction-only bookkeeping for a directory entry.
// Exactly one of storage / stream is set, determined by objectType.
type rawEntry struct {
	storage *Storage
	stream  *Stream

	objectType        uint8
	leftSib, rightSib uint32
	childID           uint32
	startSector       uint32
	streamSize        uint64
	visited           bool
}

// parseEntry parses a directory entry.
func parseEntry(buf []byte, majorVersion uint16) (rawEntry, error) {
	b := readBuf(buf)
	var nameUnits [nameMaxUnits]uint16
	for i := range nameUnits {
		nameUnits[i] = b.uint16()
	}
	nameLen := b.uint16()
	objectType := b.uint8()
	_ = b.uint8() // color
	leftSib := b.uint32()
	rightSib := b.uint32()
	childID := b.uint32()
	var clsid [16]byte
	copy(clsid[:], b.sub(16))
	stateBits := b.uint32()
	created := b.uint64()
	modified := b.uint64()
	startSector := b.uint32()
	streamSize := b.uint64()
	if majorVersion == 3 {
		// Mask off high bits on v3 as older writers sometimes left them
		// uninitialised and they may be non-zero.
		streamSize &= 0xFFFFFFFF
	}

	re := rawEntry{
		objectType:  objectType,
		leftSib:     leftSib,
		rightSib:    rightSib,
		childID:     childID,
		startSector: startSector,
		streamSize:  streamSize,
	}
	var name string
	if objectType != objectUnallocated {
		if nameLen < 4 || nameLen%2 != 0 || nameLen > 2*nameMaxUnits {
			return rawEntry{}, fmt.Errorf("%w: invalid directory entry name length %d", ErrFormat, nameLen)
		}
		n := int(nameLen)/2 - 1
		if nameUnits[n] != 0 {
			return rawEntry{}, fmt.Errorf("%w: directory entry name not NUL terminated", ErrFormat)
		}
		name = string(utf16.Decode(nameUnits[:n]))
		if strings.ContainsAny(name, "/\\:!\x00") {
			return rawEntry{}, fmt.Errorf("%w: invalid directory entry name %q", ErrFormat, name)
		}
	}
	switch objectType {
	case objectStorage, objectRoot:
		re.storage = &Storage{
			Name:      name,
			CLSID:     clsid,
			StateBits: stateBits,
			Created:   filetimeToTime(created),
			Modified:  filetimeToTime(modified),
		}
	case objectStream:
		re.stream = &Stream{
			Name:      name,
			StateBits: stateBits,
			Size:      int64(streamSize),
		}
	}
	return re, nil
}

// loadMiniStream resolves the chain backing the mini-stream.
func (p *parser) loadMiniStream() error {
	de := p.entries[0]
	if de.streamSize == 0 || de.startSector == endOfChain {
		return nil
	}
	numSectors := int((de.streamSize + uint64(p.secSize) - 1) / uint64(p.secSize))
	chain, err := walkChain(p.fat, de.startSector, numSectors)
	if err != nil {
		return err
	}
	p.miniStream = chain
	return nil
}

// buildStorage materialises the storage at directory entry id.
func (p *parser) buildStorage(id uint32) (*Storage, error) {
	de := p.entries[id]
	s := de.storage
	if de.childID == noStream {
		return s, nil
	}
	if err := p.walkBST(de.childID, &s.Entries); err != nil {
		return nil, err
	}
	return s, nil
}

// walkBST traverses a storage's child BST in-order.
func (p *parser) walkBST(id uint32, out *[]Entry) error {
	if id == noStream {
		return nil
	}
	if int(id) >= len(p.entries) {
		return fmt.Errorf("%w: child or sibling ID references non-existent directory entry", ErrFormat)
	}
	if p.entries[id].visited {
		return fmt.Errorf("%w: duplicate directory entry reference", ErrFormat)
	}
	p.entries[id].visited = true

	de := p.entries[id]
	if err := p.walkBST(de.leftSib, out); err != nil {
		return err
	}
	switch de.objectType {
	case objectStorage:
		sub, err := p.buildStorage(id)
		if err != nil {
			return err
		}
		*out = append(*out, sub)
	case objectStream:
		st, err := p.buildStream(id)
		if err != nil {
			return err
		}
		*out = append(*out, st)
	case objectUnallocated:
		// Tolerated: spec forbids unallocated entries in any BST, but if we
		// encounter one we ignore the slot rather than failing.
	case objectRoot:
		return fmt.Errorf("%w: duplicate root storage object", ErrFormat)
	default:
		return fmt.Errorf("%w: invalid object type %#x", ErrFormat, de.objectType)
	}
	return p.walkBST(de.rightSib, out)
}

// buildStream materialises the stream at directory entry id.
func (p *parser) buildStream(id uint32) (*Stream, error) {
	de := p.entries[id]
	st := de.stream
	if de.streamSize == 0 {
		return st, nil
	}
	if de.startSector == endOfChain {
		return nil, fmt.Errorf("%w: stream object has size but no sector chain", ErrFormat)
	}
	mini := de.streamSize < miniStreamCutoff
	fat := p.fat
	secSize := uint64(p.secSize)
	if mini {
		fat = p.miniFAT
		secSize = miniSectorSize
	}
	chain, err := walkChain(fat, de.startSector, int((de.streamSize+secSize-1)/secSize))
	if err != nil {
		return nil, err
	}
	st.r = p.r
	if mini {
		if st.runs, err = p.resolveMiniRuns(chain, st.Size); err != nil {
			return nil, err
		}
	} else {
		st.runs = resolveRegularRuns(chain, p.secSize, st.Size)
	}
	return st, nil
}

// resolveMiniRuns translates a mini-sector chain into file-byte runs.
func (p *parser) resolveMiniRuns(miniChain []uint32, size int64) ([]run, error) {
	if len(miniChain) == 0 {
		return nil, nil
	}
	var total int64
	runs := make([]run, 0, 1)
	var cur run
	for i, miniSec := range miniChain {
		miniOff := int64(miniSec) * miniSectorSize
		idx := miniOff / p.secSize
		if int(idx) >= len(p.miniStream) {
			return nil, fmt.Errorf("%w: mini sector exceeds mini stream size", ErrFormat)
		}
		sec := int64(p.miniStream[idx])
		off := (sec+1)*p.secSize + miniOff%p.secSize
		if i == 0 {
			cur = run{off: off, length: miniSectorSize}
			continue
		}
		if off == cur.off+cur.length {
			cur.length += miniSectorSize
			continue
		}
		runs = append(runs, cur)
		total += cur.length
		cur = run{off: off, length: miniSectorSize, start: total}
	}
	runs = append(runs, cur)
	total += cur.length
	if total > size {
		runs[len(runs)-1].length -= total - size
	}
	return runs, nil
}

// resolveRegularRuns translates a regular-sector chain into file-byte runs.
func resolveRegularRuns(chain []uint32, secSize, size int64) []run {
	if len(chain) == 0 {
		return nil
	}
	var total int64
	runs := make([]run, 0, 1)
	cur := run{off: (int64(chain[0]) + 1) * secSize, length: secSize}
	for _, sec := range chain[1:] {
		next := (int64(sec) + 1) * secSize
		if next == cur.off+cur.length {
			cur.length += secSize
			continue
		}
		runs = append(runs, cur)
		total += cur.length
		cur = run{off: next, length: secSize, start: total}
	}
	runs = append(runs, cur)
	total += cur.length
	if total > size {
		runs[len(runs)-1].length -= total - size
	}
	return runs
}

// readBuf reads little-endian fixed-width values and advances itself.
type readBuf []byte

func (b *readBuf) uint8() uint8 {
	v := (*b)[0]
	*b = (*b)[1:]
	return v
}

func (b *readBuf) uint16() uint16 {
	v := binary.LittleEndian.Uint16(*b)
	*b = (*b)[2:]
	return v
}

func (b *readBuf) uint32() uint32 {
	v := binary.LittleEndian.Uint32(*b)
	*b = (*b)[4:]
	return v
}

func (b *readBuf) uint64() uint64 {
	v := binary.LittleEndian.Uint64(*b)
	*b = (*b)[8:]
	return v
}

func (b *readBuf) sub(n int) readBuf {
	out := (*b)[:n]
	*b = (*b)[n:]
	return out
}
