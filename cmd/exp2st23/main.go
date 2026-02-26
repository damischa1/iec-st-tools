// exp2st23 — CoDeSys 2.3 EXP plain-text export → IEC 61131-3 .st file importer
//
// Reads a CoDeSys 2.3 .EXP file and writes one .st file per POU/GVL/TYPE.
// Non-ST implementations (FBD, Ladder, SFC, IL) are replaced with a minimal
// ST stub so the interface is preserved and usable as a code template.
//
// GVL objects are wrapped in a CONFIGURATION block as required by trust-LSP
// (IEC 61131-3 Ed.3).
//
// Usage:
//
//	exp2st23 -in <file.EXP> [-out <dir>]
//
// Flags:
//
//	-in     input .EXP file (required)
//	-out    output root directory for .st files (default "src")
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ── Metadata regex patterns ──────────────────────────────────────────────────

var (
	reNestedComments = regexp.MustCompile(`\(\*\s*@NESTEDCOMMENTS\s*:=\s*'[^']*'\s*\*\)`)
	reGlobalVarList  = regexp.MustCompile(`\(\*\s*@GLOBAL_VARIABLE_LIST\s*:=\s*'([^']*)'\s*\*\)`)
	rePath           = regexp.MustCompile(`\(\*\s*@PATH\s*:=\s*'([^']*)'\s*\*\)`)
	reObjectFlags    = regexp.MustCompile(`\(\*\s*@OBJECTFLAGS\s*:=\s*'[^']*'\s*\*\)`)
	reSymFileFlags   = regexp.MustCompile(`\(\*\s*@SYMFILEFLAGS\s*:=\s*'[^']*'\s*\*\)`)
	reEndDecl        = regexp.MustCompile(`\(\*\s*@END_DECLARATION\s*:=\s*'[^']*'\s*\*\)`)
	reObjectEnd      = regexp.MustCompile(`\(\*\s*@OBJECT_END\s*:=\s*'([^']*)'\s*\*\)`)
	reConnections    = regexp.MustCompile(`\(\*\s*@CONNECTIONS\s*:=`)
	reMetaLine       = regexp.MustCompile(`^\s*\(\*\s*@\w+\s*:=`)
)

// ── Object kind ──────────────────────────────────────────────────────────────

type objKind int

const (
	kindUnknown objKind = iota
	kindFunction
	kindFunctionBlock
	kindProgram
	kindType
	kindGVL
	kindVarConfig
)

func kindLabel(k objKind) string {
	switch k {
	case kindFunction:
		return "FUNCTION"
	case kindFunctionBlock:
		return "FB"
	case kindProgram:
		return "PROGRAM"
	case kindType:
		return "TYPE"
	case kindGVL:
		return "GVL"
	case kindVarConfig:
		return "VAR_CONFIG"
	default:
		return "UNKNOWN"
	}
}

// ── Parsed object ────────────────────────────────────────────────────────────

type expObject struct {
	name string
	kind objKind
	path string // raw CoDeSys path, e.g. "\/Implicit Globals"
	decl string // declaration part (before @END_DECLARATION or the whole block for TYPE)
	body string // implementation part (after @END_DECLARATION); empty for TYPE/GVL
}

// ── Splitting .EXP into objects ──────────────────────────────────────────────

// splitObjects splits the raw EXP text into individual object blocks.
// Each block starts with (* @NESTEDCOMMENTS := 'Yes' *).
func splitObjects(text string) []string {
	locs := reNestedComments.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}

	var blocks []string
	for i, loc := range locs {
		start := loc[0]
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(text)
		}
		blocks = append(blocks, text[start:end])
	}
	return blocks
}

