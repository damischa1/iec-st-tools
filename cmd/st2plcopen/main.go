// st2plcopen — PLCOpen XML (TC6) exporter
// Converts IEC 61131-3 compatible .st files to PLCOpen XML format.
//
// The PLCOpen XML format is a standardised IEC 61131-3 program exchange format
// (TC6 XML v2.0) supported by CoDeSys 3.5, TwinCAT 3, Siemens TIA Portal, and
// many other IEC 61131-3 environments.
//
// Usage:
//
//	st2plcopen [-src <dir>] [-out <dir>] [-name <file>]
//
// Flags:
//
//	-src     source root directory (default "src")
//	-out     output directory (default "build")
//	-name    output filename without extension (default "plcopen_export")
//	-company company name in file header (default "iec-st-tools")
//
// Object types detected from first code line:
//
//	FUNCTION         → pou (pouType="function")
//	FUNCTION_BLOCK   → pou (pouType="functionBlock")
//	PROGRAM          → pou (pouType="program")
//	TYPE             → dataType
//	VAR_GLOBAL       → globalVars
//	CONFIGURATION    → stripped to VAR_GLOBAL blocks (trust-LSP compatibility)
package main

import (
	"bufio"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// crlfWriter wraps an io.Writer and converts \n to \r\n.
type crlfWriter struct {
	w *bufio.Writer
}

func newCRLFWriter(f *os.File) *crlfWriter {
	return &crlfWriter{w: bufio.NewWriter(f)}
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	written := 0
	for _, b := range p {
		if b == '\n' {
			if _, err := c.w.Write([]byte{'\r', '\n'}); err != nil {
				return written, err
			}
		} else {
			if err := c.w.WriteByte(b); err != nil {
				return written, err
			}
		}
		written++
	}
	return written, nil
}

func (c *crlfWriter) Flush() error {
	return c.w.Flush()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func nowISO() string {
	return time.Now().Format("2006-01-02T15:04:05.0000000")
}

func newGUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ── Source classification ─────────────────────────────────────────────────────

type pouKind int

const (
	kindProgram pouKind = iota
	kindFunctionBlock
	kindFunction
	kindGVL
	kindDUT
	kindConfiguration
)

func detectKind(line string) pouKind {
	u := strings.ToUpper(strings.TrimSpace(line))
	switch {
	case strings.HasPrefix(u, "PROGRAM ") || u == "PROGRAM":
		return kindProgram
	case strings.HasPrefix(u, "FUNCTION_BLOCK ") || u == "FUNCTION_BLOCK":
		return kindFunctionBlock
	case strings.HasPrefix(u, "FUNCTION ") || u == "FUNCTION":
		return kindFunction
	case strings.HasPrefix(u, "VAR_GLOBAL"):
		return kindGVL
	case strings.HasPrefix(u, "TYPE ") || u == "TYPE":
		return kindDUT
	case strings.HasPrefix(u, "CONFIGURATION ") || u == "CONFIGURATION":
		return kindConfiguration
	default:
		return kindProgram
	}
}

func firstCodeLine(lines []string) (int, string) {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" ||
			strings.HasPrefix(t, "//") ||
			strings.HasPrefix(t, "(*") ||
			strings.HasPrefix(t, "{attribute") ||
			strings.HasPrefix(t, "{") {
			continue
		}
		return i, t
	}
	return 0, ""
}

// ── IEC type mapping ──────────────────────────────────────────────────────────

// simpleIECTypes are the basic types that map to empty XML elements.
var simpleIECTypes = map[string]bool{
	"BOOL": true, "BYTE": true, "WORD": true, "DWORD": true, "LWORD": true,
	"SINT": true, "INT": true, "DINT": true, "LINT": true,
	"USINT": true, "UINT": true, "UDINT": true, "ULINT": true,
	"REAL": true, "LREAL": true,
	"TIME": true, "DATE": true, "TOD": true, "DT": true,
	"TIME_OF_DAY": true, "DATE_AND_TIME": true,
	"WSTRING": true,
}

func writeTypeXML(w io.Writer, indent, typeName string) {
	upper := strings.ToUpper(strings.TrimSpace(typeName))

	// Simple IEC types → empty element
	if simpleIECTypes[upper] {
		fmt.Fprintf(w, "%s<%s />\n", indent, upper)
		return
	}

	// STRING or STRING(n)
	if upper == "STRING" || strings.HasPrefix(upper, "STRING(") {
		if strings.HasPrefix(upper, "STRING(") {
			lenStr := strings.TrimPrefix(upper, "STRING(")
			lenStr = strings.TrimSuffix(lenStr, ")")
			fmt.Fprintf(w, "%s<string length=\"%s\" />\n", indent, lenStr)
		} else {
			fmt.Fprintf(w, "%s<string />\n", indent)
		}
		return
	}

	// WSTRING(n)
	if strings.HasPrefix(upper, "WSTRING(") {
		lenStr := strings.TrimPrefix(upper, "WSTRING(")
		lenStr = strings.TrimSuffix(lenStr, ")")
		fmt.Fprintf(w, "%s<wstring length=\"%s\" />\n", indent, lenStr)
		return
	}

	// ARRAY[lo..hi] OF basetype
	if strings.HasPrefix(upper, "ARRAY") {
		lo, hi, baseType := parseArrayType(typeName)
		if baseType != "" {
			fmt.Fprintf(w, "%s<array>\n", indent)
			fmt.Fprintf(w, "%s  <dimension lower=\"%s\" upper=\"%s\" />\n", indent, lo, hi)
			fmt.Fprintf(w, "%s  <baseType>\n", indent)
			writeTypeXML(w, indent+"    ", baseType)
			fmt.Fprintf(w, "%s  </baseType>\n", indent)
			fmt.Fprintf(w, "%s</array>\n", indent)
			return
		}
	}

	// Everything else → derived type
	fmt.Fprintf(w, "%s<derived name=\"%s\" />\n", indent, xmlEscape(strings.TrimSpace(typeName)))
}

func parseArrayType(typeName string) (lo, hi, baseType string) {
	// ARRAY[0..9] OF INT
	upper := strings.ToUpper(typeName)
	ofIdx := strings.Index(upper, "] OF ")
	if ofIdx < 0 {
		return "", "", ""
	}
	bracketStart := strings.Index(typeName, "[")
	if bracketStart < 0 {
		return "", "", ""
	}
	rangeStr := typeName[bracketStart+1 : ofIdx]
	dotIdx := strings.Index(rangeStr, "..")
	if dotIdx < 0 {
		return "", "", ""
	}
	lo = strings.TrimSpace(rangeStr[:dotIdx])
	hi = strings.TrimSpace(rangeStr[dotIdx+2:])
	baseType = strings.TrimSpace(typeName[ofIdx+5:])
	return
}

// ── Variable parsing ─────────────────────────────────────────────────────────

type varInfo struct {
	name     string
	typeName string
	initVal  string
	address  string
	comment  string
}

type varBlock struct {
	kind string // VAR_INPUT, VAR_OUTPUT, VAR_IN_OUT, VAR, VAR_TEMP
	vars []varInfo
}

// parseVarDecl parses a single variable declaration line.
// e.g. "x : INT := 42; // counter" or "pin AT %I0.0 : BOOL;"
func parseVarDecl(line string) *varInfo {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "(*") || strings.HasPrefix(line, "{") {
		return nil
	}

	// Extract trailing comment
	comment := ""
	if idx := strings.Index(line, "//"); idx > 0 {
		comment = strings.TrimSpace(line[idx+2:])
		line = strings.TrimSpace(line[:idx])
	} else if idx := strings.Index(line, "(*"); idx > 0 {
		endIdx := strings.Index(line, "*)")
		if endIdx > idx {
			comment = strings.TrimSpace(line[idx+2 : endIdx])
			line = strings.TrimSpace(line[:idx])
		}
	}

	line = strings.TrimSuffix(strings.TrimSpace(line), ";")
	line = strings.TrimSpace(line)

	// Find first ':' that is not part of ':='
	colonIdx := -1
	for i := 0; i < len(line); i++ {
		if line[i] == ':' && (i+1 >= len(line) || line[i+1] != '=') {
			colonIdx = i
			break
		}
	}
	if colonIdx < 0 {
		return nil
	}

	namePart := strings.TrimSpace(line[:colonIdx])
	rest := strings.TrimSpace(line[colonIdx+1:])

	// Handle AT address: "name AT %addr"
	address := ""
	upperName := strings.ToUpper(namePart)
	if atIdx := strings.Index(upperName, " AT "); atIdx >= 0 {
		address = strings.TrimSpace(namePart[atIdx+4:])
		namePart = strings.TrimSpace(namePart[:atIdx])
	}

	// Split type and initial value on ':='
	initVal := ""
	if idx := strings.Index(rest, ":="); idx >= 0 {
		initVal = strings.TrimSpace(rest[idx+2:])
		rest = strings.TrimSpace(rest[:idx])
	}

	return &varInfo{
		name:     namePart,
		typeName: rest,
		initVal:  initVal,
		address:  address,
		comment:  comment,
	}
}

