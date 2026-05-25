# oleps: Pure Go OLE Property Set (MS-OLEPS) Reader & Writer

[![Go Reference](https://pkg.go.dev/badge/github.com/abemedia/go-cfb/oleps.svg)](https://pkg.go.dev/github.com/abemedia/go-cfb/oleps)

A pure Go library for reading and writing OLE Property Sets - the typed metadata format used inside compound files for streams like `\x05SummaryInformation` in `.msi`, `.doc`, `.xls`, `.msg`, and other COM Structured Storage documents.

See the [\[MS-OLEPS\] Object Linking and Embedding (OLE) Property Set Data Structures specification](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-oleps/) for details.

## Installation

```sh
go get github.com/abemedia/go-cfb/oleps
```

## Usage

### Reading a Property Set

```go
pss, err := oleps.Unmarshal(data)
if err != nil {
  return err
}
// A stream contains one or two property sets, each identified by an FMTID
// and holding a list of typed (PID, Value) properties.
for _, ps := range pss.PropertySets {
  fmt.Printf("FMTID=%x\n", ps.FMTID)
  for _, p := range ps.Properties {
    fmt.Printf("  PID=%d Value=%v\n", p.ID, p.Value)
  }
}
```

### Writing a Property Set

```go
// FMTID for SummaryInformation: {F29F85E0-4FF9-1068-AB91-08002B27B3D9}.
fmtid := [16]byte{
  0xE0, 0x85, 0x9F, 0xF2, 0xF9, 0x4F, 0x68, 0x10,
  0xAB, 0x91, 0x08, 0x00, 0x2B, 0x27, 0xB3, 0xD9,
}

pss := oleps.PropertySetStream{
  Version:          0,          // 0 (common) or 1 (supports more value types)
  SystemIdentifier: 0x0002000A, // OS kind + version that wrote the stream
  PropertySets: []oleps.PropertySet{{
    FMTID: fmtid,
    Properties: []oleps.Property{
      {ID: 1, Value: oleps.I2(1252)},              // Codepage
      {ID: 2, Value: oleps.LPSTR("Hello World")},  // Title
      {ID: 4, Value: oleps.LPSTR("Example Inc")},  // Author
      {ID: 12, Value: oleps.FileTime(time.Now())}, // CreateTime
    },
  }},
}

data, err := oleps.Marshal(pss)
if err != nil {
  return err
}
```

## Supported Value Types

Construct property values using the matching Go type. Additional MS-OLEPS types will be added in future releases.

| Property Type | Go type          |
| ------------- | ---------------- |
| `VT_I2`       | `oleps.I2`       |
| `VT_I4`       | `oleps.I4`       |
| `VT_UI4`      | `oleps.UI4`      |
| `VT_LPSTR`    | `oleps.LPSTR`    |
| `VT_FILETIME` | `oleps.FileTime` |
