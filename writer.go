package cfb

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/bits"
	"slices"
	"sync"
	"time"
	"unicode/utf16"
)

var rootName = utf16.Encode([]rune("Root Entry"))

var errDuplicateName = errors.New("cfb: duplicate name")

// Writer implements a CFB file writer.
//
// CreateStream, CreateStorage, and concurrent Writes on distinct
// [StreamWriter] values are safe to call from multiple goroutines.
type Writer struct {
	*StorageWriter

	mu sync.Mutex

	raw     io.WriteSeeker
	bw      *bufio.Writer
	cw      countWriter
	secSize int

	miniBuf  []byte   // pending mini-stream bytes; flushed in sector-sized chunks
	miniSecs []uint32 // file-sector numbers backing the mini-stream

	descendants uint32 // total directory entries; bumped by CreateStream/Storage

	closed bool
}

// NewWriterV3 returns a Writer that produces a CFB v3 (512-byte sector) file.
func NewWriterV3(w io.WriteSeeker) *Writer { return newWriter(w, 512) }

// NewWriterV4 returns a Writer that produces a CFB v4 (4096-byte sector) file.
func NewWriterV4(w io.WriteSeeker) *Writer { return newWriter(w, 4096) }

func newWriter(ws io.WriteSeeker, secSize int) *Writer {
	bw := bufio.NewWriterSize(&seekOnce{seeker: ws, offset: int64(secSize)}, 32<<10)
	w := &Writer{
		raw:         ws,
		bw:          bw,
		cw:          countWriter{w: bw, count: int64(secSize)},
		secSize:     secSize,
		descendants: 1, // root entry
	}
	w.StorageWriter = &StorageWriter{w: w, name: rootName}
	return w
}

// Close finishes writing the CFB file. It does not close the underlying writer.
//
// Every [StreamWriter] returned by CreateStream must be closed first.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var checkClosed func(*StorageWriter) error
	checkClosed = func(sw *StorageWriter) error {
		for _, sub := range sw.storages {
			if err := checkClosed(sub); err != nil {
				return err
			}
		}
		for _, st := range sw.streams {
			if !st.closed {
				return fmt.Errorf("cfb: stream %q not closed before Writer.Close", string(utf16.Decode(st.name)))
			}
		}
		return nil
	}
	if err := checkClosed(w.StorageWriter); err != nil {
		return err
	}

	if w.closed {
		return errors.New("cfb: writer closed twice")
	}
	w.closed = true

	var s serializer
	if err := s.init(w); err != nil {
		return err
	}
	if err := s.emitDirectory(); err != nil {
		return err
	}
	if err := s.emitFAT(); err != nil {
		return err
	}
	if err := s.emitDIFAT(); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return s.patchHeader()
}

// sectorIdx returns the file-sector number of the next byte to be written.
func (w *Writer) sectorIdx() int64 { return w.cw.count/int64(w.secSize) - 1 }

// writeMini appends p to the mini-stream, flushing each completed sector.
// Caller must hold w.mu.
func (w *Writer) writeMini(p []byte) error {
	for len(p) > 0 {
		if cap(w.miniBuf) < w.secSize {
			w.miniBuf = make([]byte, 0, w.secSize)
		}
		n := min(len(p), w.secSize-len(w.miniBuf))
		w.miniBuf = append(w.miniBuf, p[:n]...)
		p = p[n:]
		if len(w.miniBuf) == w.secSize {
			sec := uint32(w.sectorIdx())
			if _, err := w.cw.Write(w.miniBuf); err != nil {
				return err
			}
			w.miniSecs = append(w.miniSecs, sec)
			w.miniBuf = w.miniBuf[:0]
		}
	}
	return nil
}

// A StorageWriter adds a storage to a CFB file. Set the exported fields to
// configure entry metadata.
type StorageWriter struct {
	// CLSID is the GUID identifying the COM class of the entry.
	CLSID [16]byte

	// StateBits is the application-defined user flags word for the entry, opaque to CFB.
	StateBits uint32

	// Created is the time the entry was created.
	Created time.Time

	// Modified is the time the entry was last modified.
	Modified time.Time

	w        *Writer
	name     []uint16 // pre-encoded UTF-16 LE name; reused for sort and emit
	storages []*StorageWriter
	streams  []*StreamWriter
}

