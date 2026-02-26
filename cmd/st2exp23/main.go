// st2exp23 — Converts IEC 61131-3 Structured Text (.st) source files
// into a CoDeSys 2.3 compatible export file (.EXP).
//
// NOTE: CoDeSys 2.3 requires Windows-style CRLF (\r\n) line endings.
// This tool always outputs CRLF. CoDeSys runs on Windows only.
//
// Usage:
//
//	st2exp23 [flags]
//	  -src    string   Source directory containing .st files (default "src")
//	  -file   string   Compile a single .st file instead of a directory
//	  -out    string   Output directory for the .EXP file   (default "build")
//	  -name   string   Base name of the output file          (default "export")
//	  -path   string   CoDeSys PATH value for all objects    (default "")
//	  -help            Show this help message
//
// Examples:
//
//	st2exp23                                       # compile all files in src/
//	st2exp23 -file src/SafeInvert.st               # compile single file
//	st2exp23 -src src -out build -name SafeLib      # full options
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pouKind represents the top-level type of a Structured Text object.
type pouKind int

const (
	kindUnknown pouKind = iota
	kindFunction
	kindFunctionBlock
	kindProgram
	kindType
	kindVarGlobal
	kindConfiguration
)

func detectKind(firstLine string) pouKind {
	t := strings.TrimSpace(firstLine)
	switch {
	case strings.HasPrefix(t, "FUNCTION_BLOCK "):
		return kindFunctionBlock
	case strings.HasPrefix(t, "FUNCTION "):
		return kindFunction
	case strings.HasPrefix(t, "PROGRAM "):
		return kindProgram
	case strings.HasPrefix(t, "TYPE ") || t == "TYPE":
		return kindType
	case strings.HasPrefix(t, "VAR_GLOBAL"):
		return kindVarGlobal
	case strings.HasPrefix(t, "CONFIGURATION "):
		return kindConfiguration
	}
	return kindUnknown
}

// convertFile reads a single .st file and returns the corresponding EXP block(s).
// A CONFIGURATION file may contain multiple VAR_GLOBAL sections, each becoming
// a separate CoDeSys global variable list named after the CONFIGURATION.
func convertFile(path, expPath string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}

	// Split into lines; normalize line endings to LF first, then handle as plain strings.
	// Output always uses CRLF regardless of what the source file contains.
	allLines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n"), "\n")
	start := 0
	for start < len(allLines) {
		trimmed := strings.TrimSpace(allLines[start])
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			start++
		} else {
			break
		}
	}
	if start >= len(allLines) {
		return "", fmt.Errorf("file %s contains only comments/blank lines – skipped", path)
	}
	lines := allLines[start:]

	// Strip trailing empty lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	kind := detectKind(lines[0])
	if kind == kindUnknown {
		return "", fmt.Errorf("file %s: unrecognised POU type (first line: %q) – skipped", path, lines[0])
	}

	listName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	// CONFIGURATION: extract each VAR_GLOBAL block inside it.
	if kind == kindConfiguration {
		parts := strings.Fields(strings.TrimSpace(lines[0]))
		if len(parts) >= 2 {
			listName = parts[1]
		}
		return convertConfiguration(lines, listName, expPath)
	}

	return convertPou(lines, kind, listName, expPath)
}

// ensureEmptyVarBlock ensures a FUNCTION / FUNCTION_BLOCK / PROGRAM has at
// least one plain VAR...END_VAR block (CoDeSys requires it for import).
func ensureEmptyVarBlock(lines []string) []string {
	hasPlainVar := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "VAR" {
			hasPlainVar = true
			break
		}
	}
	if hasPlainVar {
		return lines
	}
	lastEndVar := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "END_VAR" {
			lastEndVar = i
		}
	}
	ins := lastEndVar + 1
	if lastEndVar == -1 {
		ins = len(lines)
	}
	result := make([]string, 0, len(lines)+2)
	result = append(result, lines[:ins]...)
	result = append(result, "VAR", "END_VAR")
	result = append(result, lines[ins:]...)
	return result
}

// convertConfiguration extracts VAR_GLOBAL blocks from a CONFIGURATION wrapper
// and renders each as a separate CoDeSys global variable list.
func convertConfiguration(lines []string, listName, expPath string) (string, error) {
	var sb strings.Builder
	inVarGlobal := false
	var block []string
	blockCount := 0

	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !inVarGlobal && strings.HasPrefix(t, "VAR_GLOBAL") {
			inVarGlobal = true
			block = []string{l}
			continue
		}
		if inVarGlobal {
			block = append(block, l)
			if t == "END_VAR" {
				inVarGlobal = false
				blockCount++
				prefix := ""
				if len(block) > 0 {
					first := block[0]
					for _, ch := range first {
						if ch == ' ' || ch == '\t' {
							prefix += string(ch)
						} else {
							break
						}
					}
				}
				for i, bl := range block {
					block[i] = strings.TrimPrefix(bl, prefix)
				}
				chunk, err := convertPou(block, kindVarGlobal, listName, expPath)
				if err != nil {
					return "", err
				}
				sb.WriteString(chunk)
				block = nil
			}
		}
	}
	if blockCount == 0 {
		return "", fmt.Errorf("CONFIGURATION %q contains no VAR_GLOBAL blocks", listName)
	}
	return sb.String(), nil
}