// parseVarBlocks parses the declaration section of a POU into variable blocks.
func parseVarBlocks(declText string) []varBlock {
	lines := strings.Split(declText, "\n")
	var blocks []varBlock
	var current *varBlock
	nestingDepth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		// Detect VAR block start
		kind := ""
		switch {
		case strings.HasPrefix(upper, "VAR_INPUT"):
			kind = "VAR_INPUT"
		case strings.HasPrefix(upper, "VAR_OUTPUT"):
			kind = "VAR_OUTPUT"
		case strings.HasPrefix(upper, "VAR_IN_OUT"):
			kind = "VAR_IN_OUT"
		case strings.HasPrefix(upper, "VAR_TEMP"):
			kind = "VAR_TEMP"
		case strings.HasPrefix(upper, "VAR_GLOBAL"):
			kind = "VAR_GLOBAL"
		case upper == "VAR" || strings.HasPrefix(upper, "VAR "):
			kind = "VAR"
		}

		if kind != "" && current == nil {
			b := varBlock{kind: kind}
			blocks = append(blocks, b)
			current = &blocks[len(blocks)-1]
			nestingDepth = 1
			continue
		}

		if current != nil {
			if upper == "END_VAR" {
				nestingDepth--
				if nestingDepth <= 0 {
					current = nil
				}
				continue
			}

			v := parseVarDecl(trimmed)
			if v != nil {
				current.vars = append(current.vars, *v)
			}
		}
	}
	return blocks
}