// CreateStream adds a stream to the storage using the provided name and
// returns a [*StreamWriter] to which the stream contents should be written.
// The [StreamWriter] must be closed before [Writer.Close].
func (sw *StorageWriter) CreateStream(name string) (*StreamWriter, error) {
	sw.w.mu.Lock()
	defer sw.w.mu.Unlock()
	enc, err := sw.validateChildName(name)
	if err != nil {
		return nil, err
	}
	s := &StreamWriter{w: sw.w, name: enc}
	sw.streams = append(sw.streams, s)
	sw.w.descendants++
	return s, nil
}

// CreateStorage adds a child storage to the storage using the provided name
// and returns a [*StorageWriter].
func (sw *StorageWriter) CreateStorage(name string) (*StorageWriter, error) {
	sw.w.mu.Lock()
	defer sw.w.mu.Unlock()
	enc, err := sw.validateChildName(name)
	if err != nil {
		return nil, err
	}
	s := &StorageWriter{w: sw.w, name: enc}
	sw.storages = append(sw.storages, s)
	sw.w.descendants++
	return s, nil
}

// validateChildName returns the UTF-16 encoding of name. It returns an
// error if name is invalid, the writer is closed, or the storage already has
// a child by that name. Caller must hold sw.w.mu.
func (sw *StorageWriter) validateChildName(name string) ([]uint16, error) {
	if sw.w.closed {
		return nil, errors.New("cfb: writer closed")
	}
	enc, err := encodeName(name)
	if err != nil {
		return nil, err
	}
	for _, c := range sw.storages {
		if compareNames(enc, c.name) == 0 {
			return nil, errDuplicateName
		}
	}
	for _, c := range sw.streams {
		if compareNames(enc, c.name) == 0 {
			return nil, errDuplicateName
		}
	}
	return enc, nil
}

// AddFS adds the files from fs.FS to the storage.
// It walks the directory tree starting at the root of the filesystem
// adding each file to the CFB while maintaining the directory structure.
//
// When [fs.FileInfo.Sys] returns a [*Storage] or [*Stream], its metadata
// fields are preserved on the new entry.
func (sw *StorageWriter) AddFS(fsys fs.FS) error {
	return sw.addFSDir(fsys, ".")
}

func (sw *StorageWriter) addFSDir(fsys fs.FS, dir string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}
	for _, d := range entries {
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := d.Name()
		path := name
		if dir != "." {
			path = dir + "/" + name
		}
		if d.IsDir() {
			s, err := sw.CreateStorage(name)
			if err != nil {
				return err
			}
			if m, ok := info.Sys().(*Storage); ok {
				s.CLSID = m.CLSID
				s.StateBits = m.StateBits
				s.Created = m.Created
				s.Modified = m.Modified
			} else {
				s.Modified = info.ModTime()
				s.Created = birthtime(info)
			}
			if err := s.addFSDir(fsys, path); err != nil {
				return err
			}
			continue
		}
		if err := sw.addFSFile(fsys, path, name, info); err != nil {
			return err
		}
	}
	return nil
}

func (sw *StorageWriter) addFSFile(fsys fs.FS, path, name string, info fs.FileInfo) error {
	f, err := fsys.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := sw.CreateStream(name)
	if err != nil {
		return err
	}
	if m, ok := info.Sys().(*Stream); ok {
		s.StateBits = m.StateBits
	}
	if _, err := io.Copy(s, f); err != nil {
		return err
	}
	return s.Close()
}

// sectorRun is a contiguous run of sector indices in a stream's chain.
type sectorRun struct{ first, count uint32 }

// A StreamWriter adds a stream to a CFB file. Set the StateBits field to
// configure entry metadata.
type StreamWriter struct {
	// StateBits is the application-defined user flags word for the entry, opaque to CFB.
	StateBits uint32

	w      *Writer
	name   []uint16 // pre-encoded UTF-16 LE name; reused for sort and emit
	closed bool

	buf   []byte      // cap=miniStreamCutoff. Pre-promotion: full data; steady state: trailing partial.
	chain []sectorRun // file-sector runs (regular) or single mini-sector run (mini), filled on Close.
	size  uint64
}

