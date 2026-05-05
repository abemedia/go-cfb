// Package istorage wraps Windows' IStorage / IStream COM API from ole32.dll.
package istorage

import (
	"io"
	"syscall"
	"time"
	"unsafe"
)

// Version selects v3 (512-byte) or v4 (4096-byte) sectors when creating.
type Version int

const (
	V3 Version = iota
	V4
)

// Type identifies an entry as a storage or stream.
type Type uint32

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

// statstg mirrors the STATSTG struct from objidl.h. Layout matters.
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

type iStorageVtbl struct {
	queryInterface  uintptr
	addRef          uintptr
	release         uintptr
	createStream    uintptr
	openStream      uintptr
	createStorage   uintptr
	openStorage     uintptr
	copyTo          uintptr
	moveElementTo   uintptr
	commit          uintptr
	revert          uintptr
	enumElements    uintptr
	destroyElement  uintptr
	renameElement   uintptr
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
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	next           uintptr
	skip           uintptr
	reset          uintptr
	clone          uintptr
}

type iEnumSTATSTG struct {
	vtbl *iEnumSTATSTGVtbl
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

func filetimeToTime(ft syscall.Filetime) time.Time {
	if ft.LowDateTime == 0 && ft.HighDateTime == 0 {
		return time.Time{}
	}
	return time.Unix(0, ft.Nanoseconds()).UTC()
}

func utf16PtrToString(p *uint16) string {
	if p == nil || *p == 0 {
		return ""
	}
	// Find NUL terminator.
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, unsafe.Sizeof(*p))
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

type iStreamVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	read           uintptr
	write          uintptr
	seek           uintptr
	setSize        uintptr
	copyTo         uintptr
	commit         uintptr
	revert         uintptr
	lockRegion     uintptr
	unlockRegion   uintptr
	stat           uintptr
	clone          uintptr
}

// Stream wraps an IStream* COM pointer.
type Stream struct {
	vtbl *iStreamVtbl
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
	var r uintptr
	if unsafe.Sizeof(uintptr(0)) == 8 {
		r, _, _ = syscall.SyscallN(
			s.vtbl.seek,
			uintptr(unsafe.Pointer(s)),
			uintptr(offset),
			uintptr(whence),
			uintptr(unsafe.Pointer(&newPos)),
		)
	} else {
		// offset is a LARGE_INTEGER split into low/high DWORDs on 32-bit.
		r, _, _ = syscall.SyscallN(s.vtbl.seek,
			uintptr(unsafe.Pointer(s)),
			uintptr(offset),
			uintptr(offset>>32),
			uintptr(whence),
			uintptr(unsafe.Pointer(&newPos)),
		)
	}
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return int64(newPos), nil
}
