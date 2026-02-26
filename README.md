# iec-st-tools

Command-line tools for converting between IEC 61131-3 Structured Text (`.st`) source files and CoDeSys export formats.

Supports **CoDeSys 2.3** (plain-text `.EXP`), **CoDeSys 3.5** (XML `.export`), and **PLCOpen XML** (TC6 standard `.xml`).

## Tools

| Tool | Description |
|------|-------------|
| `st2exp23` | `.st` → CoDeSys 2.3 `.EXP` exporter |
| `exp2st23` | CoDeSys 2.3 `.EXP` → `.st` importer |
| `st2exp35` | `.st` → CoDeSys 3.5 `.export` XML exporter |
| `exp2st35` | CoDeSys 3.5 `.export` XML → `.st` importer |
| `st2plcopen` | `.st` → PLCOpen XML (TC6) `.xml` exporter |
| `plcopen2st` | PLCOpen XML (TC6) `.xml` → `.st` importer |

## Build

Requires **Go 1.21+**.

```sh
# Build all tools
go build ./cmd/st2exp23
go build ./cmd/exp2st23
go build ./cmd/st2exp35
go build ./cmd/exp2st35
go build ./cmd/st2plcopen
go build ./cmd/plcopen2st
```

Or install directly:

```sh
go install github.com/damischa1/iec-st-tools/cmd/st2exp23@latest
go install github.com/damischa1/iec-st-tools/cmd/exp2st23@latest
go install github.com/damischa1/iec-st-tools/cmd/st2exp35@latest
go install github.com/damischa1/iec-st-tools/cmd/exp2st35@latest
go install github.com/damischa1/iec-st-tools/cmd/st2plcopen@latest
go install github.com/damischa1/iec-st-tools/cmd/plcopen2st@latest
```

## Usage

### st2exp23 — Export .st to CoDeSys 2.3 EXP

```sh
st2exp23                                        # compile all .st files in src/
st2exp23 -file src/SafeInvert.st                # compile a single file
st2exp23 -src src -out build -name SafeLib       # custom source, output, name
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-src` | `src` | Source directory containing `.st` files |
| `-file` | | Compile a single `.st` file instead of a directory |
| `-out` | `build` | Output directory for the `.EXP` file |
| `-name` | `export` | Base name of the output file |
| `-path` | `""` | CoDeSys PATH value for all objects |

### exp2st23 — Import CoDeSys 2.3 EXP to .st

```sh
exp2st23 -in project.EXP                        # import to src/
exp2st23 -in project.EXP -out my_project        # custom output directory
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-in` | *(required)* | Input `.EXP` file |
| `-out` | `src` | Output root directory for `.st` files |

### st2exp35 — Export .st to CoDeSys 3.5 XML

```sh
st2exp35                                        # compile all .st files in src/
st2exp35 -src src -out build -name MyLib        # custom options
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-src` | `src` | Source directory containing `.st` files |
| `-out` | `build` | Output directory |
| `-name` | `export` | Output filename (without extension) |
| `-base` | `Device,PLC Logic,Application` | CoDeSys tree base path (comma-separated) |

### exp2st35 — Import CoDeSys 3.5 XML to .st

```sh
exp2st35 -in project.export                     # import to src/
exp2st35 -in project.export -out my_project     # custom output directory
exp2st35 -in project.export -strip 3            # strip 3 path elements
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-in` | *(required)* | Input `.export` XML file |
| `-out` | `src` | Output root directory for `.st` files |
| `-base` | `Device,PLC Logic,Application` | CoDeSys tree prefix to strip from path |
| `-strip` | `0` | Number of path elements to strip (overrides `-base` if > 0) |

### st2plcopen — Export .st to PLCOpen XML (TC6)

```sh
st2plcopen                                      # compile all .st files in src/
st2plcopen -src src -out build -name MyProject  # custom options
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-src` | `src` | Source directory containing `.st` files |
| `-out` | `build` | Output directory |
| `-name` | `plcopen_export` | Project name and output filename (without extension) |
| `-company` | `iec-st-tools` | Company name in file header |

Generates standard PLCOpen XML TC6 v2.0 with CoDeSys-compatible `InterfaceAsPlainText` extensions for reliable import into CoDeSys 3.5 and TwinCAT 3.