// Write writes len(b) bytes from b to the stream.
// It returns the number of bytes written and an error, if any.
func (s *StreamWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("cfb: write on closed stream")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if s.w.secSize == 512 && s.size+uint64(len(p)) > streamSizeMaxV3 {
		return 0, errors.New("cfb: stream exceeds v3 2 GiB cap")
	}
	if s.buf == nil {
		s.buf = make([]byte, 0, miniStreamCutoff)
	}

	n := 0
	for len(p) > 0 {
		var room int
		switch {
		case s.size < miniStreamCutoff:
			room = miniStreamCutoff - int(s.size)
		case len(s.buf) > 0:
			room = s.w.secSize - len(s.buf)
		default:
			room = len(p) / s.w.secSize * s.w.secSize
		}
		if room == 0 || len(p) < room {
			s.buf = append(s.buf, p...)
			s.size += uint64(len(p))
			return n + len(p), nil
		}
		if err := s.flush(p[:room]); err != nil {
			return n, err
		}
		n += room
		s.size += uint64(room)
		p = p[room:]
	}
	return n, nil
}

// Close finishes writing the stream. It must be called before [Writer.Close].
func (s *StreamWriter) Close() error {
	if s.closed {
		return nil
	}
	if s.size == 0 {
		s.closed = true
		return nil
	}
	if s.size >= miniStreamCutoff {
		// Regular stream: flush s.buf + zero pad as the final partial sector.
		if len(s.buf) > 0 {
			pad := make([]byte, s.w.secSize-len(s.buf))
			if err := s.flush(pad); err != nil {
				return err
			}
		}
		s.buf = nil
		s.closed = true
		return nil
	}

	// Mini path: pad s.buf to a mini-sector boundary, then hand it to
	// w.writeMini, which drains it into the mini-stream backing buffer
	// and flushes each completed regular sector through cw.
	padded := s.buf
	if rem := len(padded) % miniSectorSize; rem != 0 {
		padded = append(padded, make([]byte, miniSectorSize-rem)...)
	}

	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	totalMini := uint64(len(s.w.miniSecs))*uint64(s.w.secSize) + uint64(len(s.w.miniBuf))
	first := uint32(totalMini / miniSectorSize)
	count := uint32(len(padded) / miniSectorSize)
	if err := s.w.writeMini(padded); err != nil {
		return err
	}
	s.chain = []sectorRun{{first: first, count: count}}
	s.buf = nil
	s.closed = true
	return nil
}

// flush appends s.buf followed by b to the stream. The combined length
// must be a non-zero multiple of secSize.
func (s *StreamWriter) flush(b []byte) error {
	sectors := uint32((len(s.buf) + len(b)) / s.w.secSize)
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	first := uint32(s.w.sectorIdx())
	if _, err := s.w.cw.Write(s.buf); err != nil {
		return err
	}
	if _, err := s.w.cw.Write(b); err != nil {
		return err
	}
	if n := len(s.chain); n > 0 && s.chain[n-1].first+s.chain[n-1].count == first {
		s.chain[n-1].count += sectors
	} else {
		s.chain = append(s.chain, sectorRun{first, sectors})
	}
	s.buf = s.buf[:0]
	return nil
}

// serializer holds the transient state for one [Writer.Close] pass.
type serializer struct {
	w    *Writer
	sect []byte

	miniStart                 uint32
	miniSize                  uint64
	dirStart, dirSecs         int64
	miniFATStart, miniFATSecs int64
	fatStart, fatCount        int64
	difatStart, difatCount    int64

	layout []layoutEntry
}

// layoutEntry is one directory entry's BST bookkeeping.
type layoutEntry struct{ leftSib, rightSib, childID uint32 }