// parseBlock extracts an expObject from a single raw block of EXP text.
func parseBlock(raw string) *expObject {
	obj := &expObject{}

	// Check for GVL marker
	if m := reGlobalVarList.FindStringSubmatch(raw); m != nil {
		obj.name = m[1]
		obj.kind = kindGVL
	}

	// Extract path
	if m := rePath.FindStringSubmatch(raw); m != nil {
		obj.path = m[1]
	}

	// Strip all metadata comments from top of block to get the real content.
	lines := strings.Split(raw, "\n")

	// Find first non-meta, non-blank line index
	contentStart := 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || reMetaLine.MatchString(line) {
			continue
		}
		contentStart = i
		break
	}

	// Find content end: strip OBJECT_END and CONNECTIONS blocks at the end.
	contentEnd := len(lines)
	for i := contentEnd - 1; i >= contentStart; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || t == "*)" || reObjectEnd.MatchString(t) || reConnections.MatchString(t) ||
			strings.HasPrefix(t, "FILENAME :") || strings.HasPrefix(t, "FILETIME :") ||
			strings.HasPrefix(t, "EXPORT :") || strings.HasPrefix(t, "NUMOFCONNECTIONS :") {
			contentEnd = i
		} else {
			break
		}
	}

	content := strings.Join(lines[contentStart:contentEnd], "\n")

	// Split at @END_DECLARATION if present
	if loc := reEndDecl.FindStringIndex(content); loc != nil {
		obj.decl = strings.TrimRight(content[:loc[0]], "\r\n ")
		afterDecl := content[loc[1]:]
		obj.body = strings.TrimSpace(afterDecl)
	} else {
		obj.decl = strings.TrimRight(content, "\r\n ")
		obj.body = ""
	}

	// Detect kind from the first code line if not already set as GVL
	if obj.kind == kindUnknown {
		obj.kind = detectKind(obj.decl)
	}

	// Extract name if not already set (GVL name comes from metadata)
	if obj.name == "" {
		obj.name = extractName(obj.decl, obj.kind)
	}

	// VAR_CONFIG with GVL marker → special kind
	if obj.kind == kindGVL {
		firstCode := firstCodeLine(obj.decl)
		if strings.HasPrefix(strings.ToUpper(firstCode), "VAR_CONFIG") {
			obj.kind = kindVarConfig
		}
	}

	return obj
}

// detectKind identifies the object kind from the declaration text.
func detectKind(decl string) objKind {
	line := firstCodeLine(decl)
	u := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(u, "FUNCTION_BLOCK "):
		return kindFunctionBlock
	case strings.HasPrefix(u, "FUNCTION "):
		return kindFunction
	case strings.HasPrefix(u, "PROGRAM "):
		return kindProgram
	case strings.HasPrefix(u, "TYPE ") || u == "TYPE":
		return kindType
	case strings.HasPrefix(u, "VAR_GLOBAL") || strings.HasPrefix(u, "VAR_CONFIG"):
		return kindGVL
	}
	return kindUnknown
}

// firstCodeLine returns the first non-blank line from the text.
func firstCodeLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			return t
		}
	}
	return ""
}

// extractName extracts the POU/TYPE name from the declaration.
func extractName(decl string, kind objKind) string {
	line := firstCodeLine(decl)

	switch kind {
	case kindFunction:
		orig := strings.TrimSpace(line[len("FUNCTION "):])
		parts := strings.Fields(orig)
		if len(parts) > 0 {
			return parts[0]
		}

	case kindFunctionBlock:
		orig := strings.TrimSpace(line[len("FUNCTION_BLOCK "):])
		parts := strings.Fields(orig)
		if len(parts) > 0 {
			return parts[0]
		}

	case kindProgram:
		orig := strings.TrimSpace(line[len("PROGRAM "):])
		parts := strings.Fields(orig)
		if len(parts) > 0 {
			return parts[0]
		}

	case kindType:
		for _, l := range strings.Split(decl, "\n") {
			t := strings.TrimSpace(l)
			up := strings.ToUpper(t)
			if up == "" || up == "TYPE" {
				continue
			}
			if strings.HasPrefix(up, "TYPE ") {
				t = strings.TrimSpace(t[5:])
			}
			name := t
			if i := strings.IndexAny(name, ":("); i > 0 {
				name = strings.TrimSpace(name[:i])
			}
			if name != "" {
				return name
			}
		}
	}
	return ""
}

