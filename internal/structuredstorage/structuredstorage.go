//go:build windows && (amd64 || arm64)

// Package structuredstorage wraps Windows' IStorage / IStream / IPropertySetStorage
// COM API from ole32.dll.
package structuredstorage

import (
	"io"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Version selects v3 (512-byte) or v4 (4096-byte) sectors when creating.
type Version int

// Compound-file format versions.
const (
	V3 Version = iota
	V4
)

// Type identifies an entry as a storage or stream.
type Type uint32

// STGTY_* values for [EntryInfo.Type].
const (
	TypeStorage Type = 1 // STGTY_STORAGE
	TypeStream  Type = 2 // STGTY_STREAM
)

// EntryInfo holds the metadata for one child entry.
type EntryInfo struct {
	Name      string
	Type      Type
	Size      int64
	CLSID     [16]byte
	StateBits uint32
	Created   time.Time
	Modified  time.Time
}

const (
	stgmRead           = 0x00000000
	stgmReadWrite      = 0x00000002
	stgmShareExcl      = 0x00000010
	stgmShareDenyWrite = 0x00000020
	stgmCreate         = 0x00001000
	stgmDirect         = 0x00000000

	stgfmtStorage = 0
	stgfmtDocfile = 5

	propsetflagAnsi = 0x2

	prspecPropID    = 1 // PROPSPEC.ulKind
	propidNameFirst = 2 // WriteMultiple's first usable PROPID
)

// Supported PROPVARIANT type tags.
const (
	vtI2       = 0x02
	vtI4       = 0x03
	vtUI4      = 0x13
	vtLPSTR    = 0x1E
	vtFiletime = 0x40
)

var (
	ole32                  = syscall.NewLazyDLL("ole32.dll")
	procStgCreateStorageEx = ole32.NewProc("StgCreateStorageEx")
	procStgOpenStorageEx   = ole32.NewProc("StgOpenStorageEx")
	procCoInitialize       = ole32.NewProc("CoInitialize")
	procCoTaskMemFree      = ole32.NewProc("CoTaskMemFree")

	iidIStorage = syscall.GUID{
		Data1: 0x0000000B,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	iidIPropertySetStorage = syscall.GUID{
		Data1: 0x0000013A,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
)

func init() {
	procCoInitialize.Call(0)
}

// stgOptions mirrors the STGOPTIONS struct from objbase.h. Pass to
// StgCreateStorageEx to select v4 (4 KiB sectors).
type stgOptions struct {
	usVersion        uint16
	reserved         uint16
	ulSectorSize     uint32
	pwcsTemplateFile *uint16
}

// statstg mirrors the STATSTG struct from objidl.h.
type statstg struct {
	pwcsName          *uint16
	stgType           uint32
	cbSize            uint64
	mtime             syscall.Filetime
	ctime             syscall.Filetime
	atime             syscall.Filetime
	grfMode           uint32
	grfLocksSupported uint32
	clsid             syscall.GUID
	grfStateBits      uint32
	reserved          uint32
}

// propspec mirrors the PROPSPEC struct from propidl.h.
type propspec struct {
	ulKind uint32
	_      uint32 // pad: union is 8-byte aligned on 64-bit Windows
	propid uint32 // union arm (PRSPEC_PROPID); lpwstr arm unused
	_      uint32 // upper half of the 8-byte union
}

// propvariant mirrors the PROPVARIANT struct from propidl.h.
type propvariant struct {
	vt  uint16
	_   [3]uint16 // wReserved1..3
	val [2]uint64 // union (only scalar arms used)
}

type iStorageVtbl struct {
	queryInterface  uintptr
	_               uintptr // addRef
	release         uintptr
	createStream    uintptr
	openStream      uintptr
	createStorage   uintptr
	openStorage     uintptr
	_               uintptr // copyTo
	_               uintptr // moveElementTo
	commit          uintptr
	_               uintptr // revert
	enumElements    uintptr
	_               uintptr // destroyElement
	_               uintptr // renameElement
	setElementTimes uintptr
	setClass        uintptr
	setStateBits    uintptr
	stat            uintptr
}

// Storage wraps an IStorage* COM pointer.
type Storage struct {
	vtbl *iStorageVtbl
}

type iEnumSTATSTGVtbl struct {
	_       uintptr // queryInterface
	_       uintptr // addRef
	release uintptr
	next    uintptr
	_       uintptr // skip
	_       uintptr // reset
	_       uintptr // clone
}

type iEnumSTATSTG struct {
	vtbl *iEnumSTATSTGVtbl
}

type iStreamVtbl struct {
	_       uintptr // queryInterface
	_       uintptr // addRef
	release uintptr
	read    uintptr
	write   uintptr
	seek    uintptr
	_       uintptr // setSize
	_       uintptr // copyTo
	_       uintptr // commit
	_       uintptr // revert
	_       uintptr // lockRegion
	_       uintptr // unlockRegion
	stat    uintptr
	_       uintptr // clone
}

// Stream wraps an IStream* COM pointer.
type Stream struct {
	vtbl *iStreamVtbl
}

type iPropertySetStorageVtbl struct {
	_       uintptr // queryInterface
	_       uintptr // addRef
	release uintptr
	create  uintptr
}

// PropertySetStorage wraps an IPropertySetStorage* COM pointer.
type PropertySetStorage struct {
	vtbl *iPropertySetStorageVtbl
}

type iPropertyStorageVtbl struct {
	_             uintptr // queryInterface
	_             uintptr // addRef
	release       uintptr
	_             uintptr // readMultiple
	writeMultiple uintptr
	_             uintptr // deleteMultiple
	_             uintptr // readPropertyNames
	_             uintptr // writePropertyNames
	_             uintptr // deletePropNames
	commit        uintptr
}

// PropertyStorage wraps an IPropertyStorage* COM pointer.
type PropertyStorage struct {
	vtbl *iPropertyStorageVtbl
}

// Prop is one (PROPID, value) pair for WriteMultiple.
type Prop struct {
	id  uint32
	pv  propvariant
	pin any // keeps an LPSTR buffer alive across the call
}

// Create creates a new compound file at path with the given version.
// Existing files are overwritten.
func Create(path string, v Version) (*Storage, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmReadWrite | stgmShareExcl | stgmCreate | stgmDirect)
	var opts *stgOptions
	if v == V4 {
		opts = &stgOptions{
			usVersion:    1,
			ulSectorSize: 4096,
		}
	}
	var stg *Storage
	r, _, _ := procStgCreateStorageEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(mode),
		uintptr(stgfmtDocfile),
		0, // grfAttrs: reserved, must be 0
		uintptr(unsafe.Pointer(opts)),
		0, // pSecurityDescriptor: NULL
		uintptr(unsafe.Pointer(&iidIStorage)),
		uintptr(unsafe.Pointer(&stg)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stg, nil
}

// Open opens an existing compound file at path read-only.
func Open(path string) (*Storage, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmRead | stgmShareDenyWrite)
	var stg *Storage
	r, _, _ := procStgOpenStorageEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(mode),
		uintptr(stgfmtStorage),
		0, // grfAttrs: reserved, must be 0
		0, // pStgOptions: NULL (use defaults)
		0, // pSecurityDescriptor: NULL
		uintptr(unsafe.Pointer(&iidIStorage)),
		uintptr(unsafe.Pointer(&stg)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stg, nil
}

// Close releases the storage.
func (s *Storage) Close() {
	syscall.SyscallN(s.vtbl.release, uintptr(unsafe.Pointer(s)))
}

// Commit flushes buffered changes.
func (s *Storage) Commit() error {
	r, _, _ := syscall.SyscallN(s.vtbl.commit,
		uintptr(unsafe.Pointer(s)),
		0, // grfCommitFlags: STGC_DEFAULT
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// CreateStream creates a new stream child named name.
func (s *Storage) CreateStream(name string) (*Stream, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmReadWrite | stgmShareExcl)
	var stm *Stream
	r, _, _ := syscall.SyscallN(s.vtbl.createStream,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(mode),
		0, // reserved1: must be 0
		0, // reserved2: must be 0
		uintptr(unsafe.Pointer(&stm)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stm, nil
}

// OpenStream opens an existing stream child named name read-only.
func (s *Storage) OpenStream(name string) (*Stream, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmRead | stgmShareExcl)
	var stm *Stream
	r, _, _ := syscall.SyscallN(s.vtbl.openStream,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		0, // reserved1: must be NULL
		uintptr(mode),
		0, // reserved2: must be 0
		uintptr(unsafe.Pointer(&stm)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stm, nil
}

// CreateStorage creates a new substorage child named name.
func (s *Storage) CreateStorage(name string) (*Storage, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmReadWrite | stgmShareExcl)
	var sub *Storage
	r, _, _ := syscall.SyscallN(s.vtbl.createStorage,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(mode),
		0, // reserved1: must be 0
		0, // reserved2: must be 0
		uintptr(unsafe.Pointer(&sub)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return sub, nil
}

// OpenStorage opens an existing substorage child named name read-only.
func (s *Storage) OpenStorage(name string) (*Storage, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmRead | stgmShareExcl)
	var sub *Storage
	r, _, _ := syscall.SyscallN(s.vtbl.openStorage,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		0, // pstgPriority: NULL
		uintptr(mode),
		0, // snbExclude: NULL
		0, // reserved: must be 0
		uintptr(unsafe.Pointer(&sub)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return sub, nil
}

// SetClass sets the CLSID of this storage.
func (s *Storage) SetClass(clsid [16]byte) error {
	r, _, _ := syscall.SyscallN(s.vtbl.setClass,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&clsid)),
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// SetStateBits replaces all state bits with bits.
func (s *Storage) SetStateBits(bits uint32) error {
	r, _, _ := syscall.SyscallN(s.vtbl.setStateBits,
		uintptr(unsafe.Pointer(s)),
		uintptr(bits),
		uintptr(uint32(0xFFFFFFFF)), // grfMask: replace all bits
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// SetElementTimes updates the timestamps of the named child entry, or of the
// root storage itself when name is empty (NULL pwcsName). A zero time.Time
// produces FILETIME{0,0}, which Windows treats as "leave unchanged".
func (s *Storage) SetElementTimes(name string, created, accessed, modified time.Time) error {
	var namePtr *uint16
	if name != "" {
		var err error
		namePtr, err = syscall.UTF16PtrFromString(name)
		if err != nil {
			return err
		}
	}
	var pCreated, pAccessed, pModified syscall.Filetime
	if !created.IsZero() {
		pCreated = syscall.NsecToFiletime(created.UnixNano())
	}
	if !accessed.IsZero() {
		pAccessed = syscall.NsecToFiletime(accessed.UnixNano())
	}
	if !modified.IsZero() {
		pModified = syscall.NsecToFiletime(modified.UnixNano())
	}
	r, _, _ := syscall.SyscallN(s.vtbl.setElementTimes,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&pCreated)),
		uintptr(unsafe.Pointer(&pAccessed)),
		uintptr(unsafe.Pointer(&pModified)),
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// Stat returns metadata for this storage.
func (s *Storage) Stat() (EntryInfo, error) {
	var st statstg
	r, _, _ := syscall.SyscallN(s.vtbl.stat,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&st)),
		0, // grfStatFlag: STATFLAG_DEFAULT
	)
	if r != 0 {
		return EntryInfo{}, syscall.Errno(r)
	}
	return entryFromStat(&st), nil
}

// Entries enumerates all direct children of s.
func (s *Storage) Entries() ([]EntryInfo, error) {
	var enum *iEnumSTATSTG
	r, _, _ := syscall.SyscallN(s.vtbl.enumElements,
		uintptr(unsafe.Pointer(s)),
		0, // reserved1: must be 0
		0, // reserved2: must be NULL
		0, // reserved3: must be 0
		uintptr(unsafe.Pointer(&enum)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	defer syscall.SyscallN(enum.vtbl.release, uintptr(unsafe.Pointer(enum)))
	var out []EntryInfo
	for {
		var st statstg
		var fetched uint32
		r, _, _ := syscall.SyscallN(enum.vtbl.next,
			uintptr(unsafe.Pointer(enum)),
			1, // celt: number of entries to fetch
			uintptr(unsafe.Pointer(&st)),
			uintptr(unsafe.Pointer(&fetched)),
		)
		if r != 0 && r != 1 { // 1 = S_FALSE: no more entries
			return nil, syscall.Errno(r)
		}
		if fetched == 0 {
			break
		}
		out = append(out, entryFromStat(&st))
	}
	return out, nil
}

// PropertySetStorage queries the root storage for its IPropertySetStorage.
func (s *Storage) PropertySetStorage() (*PropertySetStorage, error) {
	var pss *PropertySetStorage
	r, _, _ := syscall.SyscallN(s.vtbl.queryInterface,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&iidIPropertySetStorage)),
		uintptr(unsafe.Pointer(&pss)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return pss, nil
}

// Close releases the stream.
func (s *Stream) Close() {
	syscall.SyscallN(s.vtbl.release, uintptr(unsafe.Pointer(s)))
}

// Stat returns metadata for this stream.
func (s *Stream) Stat() (EntryInfo, error) {
	var st statstg
	r, _, _ := syscall.SyscallN(s.vtbl.stat,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&st)),
		0, // grfStatFlag: STATFLAG_DEFAULT
	)
	if r != 0 {
		return EntryInfo{}, syscall.Errno(r)
	}
	return entryFromStat(&st), nil
}

// Read implements [io.Reader].
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var n uint32
	r, _, _ := syscall.SyscallN(s.vtbl.read,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(unsafe.SliceData(p))),
		uintptr(len(p)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r != 0 && r != 1 { // 1 = S_FALSE: short read at EOF
		return int(n), syscall.Errno(r)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

// Write implements [io.Writer].
func (s *Stream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var written uint32
	r, _, _ := syscall.SyscallN(s.vtbl.write,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(unsafe.SliceData(p))),
		uintptr(len(p)),
		uintptr(unsafe.Pointer(&written)),
	)
	if r != 0 {
		return int(written), syscall.Errno(r)
	}
	if int(written) != len(p) {
		return int(written), io.ErrShortWrite
	}
	return int(written), nil
}

// Seek implements [io.Seeker].
func (s *Stream) Seek(offset int64, whence int) (int64, error) {
	var newPos uint64
	r, _, _ := syscall.SyscallN(s.vtbl.seek,
		uintptr(unsafe.Pointer(s)),
		uintptr(offset),
		uintptr(whence),
		uintptr(unsafe.Pointer(&newPos)),
	)
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return int64(newPos), nil
}

// Close releases the property set storage.
func (p *PropertySetStorage) Close() {
	syscall.SyscallN(p.vtbl.release, uintptr(unsafe.Pointer(p)))
}

// Create creates a property set with fmtid and clsid.
func (p *PropertySetStorage) Create(fmtid, clsid [16]byte) (*PropertyStorage, error) {
	var ps *PropertyStorage
	r, _, _ := syscall.SyscallN(p.vtbl.create,
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&fmtid)),
		uintptr(unsafe.Pointer(&clsid)),
		uintptr(propsetflagAnsi),
		uintptr(stgmCreate|stgmReadWrite|stgmShareExcl),
		uintptr(unsafe.Pointer(&ps)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return ps, nil
}

// Close releases the property storage.
func (p *PropertyStorage) Close() {
	syscall.SyscallN(p.vtbl.release, uintptr(unsafe.Pointer(p)))
}

// Commit flushes the property storage.
func (p *PropertyStorage) Commit() error {
	r, _, _ := syscall.SyscallN(p.vtbl.commit,
		uintptr(unsafe.Pointer(p)),
		0, // STGC_DEFAULT
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// WriteMultiple writes props.
func (p *PropertyStorage) WriteMultiple(props []Prop) error {
	if len(props) == 0 {
		return nil
	}
	specs := make([]propspec, len(props))
	vars := make([]propvariant, len(props))
	for i, pr := range props {
		specs[i] = propspec{ulKind: prspecPropID, propid: pr.id}
		vars[i] = pr.pv
	}
	r, _, _ := syscall.SyscallN(p.vtbl.writeMultiple,
		uintptr(unsafe.Pointer(p)),
		uintptr(len(props)),
		uintptr(unsafe.Pointer(&specs[0])),
		uintptr(unsafe.Pointer(&vars[0])),
		uintptr(propidNameFirst),
	)
	runtime.KeepAlive(props)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// PropI2 builds a VT_I2 property.
func PropI2(id uint32, v int16) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtI2}}
	*(*int16)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropI4 builds a VT_I4 property.
func PropI4(id uint32, v int32) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtI4}}
	*(*int32)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropUI4 builds a VT_UI4 property.
func PropUI4(id, v uint32) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtUI4}}
	*(*uint32)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropFiletime builds a VT_FILETIME property from t. A zero t produces
// FILETIME{0,0}.
func PropFiletime(id uint32, t time.Time) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtFiletime}}
	if !t.IsZero() {
		ft := syscall.NsecToFiletime(t.UnixNano())
		p.pv.val[0] = uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	}
	return p
}

// PropLPSTR builds a VT_LPSTR property. s must already be code-page bytes,
// not UTF-8.
func PropLPSTR(id uint32, s string) Prop {
	cstr := append([]byte(s), 0)
	p := Prop{id: id, pin: cstr, pv: propvariant{vt: vtLPSTR}}
	p.pv.val[0] = uint64(uintptr(unsafe.Pointer(&cstr[0])))
	return p
}

// entryFromStat copies a STATSTG into an [EntryInfo] and frees pwcsName.
func entryFromStat(st *statstg) EntryInfo {
	info := EntryInfo{
		Name:      utf16PtrToString(st.pwcsName),
		Type:      Type(st.stgType),
		Size:      int64(st.cbSize),
		CLSID:     *(*[16]byte)(unsafe.Pointer(&st.clsid)),
		StateBits: st.grfStateBits,
		Created:   filetimeToTime(st.ctime),
		Modified:  filetimeToTime(st.mtime),
	}
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(st.pwcsName)))
	return info
}

// filetimeToTime converts a FILETIME to a UTC [time.Time]. A zero FILETIME
// returns the zero time.
func filetimeToTime(ft syscall.Filetime) time.Time {
	if ft.LowDateTime == 0 && ft.HighDateTime == 0 {
		return time.Time{}
	}
	return time.Unix(0, ft.Nanoseconds()).UTC()
}

// utf16PtrToString reads a NUL-terminated UTF-16 string from p.
func utf16PtrToString(p *uint16) string {
	if p == nil || *p == 0 {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, unsafe.Sizeof(*p))
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