// init prepares s for the emit phases.
func (s *serializer) init(w *Writer) error {
	s.w = w
	s.sect = make([]byte, w.secSize)
	s.miniStart = endOfChain

	// numMiniSecs counts logical (non-pad) mini sectors; capture before
	// the partial-sector flush below so the count excludes the zero-pad.
	numMiniSecs := uint64(len(w.miniSecs))*uint64(w.secSize)/miniSectorSize + uint64(len(w.miniBuf))/miniSectorSize
	if len(w.miniBuf) > 0 {
		if err := w.writeMini(make([]byte, w.secSize-len(w.miniBuf))); err != nil {
			return err
		}
	}
	if numMiniSecs > 0 {
		s.miniStart = w.miniSecs[0]
		s.miniSize = numMiniSecs * miniSectorSize
	}

	s.layout = make([]layoutEntry, w.descendants)
	return nil
}

// layoutDirectory assigns directory IDs and BST sibling/child pointers
// for every entry in the tree, populating s.layout.
func (s *serializer) layoutDirectory() {
	s.assignLayout(s.w.StorageWriter, new(uint32))
}

// assignLayout assigns sequential IDs and BST pointers for sw and its
// descendants. Returns sw's assigned ID.
func (s *serializer) assignLayout(sw *StorageWriter, counter *uint32) uint32 {
	ownID := *counter
	*counter++
	s.layout[ownID] = layoutEntry{leftSib: noStream, rightSib: noStream, childID: noStream}

	slices.SortFunc(sw.streams, func(a, b *StreamWriter) int { return compareNames(a.name, b.name) })
	slices.SortFunc(sw.storages, func(a, b *StorageWriter) int { return compareNames(a.name, b.name) })

	childIDs := make([]uint32, 0, len(sw.streams)+len(sw.storages))
	si, ti := 0, 0
	for si < len(sw.streams) || ti < len(sw.storages) {
		pickStream := ti == len(sw.storages) ||
			(si < len(sw.streams) && compareNames(sw.streams[si].name, sw.storages[ti].name) < 0)
		if pickStream {
			id := *counter
			*counter++
			s.layout[id] = layoutEntry{leftSib: noStream, rightSib: noStream, childID: noStream}
			childIDs = append(childIDs, id)
			si++
		} else {
			childIDs = append(childIDs, s.assignLayout(sw.storages[ti], counter))
			ti++
		}
	}
	s.layout[ownID].childID = linkBST(s.layout, childIDs, 0, len(childIDs)-1)
	return ownID
}

// emitDirectory writes the directory region.
//
//nolint:funlen,gocognit // single coherent walk
func (s *serializer) emitDirectory() error {
	s.layoutDirectory()
	s.dirStart = s.w.sectorIdx()
	perDir := s.w.secSize / direntrySize

	var dbuf [direntrySize]byte
	var counter uint32
	var emitted int

	var emit func(sw *StorageWriter, isRoot bool) error
	emit = func(sw *StorageWriter, isRoot bool) error {
		id := counter
		counter++
		de := direntry{
			name:       sw.name,
			clsid:      sw.CLSID,
			stateBits:  sw.StateBits,
			created:    timeToFiletime(sw.Created),
			modified:   timeToFiletime(sw.Modified),
			objectType: objectStorage,
			leftSib:    s.layout[id].leftSib,
			rightSib:   s.layout[id].rightSib,
			childID:    s.layout[id].childID,
			startSec:   endOfChain,
		}
		if isRoot {
			de.created = 0 // the root entry must keep Created zero
			de.objectType = objectRoot
			if s.miniSize > 0 {
				de.startSec = s.miniStart
				de.size = s.miniSize
			}
		}
		encodeDirentry(dbuf[:], de)
		if _, err := s.w.cw.Write(dbuf[:]); err != nil {
			return err
		}
		emitted++

		si, ti := 0, 0
		for si < len(sw.streams) || ti < len(sw.storages) {
			pickStream := ti == len(sw.storages) ||
				(si < len(sw.streams) && compareNames(sw.streams[si].name, sw.storages[ti].name) < 0)
			if pickStream {
				v := sw.streams[si]
				strID := counter
				counter++
				de := direntry{
					name:       v.name,
					stateBits:  v.StateBits,
					size:       v.size,
					objectType: objectStream,
					leftSib:    s.layout[strID].leftSib,
					rightSib:   s.layout[strID].rightSib,
					childID:    noStream,
					startSec:   endOfChain,
				}
				if v.size > 0 {
					de.startSec = v.chain[0].first
				}
				encodeDirentry(dbuf[:], de)
				if _, err := s.w.cw.Write(dbuf[:]); err != nil {
					return err
				}
				emitted++
				si++
			} else {
				if err := emit(sw.storages[ti], false); err != nil {
					return err
				}
				ti++
			}
		}
		return nil
	}
	if err := emit(s.w.StorageWriter, true); err != nil {
		return err
	}

	// Pad the final partial sector with empty direntries so the directory
	// region ends on a sector boundary.
	for emitted%perDir != 0 {
		if _, err := s.w.cw.Write(emptyDirentry[:]); err != nil {
			return err
		}
		emitted++
	}

	s.dirSecs = s.w.sectorIdx() - s.dirStart
	return nil
}