### plcopen2st — Import PLCOpen XML (TC6) to .st

```sh
plcopen2st -in project.xml                      # import to src/
plcopen2st -in project.xml -out my_project      # custom output directory
plcopen2st -in project.xml -flat                # all files in one directory
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-in` | *(required)* | Input PLCOpen XML file |
| `-out` | `src` | Output root directory for `.st` files |
| `-flat` | `false` | Write all files flat, no subdirectories |

Handles PLCOpen XML files from CoDeSys 3.5, TwinCAT 3, and other IEC 61131-3 tools. Uses `InterfaceAsPlainText` when available for highest fidelity, falls back to reconstructing declarations from structured XML.

## Supported Object Types

| IEC 61131-3 construct | CoDeSys type | Detected from |
|----------------------|--------------|---------------|
| `FUNCTION` | POU | first code line |
| `FUNCTION_BLOCK` | POU | first code line |
| `PROGRAM` | POU | first code line |
| `TYPE ... END_TYPE` | DUT | first code line |
| `VAR_GLOBAL ... END_VAR` | GVL | first code line |
| `CONFIGURATION` wrapping `VAR_GLOBAL` | GVL | stripped to `VAR_GLOBAL` on export |

## Source File Conventions

### Directory structure maps to CoDeSys project tree

```
src/
├── SafeInvert.st                   → root of project tree
├── UserGlobals/
│   └── Globals.st                  → UserGlobals folder
└── UserTypes/
    └── TestTypes.st                → UserTypes folder
```

### CONFIGURATION wrapper for global variables

IEC 61131-3 Ed.3 requires `VAR_GLOBAL` blocks to appear inside a `CONFIGURATION` wrapper. The tools handle this automatically:

- **Exporters** (`st2exp23`, `st2exp35`, `st2plcopen`): strip the `CONFIGURATION`/`END_CONFIGURATION` wrapper and export only the `VAR_GLOBAL` block.
- **Importers** (`exp2st23`, `exp2st35`, `plcopen2st`): wrap imported `VAR_GLOBAL` blocks in `CONFIGURATION 'name' ... END_CONFIGURATION`.

Example `.st` source file for globals:

```iec
CONFIGURATION Globals
  VAR_GLOBAL
    G_Counter : INT := 0;
    G_Enable  : BOOL := TRUE;
  END_VAR
END_CONFIGURATION
```

### Non-ST implementation stubs

When importing a CoDeSys project that contains FBD, Ladder, SFC, or IL implementations, the importers generate a minimal ST stub body (e.g., `; (* TODO: originally FBD *)`) preserving the declaration/interface so the code compiles and can be used as a template.

## Format Details

### CoDeSys 2.3 EXP

Plain-text format with **mandatory CRLF** line endings. Each object starts with an `(* @NESTEDCOMMENTS := ... *)` metadata block followed by the ST source. Objects are separated by blank lines.

### CoDeSys 3.5 XML Export

XML format (`<ExportFile>`) with GUID-based object identifiers. Each POU has separate `<Declaration>` and `<Implementation><ST>` sections. GVLs and DUTs have only a `<Declaration>`.

### PLCOpen XML (TC6)

Standard IEC 61131-3 exchange format (PLCOpen TC6 v2.0, namespace `http://www.plcopen.org/xml/tc6_0200`).

- **POUs** in `<types><pous><pou>` with `<interface>` (structured variable lists) and `<body><ST><xhtml>` (implementation)
- **DUTs** in `<types><dataTypes><dataType>` with `<baseType>` containing `<struct>` or `<enum>`
- **GVLs** in `<instances><configurations><configuration><resource><globalVars>`
- **CoDeSys extension**: `InterfaceAsPlainText` in `<addData>` sections preserves the exact ST declaration text for reliable round-tripping
- **Project structure**: folder layout stored in `<addData>` `ProjectStructure` element

Compatible with CoDeSys 3.5, TwinCAT 3, and other PLCOpen-compliant tools.

## Test Data

The `testdata/` directory contains sample files:

- `testdata/src/` — clean `.st` source files
- `testdata/codesys23/export.EXP` — generated CoDeSys 2.3 export
- `testdata/codesys35/export.export` — generated CoDeSys 3.5 export
- `testdata/plcopen/TestProject.xml` — generated PLCOpen XML export