// ── POU declaration parsing ──────────────────────────────────────────────────

func parsePOUName(firstLine string) string {
	parts := strings.Fields(firstLine)
	switch strings.ToUpper(parts[0]) {
	case "FUNCTION":
		if len(parts) >= 2 {
			return strings.TrimSuffix(parts[1], ":")
		}
	case "FUNCTION_BLOCK":
		if len(parts) >= 2 {
			return parts[1]
		}
	case "PROGRAM":
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}

func parseFunctionReturnType(firstLine string) string {
	// FUNCTION Name : ReturnType
	colonIdx := strings.Index(firstLine, ":")
	if colonIdx < 0 {
		return ""
	}
	return strings.TrimSpace(firstLine[colonIdx+1:])
}

// splitDeclImpl splits POU source into declaration and implementation.
func splitDeclImpl(src string) (decl, impl string) {
	lines := strings.Split(src, "\n")
	lastEndVar := -1
	for i, l := range lines {
		u := strings.ToUpper(strings.TrimSpace(l))
		if u == "END_VAR" {
			lastEndVar = i
		}
	}
	if lastEndVar < 0 {
		return src, ""
	}
	declLines := lines[:lastEndVar+1]
	implLines := lines[lastEndVar+1:]
	// Remove trailing END_xxx keyword and empty lines from impl
	for len(implLines) > 0 {
		u := strings.ToUpper(strings.TrimSpace(implLines[len(implLines)-1]))
		if u == "END_PROGRAM" || u == "END_FUNCTION_BLOCK" || u == "END_FUNCTION" || u == "" {
			implLines = implLines[:len(implLines)-1]
		} else {
			break
		}
	}
	for len(implLines) > 0 && strings.TrimSpace(implLines[0]) == "" {
		implLines = implLines[1:]
	}
	decl = strings.Join(declLines, "\n")
	impl = strings.Join(implLines, "\n")
	return
}

// extractGVLFromConfiguration strips the CONFIGURATION wrapper.
func extractGVLFromConfiguration(lines []string) string {
	inVarGlobal := false
	var out []string
	for _, l := range lines {
		u := strings.ToUpper(strings.TrimSpace(l))
		if strings.HasPrefix(u, "VAR_GLOBAL") {
			inVarGlobal = true
		}
		if inVarGlobal {
			// Dedent one level
			trimmed := strings.TrimPrefix(l, "\t")
			if trimmed == l && len(l) >= 4 && l[:4] == "    " {
				trimmed = l[4:]
			}
			out = append(out, trimmed)
			if strings.HasPrefix(u, "END_VAR") {
				inVarGlobal = false
			}
		}
	}
	return strings.Join(out, "\n")
}

// ── Parsed objects ────────────────────────────────────────────────────────────

type stObject struct {
	name      string
	kind      pouKind
	rawSource string // original source
	decl      string // declaration part (for POUs) or full source (for DUT/GVL)
	impl      string // implementation body (for POUs only)
	pathParts []string
	objectID  string // GUID for CoDeSys
}

func isPOUWithImpl(k pouKind) bool {
	return k == kindProgram || k == kindFunctionBlock || k == kindFunction
}

// ── DUT parsing ──────────────────────────────────────────────────────────────

type dutInfo struct {
	name     string
	typeName string // "STRUCT" or "ENUM"
	fields   []varInfo
	enumVals []enumVal
	rawDecl  string // original TYPE block text
	objectID string // GUID for CoDeSys
}

type enumVal struct {
	name  string
	value string
}

func parseDUTs(src string) []dutInfo {
	lines := strings.Split(src, "\n")
	var duts []dutInfo

	i := 0
	for i < len(lines) {
		u := strings.ToUpper(strings.TrimSpace(lines[i]))
		if !(strings.HasPrefix(u, "TYPE ") || u == "TYPE") {
			i++
			continue
		}

		// Collect lines until END_TYPE
		start := i
		i++
		for i < len(lines) {
			if strings.ToUpper(strings.TrimSpace(lines[i])) == "END_TYPE" {
				i++
				break
			}
			i++
		}
		typeBlock := strings.Join(lines[start:i], "\n")

		// Parse individual type declarations within the block
		parsed := parseSingleTypeBlock(lines[start:i])
		for _, d := range parsed {
			d.rawDecl = typeBlock
		}
		duts = append(duts, parsed...)
	}
	return duts
}

func parseSingleTypeBlock(lines []string) []dutInfo {
	var duts []dutInfo
	var currentName string
	var inStruct, inEnum bool
	var fields []varInfo
	var enumVals []enumVal

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		// Detect standalone STRUCT keyword after "TypeName :"
		if currentName != "" && !inStruct && !inEnum && (upper == "STRUCT" || upper == "STRUCT;") {
			inStruct = true
			fields = nil
			continue
		}

		// Match "TypeName : STRUCT" or "TypeName :"
		if !inStruct && !inEnum && strings.Contains(trimmed, ":") {
			parts := strings.SplitN(trimmed, ":", 2)
			name := strings.TrimSpace(parts[0])
			rest := strings.TrimSpace(parts[1])
			restUpper := strings.ToUpper(rest)

			// Skip TYPE keyword
			if strings.ToUpper(name) == "TYPE" || name == "" {
				name = ""
			}
			// Handle "TYPE TypeName : STRUCT"
			if strings.HasPrefix(strings.ToUpper(name), "TYPE ") {
				name = strings.TrimSpace(name[5:])
			}

			if name != "" {
				currentName = name
				if strings.HasPrefix(restUpper, "STRUCT") {
					inStruct = true
					fields = nil
					continue
				}
				// Handle inline enum start: "TypeName : (" or "TypeName : (val1, val2)"
				if strings.HasPrefix(rest, "(") {
					inEnum = true
					enumVals = nil
					line := strings.TrimPrefix(rest, "(")
					line = strings.TrimSuffix(line, ")")
					line = strings.TrimSuffix(line, ");")
					for _, ev := range splitEnumLine(line) {
						enumVals = append(enumVals, ev)
					}
					if strings.HasSuffix(rest, ");") || strings.HasSuffix(rest, ")") {
						inEnum = false
						duts = append(duts, dutInfo{
							name:     currentName,
							typeName: "ENUM",
							enumVals: enumVals,
						})
						currentName = ""
					}
					continue
				}
			} else {
				continue
			}
		}

		// Detect enum start: "(..."
		if currentName != "" && !inStruct && !inEnum && strings.HasPrefix(trimmed, "(") {
			inEnum = true
			enumVals = nil
			// May have values on same line
			line := strings.TrimPrefix(trimmed, "(")
			line = strings.TrimSuffix(line, ")")
			line = strings.TrimSuffix(line, ");")
			for _, ev := range splitEnumLine(line) {
				enumVals = append(enumVals, ev)
			}
			if strings.HasSuffix(trimmed, ");") || strings.HasSuffix(trimmed, ")") {
				inEnum = false
				duts = append(duts, dutInfo{
					name:     currentName,
					typeName: "ENUM",
					enumVals: enumVals,
				})
				currentName = ""
			}
			continue
		}

		if inEnum {
			line := strings.TrimSuffix(strings.TrimSuffix(trimmed, ");"), ")")
			line = strings.TrimSpace(line)
			if line != "" {
				for _, ev := range splitEnumLine(line) {
					enumVals = append(enumVals, ev)
				}
			}
			if strings.HasSuffix(trimmed, ");") || strings.HasSuffix(trimmed, ")") {
				inEnum = false
				duts = append(duts, dutInfo{
					name:     currentName,
					typeName: "ENUM",
					enumVals: enumVals,
				})
				currentName = ""
			}
			continue
		}

		if inStruct {
			if strings.HasPrefix(upper, "END_STRUCT") {
				inStruct = false
				duts = append(duts, dutInfo{
					name:     currentName,
					typeName: "STRUCT",
					fields:   fields,
				})
				currentName = ""
				continue
			}
			v := parseVarDecl(trimmed)
			if v != nil {
				fields = append(fields, *v)
			}
		}
	}
	return duts
}