// emitFAT writes the miniFAT and FAT regions.
//
//nolint:funlen
func (s *serializer) emitFAT() error {
	w := s.w
	const metadataRegions = 4 // dir, miniFAT, FAT, DIFAT, appended below
	fatRuns := make([]fatRun, 0, int(w.descendants)+len(w.miniSecs)+metadataRegions)
	var miniRuns []fatRun
	if s.miniSize > 0 {
		miniRuns = make([]fatRun, 0, w.descendants)
	}
	var visit func(*StorageWriter)
	visit = func(sw *StorageWriter) {
		for _, st := range sw.streams {
			switch {
			case st.size == 0:
				continue
			case st.size < miniStreamCutoff:
				r := st.chain[0]
				miniRuns = append(miniRuns, fatRun{first: r.first, count: r.count, terminal: endOfChain, linear: true})
			default:
				for i, r := range st.chain {
					term := endOfChain
					if i+1 < len(st.chain) {
						term = st.chain[i+1].first
					}
					fatRuns = append(fatRuns, fatRun{first: r.first, count: r.count, terminal: term, linear: true})
				}
			}
		}
		for _, sub := range sw.storages {
			visit(sub)
		}
	}
	visit(w.StorageWriter)

	// MiniFAT region.
	s.miniFATStart = w.sectorIdx()
	if s.miniSize > 0 {
		perWord := uint32(w.secSize / 4)
		numMini := uint32(s.miniSize / miniSectorSize)
		s.miniFATSecs = int64((numMini + perWord - 1) / perWord)
		if err := s.writeChainTable(s.miniFATSecs, miniRuns); err != nil {
			return err
		}
	}

	// FAT region.
	perWord := int64(w.secSize) / 4
	prefix := w.sectorIdx()
	s.fatCount, s.difatCount = computeFATCounts(prefix, perWord)
	s.fatStart = prefix
	s.difatStart = s.fatStart + s.fatCount

	for i := 0; i < len(w.miniSecs); {
		j := i + 1
		for j < len(w.miniSecs) && w.miniSecs[j] == w.miniSecs[j-1]+1 {
			j++
		}
		term := endOfChain
		if j < len(w.miniSecs) {
			term = w.miniSecs[j]
		}
		fatRuns = append(fatRuns, fatRun{first: w.miniSecs[i], count: uint32(j - i), terminal: term, linear: true})
		i = j
	}

	fatRuns = append(fatRuns,
		fatRun{first: uint32(s.dirStart), count: uint32(s.dirSecs), terminal: endOfChain, linear: true},
		fatRun{first: uint32(s.miniFATStart), count: uint32(s.miniFATSecs), terminal: endOfChain, linear: true},
		fatRun{first: uint32(s.fatStart), count: uint32(s.fatCount), terminal: fatSect},
		fatRun{first: uint32(s.difatStart), count: uint32(s.difatCount), terminal: difSect},
	)
	return s.writeChainTable(s.fatCount, fatRuns)
}