// ── ST detection ─────────────────────────────────────────────────────────────

// isSTBody checks whether the implementation body looks like Structured Text.
// FBD bodies start with _FBD_BODY, IL with _IL_BODY, LD with _LD_BODY.
// SFC bodies contain INITIAL_STEP, STEP, TRANSITION, etc.
func isSTBody(body string) bool {
	t := strings.TrimSpace(body)
	if t == "" {
		return true // empty body → treat as ST
	}
	if strings.HasPrefix(t, "_FBD_BODY") || strings.HasPrefix(t, "_IL_BODY") ||
		strings.HasPrefix(t, "_LD_BODY") || strings.HasPrefix(t, "_CFC_BODY") {
		return false
	}
	fl := firstCodeLine(t)
	if strings.HasPrefix(strings.ToUpper(fl), "INITIAL_STEP ") {
		return false
	}
	return true
}

// ── Stub generation ──────────────────────────────────────────────────────────

func pouEndKeyword(decl string) string {
	line := firstCodeLine(decl)
	u := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(u, "PROGRAM "):
		return "END_PROGRAM"
	case strings.HasPrefix(u, "FUNCTION_BLOCK "):
		return "END_FUNCTION_BLOCK"
	case strings.HasPrefix(u, "FUNCTION "):
		return "END_FUNCTION"
	}
	return "END_PROGRAM"
}

// stubBody generates a minimal ST body from the declaration.
// It sets each VAR_OUTPUT / VAR_IN_OUT variable to its declared default or
// the zero value for the type.
func stubBody(decl string) string {
	var lines []string
	lines = append(lines, "// ** GENERATED STUB — original implementation is non-ST (FBD/Ladder/SFC/IL) **")
	lines = append(lines, "// Adapt this body for your application logic.")
	lines = append(lines, "")

	inOut := false
	for _, line := range strings.Split(decl, "\n") {
		u := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(u, "VAR_OUTPUT") || strings.HasPrefix(u, "VAR_IN_OUT"):
			inOut = true
		case u == "END_VAR":
			inOut = false
		case inOut:
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "(*") {
				continue
			}
			varName := t
			if i := strings.Index(varName, ":"); i > 0 {
				varName = strings.TrimSpace(varName[:i])
			}
			if varName == "" || strings.HasPrefix(varName, "{") {
				continue
			}
			rest := t[strings.Index(t, ":")+1:]
			rest = strings.TrimLeft(rest, ": \t")
			rest = strings.TrimSuffix(strings.TrimSpace(rest), ";")
			defaultVal := "0"
			if i := strings.Index(rest, ":="); i >= 0 {
				defaultVal = strings.TrimSpace(rest[i+2:])
				defaultVal = strings.TrimSuffix(defaultVal, ";")
			} else {
				fields := strings.Fields(rest)
				if len(fields) > 0 {
					typeName := strings.ToUpper(fields[0])
					switch {
					case typeName == "BOOL" || strings.HasSuffix(typeName, "BOOL"):
						defaultVal = "FALSE"
					case strings.HasPrefix(typeName, "STRING"):
						defaultVal = "''"
					default:
						defaultVal = "0"
					}
				}
			}
			lines = append(lines, fmt.Sprintf("%s := %s;", varName, defaultVal))
		}
	}
	return strings.Join(lines, "\n")
}

// ── .st file generation ─────────────────────────────────────────────────────

func pathToDir(rawPath string) string {
	p := strings.ReplaceAll(rawPath, "\\/", "/")
	p = strings.ReplaceAll(rawPath, "\\", "")
	p = strings.ReplaceAll(p, "/", string(filepath.Separator))
	p = strings.TrimLeft(p, string(filepath.Separator))
	return p
}

func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