func splitEnumLine(line string) []enumVal {
	var vals []enumVal
	parts := strings.Split(line, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ev := enumVal{}
		if idx := strings.Index(p, ":="); idx >= 0 {
			ev.name = strings.TrimSpace(p[:idx])
			ev.value = strings.TrimSpace(p[idx+2:])
		} else {
			ev.name = p
		}
		if ev.name != "" {
			vals = append(vals, ev)
		}
	}
	return vals
}

// ── XML writing ──────────────────────────────────────────────────────────────

func writeXHTMLContent(w io.Writer, indent, text string) {
	fmt.Fprintf(w, "%s<xhtml xmlns=\"http://www.w3.org/1999/xhtml\">%s</xhtml>\n", indent, xmlEscape(text))
}

func writeVariable(w io.Writer, indent string, v varInfo) {
	if v.address != "" {
		fmt.Fprintf(w, "%s<variable name=\"%s\" address=\"%s\">\n", indent, xmlEscape(v.name), xmlEscape(v.address))
	} else {
		fmt.Fprintf(w, "%s<variable name=\"%s\">\n", indent, xmlEscape(v.name))
	}
	fmt.Fprintf(w, "%s  <type>\n", indent)
	writeTypeXML(w, indent+"    ", v.typeName)
	fmt.Fprintf(w, "%s  </type>\n", indent)
	if v.initVal != "" {
		fmt.Fprintf(w, "%s  <initialValue>\n", indent)
		fmt.Fprintf(w, "%s    <simpleValue value=\"%s\" />\n", indent, xmlEscape(v.initVal))
		fmt.Fprintf(w, "%s  </initialValue>\n", indent)
	}
	if v.comment != "" {
		fmt.Fprintf(w, "%s  <documentation>\n", indent)
		writeXHTMLContent(w, indent+"    ", v.comment)
		fmt.Fprintf(w, "%s  </documentation>\n", indent)
	}
	fmt.Fprintf(w, "%s</variable>\n", indent)
}