// writeChainTable writes numSectors FAT-shaped sectors. Gaps between
// runs are filled with freeSect.
func (s *serializer) writeChainTable(numSectors int64, runs []fatRun) error {
	slices.SortFunc(runs, func(a, b fatRun) int { return cmp.Compare(a.first, b.first) })
	perWord := uint32(s.w.secSize / 4)
	cursor := 0
	for k := range numSectors {
		base := uint32(k) * perWord
		end := base + perWord
		for j := range perWord {
			binary.LittleEndian.PutUint32(s.sect[j*4:], freeSect)
		}
		for cursor < len(runs) && runs[cursor].first+runs[cursor].count <= base {
			cursor++
		}
		for j := cursor; j < len(runs) && runs[j].first < end; j++ {
			r := runs[j]
			lo := max(base, r.first)
			hi := min(end, r.first+r.count)
			last := r.first + r.count - 1
			for i := lo; i < hi; i++ {
				v := r.terminal
				if r.linear && i != last {
					v = i + 1
				}
				binary.LittleEndian.PutUint32(s.sect[(i-base)*4:], v)
			}
		}
		if _, err := s.w.cw.Write(s.sect); err != nil {
			return err
		}
	}
	return nil
}

// emitDIFAT writes the DIFAT region.
func (s *serializer) emitDIFAT() error {
	w := s.w
	perDIFAT := int64(w.secSize/4) - 1
	for i := range s.difatCount {
		clear(s.sect)
		b := writeBuf(s.sect)
		for j := range perDIFAT {
			fatIdx := int64(inlineDIFAT) + i*perDIFAT + j
			v := freeSect
			if fatIdx < s.fatCount {
				v = uint32(s.fatStart + fatIdx)
			}
			b.uint32(v)
		}
		next := endOfChain
		if i+1 < s.difatCount {
			next = uint32(s.difatStart + i + 1)
		}
		b.uint32(next)
		if _, err := w.cw.Write(s.sect); err != nil {
			return err
		}
	}
	return nil
}

// patchHeader writes the file header at offset 0.
func (s *serializer) patchHeader() error {
	w := s.w
	if _, err := w.raw.Seek(0, io.SeekStart); err != nil {
		return err
	}
	major := uint16(3)
	if w.secSize == 4096 {
		major = 4
	}
	var hbuf [headerSize]byte
	b := writeBuf(hbuf[:])
	b.uint64(headerSignature)
	_ = b.sub(16) // CLSID: zero
	b.uint16(minorVersion)
	b.uint16(major)
	b.uint16(byteOrderLE)
	b.uint16(uint16(bits.TrailingZeros(uint(w.secSize))))
	b.uint16(miniSectorShift)
	_ = b.sub(6) // reserved
	if major == 4 {
		b.uint32(uint32(s.dirSecs))
	} else {
		b.uint32(0)
	}
	b.uint32(uint32(s.fatCount))
	b.uint32(uint32(s.dirStart))
	b.uint32(0) // TxnSignature
	b.uint32(miniStreamCutoff)
	if s.miniFATSecs > 0 {
		b.uint32(uint32(s.miniFATStart))
	} else {
		b.uint32(endOfChain)
	}
	b.uint32(uint32(s.miniFATSecs))
	if s.difatCount > 0 {
		b.uint32(uint32(s.difatStart))
	} else {
		b.uint32(endOfChain)
	}
	b.uint32(uint32(s.difatCount))
	inline := min(int(s.fatCount), inlineDIFAT)
	for i := range inline {
		b.uint32(uint32(s.fatStart) + uint32(i))
	}
	for range inlineDIFAT - inline {
		b.uint32(freeSect)
	}
	if _, err := w.raw.Write(hbuf[:]); err != nil {
		return err
	}
	if pad := w.secSize - headerSize; pad > 0 {
		if _, err := w.raw.Write(make([]byte, pad)); err != nil {
			return err
		}
	}
	return nil
}

// direntry holds the fields encoded into one on-disk directory entry.
type direntry struct {
	name       []uint16
	clsid      [16]byte
	stateBits  uint32
	created    uint64
	modified   uint64
	size       uint64
	objectType uint8
	leftSib    uint32
	rightSib   uint32
	childID    uint32
	startSec   uint32
}

