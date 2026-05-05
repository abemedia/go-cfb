// Package cfbtest provides utilities for CFB testing.
package cfbtest

import (
	"crypto/sha256"
	"fmt"
	"io"
	"testing"
	"time"
)

// Equal fails t if want and got describe different CFB trees.
func Equal[T1, T2 storage](t *testing.T, want T1, got T2) {
	t.Helper()
	if err := compare("", buildEntry(t, want), buildEntry(t, got)); err != nil {
		t.Error(err)
	}
}

type entry struct {
	Name        string
	Type        string
	CLSID       [16]byte
	StateBits   uint32
	Created     time.Time
	Modified    time.Time
	Size        int64
	ContentHash [32]byte
	Children    []entry
}

const (
	entryTypeStorage = "storage"
	entryTypeStream  = "stream"
)

func compare(path string, want, got entry) error {
	if want.Name != got.Name {
		return fmt.Errorf("%s: name = %q, want %q", path, got.Name, want.Name)
	}
	if want.Type != got.Type {
		return fmt.Errorf("%s: type = %q, want %q", path, got.Type, want.Type)
	}
	if want.CLSID != got.CLSID {
		return fmt.Errorf("%s: CLSID = %x, want %x", path, got.CLSID, want.CLSID)
	}
	if want.StateBits != got.StateBits {
		return fmt.Errorf("%s: StateBits = %#x, want %#x", path, got.StateBits, want.StateBits)
	}
	if !want.Created.Equal(got.Created) {
		return fmt.Errorf("%s: Created = %v, want %v", path, got.Created, want.Created)
	}
	if !want.Modified.Equal(got.Modified) {
		return fmt.Errorf("%s: Modified = %v, want %v", path, got.Modified, want.Modified)
	}
	if want.Type == entryTypeStream {
		if want.Size != got.Size {
			return fmt.Errorf("%s: Size = %d, want %d", path, got.Size, want.Size)
		}
		if want.ContentHash != got.ContentHash {
			return fmt.Errorf("%s: byte mismatch", path)
		}
		return nil
	}
	if len(want.Children) != len(got.Children) {
		return fmt.Errorf("%s: child count = %d, want %d", path, len(got.Children), len(want.Children))
	}
	for i := range want.Children {
		childPath := path + "/" + want.Children[i].Name
		if err := compare(childPath, want.Children[i], got.Children[i]); err != nil {
			return err
		}
	}
	return nil
}

func hashStream(t *testing.T, r io.Reader) [32]byte {
	t.Helper()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		t.Fatal(err)
	}
	return [32]byte(h.Sum(nil))
}