func writeVarList(w io.Writer, indent, tag string, vars []varInfo) {
	if len(vars) == 0 {
		return
	}
	fmt.Fprintf(w, "%s<%s>\n", indent, tag)
	for _, v := range vars {
		writeVariable(w, indent+"  ", v)
	}
	fmt.Fprintf(w, "%s</%s>\n", indent, tag)
}

func writeObjectIdAddData(w io.Writer, indent, objectID string) {
	fmt.Fprintf(w, "%s<addData>\n", indent)
	fmt.Fprintf(w, "%s  <data name=\"http://www.3s-software.com/plcopenxml/objectid\" handleUnknown=\"discard\">\n", indent)
	fmt.Fprintf(w, "%s    <ObjectId>%s</ObjectId>\n", indent, objectID)
	fmt.Fprintf(w, "%s  </data>\n", indent)
	fmt.Fprintf(w, "%s</addData>\n", indent)
}

func writePOU(w io.Writer, obj *stObject) {
	// Determine pouType string
	pouType := "program"
	switch obj.kind {
	case kindFunctionBlock:
		pouType = "functionBlock"
	case kindFunction:
		pouType = "function"
	}

	fmt.Fprintf(w, "      <pou name=\"%s\" pouType=\"%s\">\n", xmlEscape(obj.name), pouType)

	// Parse variable blocks from declaration
	blocks := parseVarBlocks(obj.decl)

	// Interface
	fmt.Fprintf(w, "        <interface>\n")

	// For FUNCTION: add returnType
	if obj.kind == kindFunction {
		lines := strings.Split(obj.decl, "\n")
		if len(lines) > 0 {
			retType := parseFunctionReturnType(lines[0])
			if retType != "" {
				fmt.Fprintf(w, "          <returnType>\n")
				writeTypeXML(w, "            ", retType)
				fmt.Fprintf(w, "          </returnType>\n")
			}
		}
	}

	// Write variable blocks in PLCOpen order
	for _, b := range blocks {
		switch b.kind {
		case "VAR_INPUT":
			writeVarList(w, "          ", "inputVars", b.vars)
		case "VAR_OUTPUT":
			writeVarList(w, "          ", "outputVars", b.vars)
		case "VAR_IN_OUT":
			writeVarList(w, "          ", "inOutVars", b.vars)
		case "VAR":
			writeVarList(w, "          ", "localVars", b.vars)
		case "VAR_TEMP":
			writeVarList(w, "          ", "tempVars", b.vars)
		}
	}
	fmt.Fprintf(w, "        </interface>\n")

	// Body
	fmt.Fprintf(w, "        <body>\n")
	fmt.Fprintf(w, "          <ST>\n")
	writeXHTMLContent(w, "            ", obj.impl)
	fmt.Fprintf(w, "          </ST>\n")
	fmt.Fprintf(w, "        </body>\n")

	// ObjectId (CoDeSys compatibility)
	writeObjectIdAddData(w, "        ", obj.objectID)

	fmt.Fprintf(w, "      </pou>\n")
}