// convertPou renders a single POU (FUNCTION, FUNCTION_BLOCK, PROGRAM, TYPE, VAR_GLOBAL)
// as a CoDeSys 2.3 EXP block.
func convertPou(lines []string, kind pouKind, listName, expPath string) (string, error) {
	var sb strings.Builder

	// --- Metadata header ---
	sb.WriteString("(* @NESTEDCOMMENTS := 'Yes' *)\r\n")
	if kind == kindVarGlobal {
		fmt.Fprintf(&sb, "(* @GLOBAL_VARIABLE_LIST := '%s' *)\r\n", listName)
	}
	fmt.Fprintf(&sb, "(* @PATH := '%s' *)\r\n", expPath)
	sb.WriteString("(* @OBJECTFLAGS := '0, 8' *)\r\n")
	if kind == kindFunction || kind == kindFunctionBlock || kind == kindProgram || kind == kindVarGlobal {
		sb.WriteString("(* @SYMFILEFLAGS := '2048' *)\r\n")
	}

	switch kind {
	case kindFunction, kindFunctionBlock, kindProgram:
		lastEndVar := -1
		for i, l := range lines {
			if strings.TrimSpace(l) == "END_VAR" {
				lastEndVar = i
			}
		}
		if lastEndVar == -1 {
			sb.WriteString("(* @END_DECLARATION := '0' *)\r\n")
			for _, l := range lines {
				sb.WriteString(l + "\r\n")
			}
		} else {
			for _, l := range lines[:lastEndVar+1] {
				sb.WriteString(l + "\r\n")
			}
			sb.WriteString("(* @END_DECLARATION := '0' *)\r\n")
			for _, l := range lines[lastEndVar+1:] {
				sb.WriteString(l + "\r\n")
			}
		}

	case kindType:
		for _, l := range lines {
			sb.WriteString(l + "\r\n")
		}
		sb.WriteString("(* @END_DECLARATION := '0' *)\r\n")

	case kindVarGlobal:
		for _, l := range lines {
			sb.WriteString(l + "\r\n")
		}
		sb.WriteString("\r\n")
		fmt.Fprintf(&sb, "(* @OBJECT_END := '%s' *)\r\n", listName)
		fmt.Fprintf(&sb, "(* @CONNECTIONS := %s\r\n", listName)
		sb.WriteString("FILENAME : ''\r\n")
		sb.WriteString("FILETIME : 0\r\n")
		sb.WriteString("EXPORT : 0\r\n")
		sb.WriteString("NUMOFCONNECTIONS : 0\r\n")
		sb.WriteString("*)\r\n")
	}

	sb.WriteString("\r\n")
	return sb.String(), nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("st2exp23: ")

	srcDir := flag.String("src", "src", "Source directory containing .st files")
	srcFile := flag.String("file", "", "Compile a single .st file instead of a whole directory")
	outDir := flag.String("out", "build", "Output directory for the .EXP file")
	name := flag.String("name", "export", "Base name of the output .EXP file")
	expPath := flag.String("path", "", `CoDeSys PATH value for all objects, e.g. "\/MyLib"`)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "st2exp23 - Convert IEC 61131-3 .st files to CoDeSys 2.3 .EXP format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintln(os.Stderr, `  st2exp23                                      # compile all files in src/`)
		fmt.Fprintln(os.Stderr, `  st2exp23 -file src/SafeInvert.st               # compile single file`)
		fmt.Fprintln(os.Stderr, `  st2exp23 -src src -out build -name SafeLib      # full options`)
	}
	flag.Parse()

	var stFiles []string
	if *srcFile != "" {
		if _, err := os.Stat(*srcFile); os.IsNotExist(err) {
			log.Fatalf("file %q does not exist", *srcFile)
		}
		stFiles = []string{*srcFile}
		*srcDir = filepath.Dir(*srcFile)
	} else {
		if _, err := os.Stat(*srcDir); os.IsNotExist(err) {
			log.Fatalf("source directory %q does not exist", *srcDir)
		}
		err := filepath.WalkDir(*srcDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".st") {
				stFiles = append(stFiles, p)
			}
			return nil
		})
		if err != nil {
			log.Fatalf("error scanning %s: %v", *srcDir, err)
		}
	}
	if len(stFiles) == 0 {
		log.Fatalf("no .st files found")
	}
	sort.Strings(stFiles)

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("cannot create output directory %q: %v", *outDir, err)
	}

	outPath := filepath.Join(*outDir, *name+".EXP")

	var all strings.Builder
	all.WriteString("\r\n")
	processed := 0

	absSrc, err := filepath.Abs(*srcDir)
	if err != nil {
		log.Fatalf("cannot resolve src path: %v", err)
	}

	for _, f := range stFiles {
		absF, _ := filepath.Abs(f)
		rel, _ := filepath.Rel(absSrc, filepath.Dir(absF))
		var filePath string
		if rel == "." {
			filePath = *expPath
		} else {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			filePath = *expPath + "\\/" + strings.Join(parts, "\\/")
		}

		fmt.Printf("Processing: %-30s  PATH='%s'\n", filepath.Base(f), filePath)
		block, err := convertFile(f, filePath)
		if err != nil {
			log.Printf("WARNING: %v", err)
			continue
		}
		all.WriteString(block)
		processed++
	}

	if processed == 0 {
		log.Fatal("no files were successfully converted")
	}

	content := strings.TrimRight(all.String(), "\r\n") + "\r\n"

	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		log.Fatalf("cannot write output file: %v", err)
	}

	fmt.Printf("\nDone! Written: %s  (%d file(s) converted)\n", outPath, processed)
}
