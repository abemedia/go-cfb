// generate.go materialises testdata fixtures via Windows' IStorage API.
// Run on Windows:
//
//	go run ./testdata/generate.go
//
// For each generator=istorage entry in fixtures.json it builds testdata/{name}.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/abemedia/go-cfb/internal/istorage"
)

type EntrySpec struct {
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Size      int         `json:"size,omitempty"`
	Content   string      `json:"content,omitempty"`
	Children  []EntrySpec `json:"children,omitempty"`
	CLSID     string      `json:"clsid,omitempty"`
	StateBits uint32      `json:"stateBits,omitempty"`
	Modified  time.Time   `json:"modified,omitzero"`
	Created   time.Time   `json:"created,omitzero"`
}

type Fixture struct {
	Name      string    `json:"name"`
	Generator string    `json:"generator"`
	Version   string    `json:"version,omitempty"`
	Tree      EntrySpec `json:"tree,omitzero"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	if runtime.GOOS != "windows" {
		return errors.New("this generator must be run on Windows (uses ole32.dll)")
	}

	dir, err := sourceDir()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		return err
	}

	var fixtures []Fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		return fmt.Errorf("parse fixtures.json: %w", err)
	}

	// Clean up old .cfb files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".cfb") {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}

	for _, fix := range fixtures {
		fmt.Printf("Generating fixture %q...\n", fix.Name)
		if fix.Generator == "istorage" {
			if err := buildCFB(filepath.Join(dir, fix.Name), fix.Version, fix.Name, fix.Tree); err != nil {
				return fmt.Errorf("%s: %w", fix.Name, err)
			}
		}
	}
	fmt.Println("Done.")
	return nil
}

func sourceDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("could not determine source file location")
	}
	return filepath.Dir(file), nil
}

func buildCFB(cfbPath, version, fixtureName string, root EntrySpec) error {
	var v istorage.Version
	switch {
	case strings.EqualFold(version, "v3"):
		v = istorage.V3
	case strings.EqualFold(version, "v4"):
		v = istorage.V4
	default:
		return fmt.Errorf("unknown version %q", version)
	}
	stg, err := istorage.Create(cfbPath, v)
	if err != nil {
		return err
	}
	defer stg.Close()
	if err := applyMetadata(stg, root); err != nil {
		return err
	}
	if err := writeTree(stg, fixtureName, "", root.Children); err != nil {
		return err
	}
	if err := stg.Commit(); err != nil {
		return err
	}
	return stg.SetElementTimes("", root.Created, root.Modified, root.Modified)
}

func writeTree(parent *istorage.Storage, fixtureName, prefix string, entries []EntrySpec) error {
	for _, e := range entries {
		rel := e.Name
		if prefix != "" {
			rel = prefix + "/" + e.Name
		}
		switch e.Type {
		case "stream":
			stm, err := parent.CreateStream(e.Name)
			if err != nil {
				return err
			}
			content := []byte(e.Content)
			if len(content) == 0 {
				content = generateContent(fixtureName, rel, e.Size)
			}
			if _, err := stm.Write(content); err != nil {
				stm.Close()
				return err
			}
			stm.Close()
		case "storage":
			sub, err := parent.CreateStorage(e.Name)
			if err != nil {
				return err
			}
			err = applyMetadata(sub, e)
			if err == nil {
				err = writeTree(sub, fixtureName, rel, e.Children)
			}
			sub.Close()
			if err != nil {
				return err
			}
			if err := parent.SetElementTimes(e.Name, e.Created, e.Modified, e.Modified); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unknown entry type %q", rel, e.Type)
		}
	}
	return nil
}

// applyMetadata writes CLSID and StateBits onto an open storage handle.
func applyMetadata(s *istorage.Storage, e EntrySpec) error {
	if e.CLSID != "" {
		clsid, err := parseCLSID(e.CLSID)
		if err != nil {
			return err
		}
		if err := s.SetClass(clsid); err != nil {
			return err
		}
	}
	if err := s.SetStateBits(e.StateBits); err != nil {
		return err
	}
	return nil
}

// parseCLSID parses a GUID string into its COM in-memory layout.
func parseCLSID(in string) ([16]byte, error) {
	s := strings.ReplaceAll(strings.Trim(in, "{}"), "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return [16]byte{}, fmt.Errorf("CLSID %q: invalid", in)
	}
	var clsid [16]byte
	copy(clsid[:], b)
	slices.Reverse(clsid[0:4])
	slices.Reverse(clsid[4:6])
	slices.Reverse(clsid[6:8])
	return clsid, nil
}

// generateContent returns deterministic pseudo-random content for a stream.
// Output is fully determined by fixtureName+entryPath so re-running the
// generator is reproducible.
func generateContent(fixtureName, entryPath string, size int) []byte {
	if size <= 0 {
		return nil
	}
	h := fnv.New64a()
	h.Write([]byte(fixtureName + "/" + entryPath))
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(rng.IntN(256))
	}
	return buf
}