func writeDataType(w io.Writer, d dutInfo) {
	fmt.Fprintf(w, "      <dataType name=\"%s\">\n", xmlEscape(d.name))

	switch d.typeName {
	case "STRUCT":
		fmt.Fprintf(w, "        <baseType>\n")
		fmt.Fprintf(w, "          <struct>\n")
		for _, f := range d.fields {
			writeVariable(w, "            ", f)
		}
		fmt.Fprintf(w, "          </struct>\n")
		fmt.Fprintf(w, "        </baseType>\n")

	case "ENUM":
		fmt.Fprintf(w, "        <baseType>\n")
		fmt.Fprintf(w, "          <enum>\n")
		fmt.Fprintf(w, "            <values>\n")
		for _, ev := range d.enumVals {
			if ev.value != "" {
				fmt.Fprintf(w, "              <value name=\"%s\" value=\"%s\" />\n", xmlEscape(ev.name), xmlEscape(ev.value))
			} else {
				fmt.Fprintf(w, "              <value name=\"%s\" />\n", xmlEscape(ev.name))
			}
		}
		fmt.Fprintf(w, "            </values>\n")
		fmt.Fprintf(w, "          </enum>\n")
		fmt.Fprintf(w, "        </baseType>\n")
	}

	// ObjectId
	if d.objectID != "" {
		writeObjectIdAddData(w, "        ", d.objectID)
	}

	fmt.Fprintf(w, "      </dataType>\n")
}

func writeGlobalVarsAddData(w io.Writer, obj *stObject) {
	blocks := parseVarBlocks(obj.decl)
	fmt.Fprintf(w, "    <data name=\"http://www.3s-software.com/plcopenxml/globalvars\" handleUnknown=\"implementation\">\n")
	fmt.Fprintf(w, "      <globalVars name=\"%s\">\n", xmlEscape(obj.name))
	for _, b := range blocks {
		if b.kind == "VAR_GLOBAL" {
			for _, v := range b.vars {
				writeVariable(w, "        ", v)
			}
		}
	}
	writeObjectIdAddData(w, "        ", obj.objectID)
	fmt.Fprintf(w, "      </globalVars>\n")
	fmt.Fprintf(w, "    </data>\n")
}

// ── File conversion ───────────────────────────────────────────────────────────

