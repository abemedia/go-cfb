# go-cfb: Pure Go Microsoft Compound File Binary Reader & Writer

[![Go Reference](https://pkg.go.dev/badge/github.com/abemedia/go-cfb.svg)](https://pkg.go.dev/github.com/abemedia/go-cfb)
[![Go Report Card](https://goreportcard.com/badge/github.com/abemedia/go-cfb)](https://goreportcard.com/report/github.com/abemedia/go-cfb)
[![Codecov](https://codecov.io/gh/abemedia/go-cfb/branch/master/graph/badge.svg)](https://codecov.io/gh/abemedia/go-cfb)

A pure Go library for reading and writing Microsoft Compound File Binary (CFB) files - the COM Structured Storage (OLE2) container format used by `.msg`, `.doc`, `.xls`, `.msi`, and similar files.

See the [\[MS-CFB\] Compound File Binary File Format specification](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-cfb/) for details.

## Installation

```sh
go get github.com/abemedia/go-cfb
```

## Usage

### Reading a Compound File

A compound file is a tree of storages (directories) and streams (files). Open one and walk its entries:

```go
r, err := cfb.OpenReader("archive.cfb")
if err != nil {
  return err
}
defer r.Close()

var walk func(s *cfb.Storage)
walk = func(s *cfb.Storage) {
  for _, e := range s.Entries {
    switch e := e.(type) {
      // Every entry is one of exactly two types: *cfb.Storage or *cfb.Stream.
      case *cfb.Storage:
        fmt.Println(e.Name + "/")
        walk(e)
      case *cfb.Stream:
        fmt.Printf("%s (%d bytes)\n", e.Name, e.Size)
    }
  }
}
walk(r.Storage)
```

A specific stream or storage can be looked up by name (case-insensitive) with `OpenStream` and `OpenStorage`:

```go
s, err := r.OpenStream("\x05SummaryInformation")
if err != nil {
  return err
}
data, err := io.ReadAll(s.Open())
```

The `Reader` also implements `fs.FS`, so it works with `fs.WalkDir`, `fs.ReadFile`, and other standard library functions:

```go
fs.WalkDir(r, ".", func(path string, d fs.DirEntry, err error) error {
  fmt.Println(path)
  return err
})
```

`Stream` additionally implements `io.ReaderAt`, which is stateless and safe for concurrent use.

### Writing a Compound File

Choose a version: `NewWriterV3` (512-byte sectors) or `NewWriterV4` (4096-byte sectors); v4 is typical for large files such as modern MSIs.

```go
f, err := os.Create("archive.cfb")
if err != nil {
  return err
}
defer f.Close()

w := cfb.NewWriterV4(f)

s, err := w.CreateStream("hello.txt")
if err != nil {
  return err
}

if _, err = s.Write([]byte("hello world")); err != nil {
  return err
}

// Streams must be closed before the writer.
if err := s.Close(); err != nil {
  return err
}

// Make sure to check the error on Close.
if err := w.Close(); err != nil {
  return err
}
```

Nested storages are created with `CreateStorage`, which returns a writer with the same `CreateStream` / `CreateStorage` methods:

```go
sub, err := w.CreateStorage("Data")
if err != nil {
  return err
}
s, err := sub.CreateStream("part1")
```

If you want to pack any `fs.FS` in one go, use `AddFS`:

```go
if err := w.AddFS(os.DirFS("./my-app-files")); err != nil {
  return err
}
```

`CreateStream`, `CreateStorage`, and concurrent `Write`s on distinct streams are safe to call from multiple goroutines.

See the [package documentation](https://pkg.go.dev/github.com/abemedia/go-cfb) for further examples.