func generateST(obj *expObject) (content, relPath string) {
	dir := pathToDir(obj.path)
	relPath = filepath.Join(dir, obj.name+".st")

	switch obj.kind {
	case kindType:
		content = strings.TrimRight(obj.decl, "\r\n ") + "\n"

	case kindGVL:
		d := strings.TrimRight(obj.decl, "\r\n ")
		content = fmt.Sprintf("// trust-LSP wrapper — compiler extracts VAR_GLOBAL automatically\n"+
			"CONFIGURATION %s\n%s\nEND_CONFIGURATION\n",
			obj.name, indentBlock(d, "    "))

	case kindVarConfig:
		d := strings.TrimRight(obj.decl, "\r\n ")
		content = fmt.Sprintf("// trust-LSP wrapper — VAR_CONFIG from CoDeSys 2.3\n"+
			"CONFIGURATION %s\n%s\nEND_CONFIGURATION\n",
			obj.name, indentBlock(d, "    "))

	case kindFunction, kindFunctionBlock, kindProgram:
		endKW := pouEndKeyword(obj.decl)
		body := obj.body
		stubFlag := ""
		if !isSTBody(body) {
			body = stubBody(obj.decl)
			stubFlag = "\n// NOTE: Original implementation was non-ST (FBD/Ladder/SFC/IL). Stub generated."
		}
		decl := strings.TrimRight(obj.decl, "\r\n ")
		body = strings.TrimRight(body, "\r\n ")

		// In CoDeSys 2.3, the END_xxx keyword is part of the body (after @END_DECLARATION).
		// Check if body already ends with the END keyword to avoid duplication.
		bodyHasEnd := strings.HasSuffix(strings.ToUpper(strings.TrimSpace(body)), strings.ToUpper(endKW))

		if body == "" {
			content = fmt.Sprintf("%s%s\n%s\n", decl, stubFlag, endKW)
		} else if bodyHasEnd {
			content = fmt.Sprintf("%s%s\n%s\n", decl, stubFlag, body)
		} else {
			content = fmt.Sprintf("%s%s\n%s\n%s\n", decl, stubFlag, body, endKW)
		}

	default:
		content = fmt.Sprintf("// Unknown object: %s\n%s\n", obj.name, obj.decl)
	}
	return
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	inFile := flag.String("in", "", "input .EXP file (required)")
	outDir := flag.String("out", "src", "output root directory")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "exp2st23 - Import CoDeSys 2.3 .EXP files to IEC 61131-3 .st format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *inFile == "" {
		fmt.Fprintln(os.Stderr, "usage: exp2st23 -in <file.EXP> [-out <dir>]")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*inFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read input:", err)
		os.Exit(1)
	}

	// Normalize to LF for parsing (EXP files are CRLF)
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	blocks := splitObjects(text)
	if len(blocks) == 0 {
		fmt.Fprintln(os.Stderr, "no objects found in input file")
		os.Exit(1)
	}

	written, skipped, stubbed := 0, 0, 0

	for _, block := range blocks {
		obj := parseBlock(block)
		if obj == nil || obj.name == "" {
			fmt.Fprintf(os.Stderr, "WARNING: skipping unrecognized block\n")
			skipped++
			continue
		}

		wasStubbed := false
		switch obj.kind {
		case kindFunction, kindFunctionBlock, kindProgram:
			if !isSTBody(obj.body) {
				wasStubbed = true
			}
		}

		content, relPath := generateST(obj)
		outPath := filepath.Join(*outDir, relPath)

		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create dir for %s: %v\n", outPath, err)
			continue
		}
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", outPath, err)
			continue
		}

		tag := ""
		if wasStubbed {
			tag = " [STUB]"
			stubbed++
		}
		fmt.Printf("  %-10s  %s%s\n", kindLabel(obj.kind), outPath, tag)
		written++
	}

	fmt.Printf("\nDone: %d written (%d stubs), %d skipped\n", written, stubbed, skipped)
}