func convertFile(stPath, srcRoot string) (*stObject, error) {
	raw, err := os.ReadFile(stPath)
	if err != nil {
		return nil, err
	}
	src := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(src, "\n")

	_, firstLine := firstCodeLine(lines)
	kind := detectKind(firstLine)
	name := strings.TrimSuffix(filepath.Base(stPath), ".st")

	relDir, _ := filepath.Rel(srcRoot, filepath.Dir(stPath))
	var pathParts []string
	if relDir != "." && relDir != "" {
		for _, p := range strings.Split(relDir, string(os.PathSeparator)) {
			if p != "" {
				pathParts = append(pathParts, p)
			}
		}
	}
	pathParts = append(pathParts, name)

	var decl, impl string

	if kind == kindConfiguration {
		decl = extractGVLFromConfiguration(lines)
		kind = kindGVL
	} else if isPOUWithImpl(kind) {
		decl, impl = splitDeclImpl(src)
	} else {
		decl = src
	}

	return &stObject{
		name:      name,
		kind:      kind,
		rawSource: src,
		decl:      strings.TrimRight(decl, "\n") + "\n",
		impl:      strings.TrimRight(impl, "\n") + "\n",
		pathParts: pathParts,
	}, nil
}

// ── ProjectStructure XML ──────────────────────────────────────────────────────

type projFolder struct {
	name     string
	children map[string]*projFolder
	objects  []projObject // object names with IDs
}

type projObject struct {
	name     string
	objectID string
}

func newProjFolder(name string) *projFolder {
	return &projFolder{name: name, children: make(map[string]*projFolder)}
}

func ensureProjPath(root *projFolder, parts []string) *projFolder {
	cur := root
	for _, p := range parts {
		if cur.children[p] == nil {
			cur.children[p] = newProjFolder(p)
		}
		cur = cur.children[p]
	}
	return cur
}

func writeProjStructure(w io.Writer, f *projFolder, indent string) {
	for _, name := range sortedKeys(f.children) {
		child := f.children[name]
		fmt.Fprintf(w, "%s<Folder Name=\"%s\">\n", indent, xmlEscape(child.name))
		writeProjStructure(w, child, indent+"  ")
		fmt.Fprintf(w, "%s</Folder>\n", indent)
	}
	for _, obj := range f.objects {
		fmt.Fprintf(w, "%s<Object Name=\"%s\" ObjectId=\"%s\" />\n", indent, xmlEscape(obj.name), obj.objectID)
	}
}