// linkBST links ids as a balanced BST in layout, returning the root ID.
func linkBST(layout []layoutEntry, ids []uint32, lo, hi int) uint32 {
	if lo > hi {
		return noStream
	}
	mid := (lo + hi) / 2
	id := ids[mid]
	layout[id].leftSib = linkBST(layout, ids, lo, mid-1)
	layout[id].rightSib = linkBST(layout, ids, mid+1, hi)
	return id
}

// encodeDirentry writes a directory entry into buf.
func encodeDirentry(buf []byte, de direntry) {
	b := writeBuf(buf)
	for _, u := range de.name {
		b.uint16(u)
	}
	clear(b.sub(2 * (nameMaxUnits - len(de.name))))
	b.uint16(uint16((len(de.name) + 1) * 2))
	b.uint8(de.objectType)
	b.uint8(colorBlack)
	b.uint32(de.leftSib)
	b.uint32(de.rightSib)
	b.uint32(de.childID)
	copy(b.sub(16), de.clsid[:])
	b.uint32(de.stateBits)
	b.uint64(de.created)
	b.uint64(de.modified)
	b.uint32(de.startSec)
	b.uint64(de.size)
}

// emptyDirentry is a precomputed unallocated direntry.
var emptyDirentry = func() [direntrySize]byte {
	var b [direntrySize]byte
	binary.LittleEndian.PutUint32(b[68:], noStream) // leftSib
	binary.LittleEndian.PutUint32(b[72:], noStream) // rightSib
	binary.LittleEndian.PutUint32(b[76:], noStream) // childID
	return b
}()

// fatRun describes one stretch of FAT or miniFAT entries. When linear,
// each entry but the last links to its successor; otherwise every entry
// is set to terminal.
type fatRun struct {
	first, count, terminal uint32
	linear                 bool
}

// computeFATCounts returns how many FAT and DIFAT sectors the file needs.
func computeFATCounts(prefixSectors, perFAT int64) (fatSectors, difatSectors int64) {
	for {
		total := prefixSectors + fatSectors + difatSectors
		nextFAT := max((total+perFAT-1)/perFAT, 1)
		nextDIFAT := int64(0)
		if nextFAT > inlineDIFAT {
			nextDIFAT = (nextFAT - inlineDIFAT + perFAT - 2) / (perFAT - 1)
		}
		if nextFAT == fatSectors && nextDIFAT == difatSectors {
			return nextFAT, nextDIFAT
		}
		fatSectors, difatSectors = nextFAT, nextDIFAT
	}
}

// countWriter wraps an io.Writer and tallies bytes written.
type countWriter struct {
	w     io.Writer
	count int64
}

func (w *countWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.count += int64(n)
	return n, err
}

// seekOnce performs the initial Seek-past-header on its first Write, then
// just forwards subsequent writes.
type seekOnce struct {
	seeker io.WriteSeeker
	offset int64
	done   bool
}

func (s *seekOnce) Write(p []byte) (int, error) {
	if !s.done {
		if _, err := s.seeker.Seek(s.offset, io.SeekStart); err != nil {
			return 0, err
		}
		s.done = true
	}
	return s.seeker.Write(p)
}

// writeBuf writes little-endian fixed-width values and advances itself.
type writeBuf []byte

func (b *writeBuf) uint8(v uint8) {
	(*b)[0] = v
	*b = (*b)[1:]
}

func (b *writeBuf) uint16(v uint16) {
	binary.LittleEndian.PutUint16(*b, v)
	*b = (*b)[2:]
}

func (b *writeBuf) uint32(v uint32) {
	binary.LittleEndian.PutUint32(*b, v)
	*b = (*b)[4:]
}

func (b *writeBuf) uint64(v uint64) {
	binary.LittleEndian.PutUint64(*b, v)
	*b = (*b)[8:]
}

func (b *writeBuf) sub(n int) writeBuf {
	out := (*b)[:n]
	*b = (*b)[n:]
	return out
}