func sortedKeys(m map[string]*projFolder) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	srcDir := flag.String("src", "src", "source root directory")
	outDir := flag.String("out", "build", "output directory")
	outName := flag.String("name", "plcopen_export", "output filename (without extension)")
	company := flag.String("company", "iec-st-tools", "company name in file header")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "st2plcopen — Convert IEC 61131-3 .st files to PLCOpen XML (TC6) format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Walk source files
	var files []string
	_ = filepath.Walk(*srcDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.ToLower(filepath.Ext(path)) == ".st" {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no .st files found in", *srcDir)
		os.Exit(1)
	}

	// Parse all files
	var pous []*stObject
	var gvls []*stObject
	var dutSources []*stObject
	projRoot := newProjFolder("Application")

	for _, f := range files {
		obj, err := convertFile(f, *srcDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error processing %s: %v\n", f, err)
			continue
		}

		obj.objectID = newGUID()

		folderParts := obj.pathParts[:len(obj.pathParts)-1]
		folder := ensureProjPath(projRoot, folderParts)
		folder.objects = append(folder.objects, projObject{name: obj.name, objectID: obj.objectID})

		switch obj.kind {
		case kindGVL:
			gvls = append(gvls, obj)
		case kindDUT:
			dutSources = append(dutSources, obj)
		default:
			pous = append(pous, obj)
		}

		fmt.Printf("  %-30s  kind=%-15s  path=%s\n",
			filepath.Base(f), kindName(obj.kind), strings.Join(obj.pathParts, "/"))
	}

	// Parse DUTs into structured form
	var allDUTs []dutInfo
	for _, obj := range dutSources {
		duts := parseDUTs(obj.rawSource)
		for i := range duts {
			if duts[i].rawDecl == "" {
				duts[i].rawDecl = obj.rawSource
			}
			duts[i].objectID = newGUID()
		}
		allDUTs = append(allDUTs, duts...)
	}

	// Write output
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output dir:", err)
		os.Exit(1)
	}

	outFile := filepath.Join(*outDir, *outName+".xml")
	f, err := os.Create(outFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output file:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := newCRLFWriter(f)
	defer w.Flush()

	ts := nowISO()

	// XML header
	fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n")
	fmt.Fprintf(w, "<project xmlns=\"http://www.plcopen.org/xml/tc6_0200\">\n")

	// FileHeader
	fmt.Fprintf(w, "  <fileHeader companyName=\"%s\" productName=\"CODESYS\" productVersion=\"CODESYS V3.5 SP19 Patch 6\" creationDateTime=\"%s\" />\n",
		xmlEscape(*company), ts)

	// ContentHeader with ProjectInformation
	fmt.Fprintf(w, "  <contentHeader name=\"%s.project\" modificationDateTime=\"%s\">\n", xmlEscape(*outName), ts)
	fmt.Fprintf(w, "    <coordinateInfo>\n")
	fmt.Fprintf(w, "      <fbd>\n        <scaling x=\"1\" y=\"1\" />\n      </fbd>\n")
	fmt.Fprintf(w, "      <ld>\n        <scaling x=\"1\" y=\"1\" />\n      </ld>\n")
	fmt.Fprintf(w, "      <sfc>\n        <scaling x=\"1\" y=\"1\" />\n      </sfc>\n")
	fmt.Fprintf(w, "    </coordinateInfo>\n")
	fmt.Fprintf(w, "    <addData>\n")
	fmt.Fprintf(w, "      <data name=\"http://www.3s-software.com/plcopenxml/projectinformation\" handleUnknown=\"implementation\">\n")
	fmt.Fprintf(w, "        <ProjectInformation />\n")
	fmt.Fprintf(w, "      </data>\n")
	fmt.Fprintf(w, "    </addData>\n")
	fmt.Fprintf(w, "  </contentHeader>\n")

	// Types
	fmt.Fprintf(w, "  <types>\n")

	// DataTypes
	if len(allDUTs) > 0 {
		fmt.Fprintf(w, "    <dataTypes>\n")
		for _, d := range allDUTs {
			writeDataType(w, d)
		}
		fmt.Fprintf(w, "    </dataTypes>\n")
	} else {
		fmt.Fprintf(w, "    <dataTypes />\n")
	}

	// POUs
	if len(pous) > 0 {
		fmt.Fprintf(w, "    <pous>\n")
		for _, obj := range pous {
			writePOU(w, obj)
		}
		fmt.Fprintf(w, "    </pous>\n")
	} else {
		fmt.Fprintf(w, "    <pous />\n")
	}

	fmt.Fprintf(w, "  </types>\n")

	// Instances (CoDeSys: always empty configurations)
	fmt.Fprintf(w, "  <instances>\n")
	fmt.Fprintf(w, "    <configurations />\n")
	fmt.Fprintf(w, "  </instances>\n")

	// addData: GVLs + ProjectStructure
	fmt.Fprintf(w, "  <addData>\n")

	// GVLs as addData > globalvars (CoDeSys 3.5 format)
	for _, obj := range gvls {
		writeGlobalVarsAddData(w, obj)
	}

	// ProjectStructure
	fmt.Fprintf(w, "    <data name=\"http://www.3s-software.com/plcopenxml/projectstructure\" handleUnknown=\"discard\">\n")
	fmt.Fprintf(w, "      <ProjectStructure>\n")
	writeProjStructure(w, projRoot, "        ")
	fmt.Fprintf(w, "      </ProjectStructure>\n")
	fmt.Fprintf(w, "    </data>\n")
	fmt.Fprintf(w, "  </addData>\n")

	fmt.Fprintf(w, "</project>\n")

	total := len(pous) + len(gvls) + len(allDUTs)
	fmt.Printf("\nWritten: %s  (%d POUs, %d GVLs, %d DUTs = %d objects)\n",
		outFile, len(pous), len(gvls), len(allDUTs), total)
}

func kindName(k pouKind) string {
	switch k {
	case kindProgram:
		return "PROGRAM"
	case kindFunctionBlock:
		return "FUNCTION_BLOCK"
	case kindFunction:
		return "FUNCTION"
	case kindGVL:
		return "GVL"
	case kindDUT:
		return "DUT"
	default:
		return "unknown"
	}
}
