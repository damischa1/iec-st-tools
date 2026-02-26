// exp2st35 — CoDeSys 3.5 XML export → IEC 61131-3 .st file importer
//
// Reads a CoDeSys 3.5 .export XML file and writes one .st file per POU/GVL/DUT.
// Non-ST implementations (FBD, Ladder, SFC) are replaced with a minimal ST stub
// so the interface is preserved and usable as a code template.
//
// GVL objects are wrapped in a CONFIGURATION block as required by trust-LSP
// (IEC 61131-3 Ed.3).
//
// Usage:
//
//	exp2st35 -in <file.export> [-out <dir>] [-base <path>] [-strip <n>]
//
// Flags:
//
//	-in     input .export file (required)
//	-out    output root directory for .st files (default "src")
//	-base   comma-separated CoDeSys tree prefix to strip from Path
//	        (default "Device,PLC Logic,Application")
//	-strip  number of path elements to strip as prefix instead of using -base
//	        (overrides -base if > 0)
package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── Generic XML node tree ─────────────────────────────────────────────────────

type xmlNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Content  string     `xml:",chardata"`
	Children []*xmlNode `xml:",any"`
}

func (n *xmlNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *xmlNode) child(localName string) *xmlNode {
	for _, c := range n.Children {
		if c.XMLName.Local == localName {
			return c
		}
	}
	return nil
}

func (n *xmlNode) namedChild(nameAttr string) *xmlNode {
	for _, c := range n.Children {
		if c.attr("Name") == nameAttr {
			return c
		}
	}
	return nil
}

func (n *xmlNode) allChildren(localName string) []*xmlNode {
	var out []*xmlNode
	for _, c := range n.Children {
		if c.XMLName.Local == localName {
			out = append(out, c)
		}
	}
	return out
}

func (n *xmlNode) text() string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Content)
}

func (n *xmlNode) textBlob() string {
	if n == nil {
		return ""
	}
	td := n.namedChild("TextDocument")
	if td == nil {
		return ""
	}
	blob := td.namedChild("TextBlobForSerialisation")
	if blob == nil {
		return ""
	}
	return blob.text()
}

// ── CoDeSys 3.5 TypeGuids ─────────────────────────────────────────────────────

const (
	tFolder = "738bea1e-99bb-4f04-90bb-a7a567e74e3a"
	tGVL    = "ffbfa93a-b94d-45fc-a329-229860183b1d"
	tDUT    = "2db5746d-d284-4425-9f7f-2663a34b0ebc"
	tPOU    = "6f9dac99-8de1-4efc-8465-68ac443b7d08"
)

// ── Object extraction ─────────────────────────────────────────────────────────

type importedObj struct {
	name     string
	typeGUID string
	path     []string
	decl     string
	impl     string
	isRoot   bool
}

func parseEntry(entry *xmlNode) *importedObj {
	isRootNode := entry.namedChild("IsRoot")
	isRoot := isRootNode != nil && strings.EqualFold(isRootNode.text(), "true")

	meta := entry.namedChild("MetaObject")
	if meta == nil {
		return nil
	}

	nameNode := meta.namedChild("Name")
	if nameNode == nil {
		return nil
	}
	typeGUIDNode := meta.namedChild("TypeGuid")
	typeGUID := ""
	if typeGUIDNode != nil {
		typeGUID = typeGUIDNode.text()
	}

	var pathParts []string
	pathArr := entry.namedChild("Path")
	if pathArr != nil {
		for _, s := range pathArr.allChildren("Single") {
			pathParts = append(pathParts, s.text())
		}
	}

	var decl, impl string
	objNode := entry.namedChild("Object")
	if objNode != nil {
		ifaceNode := objNode.namedChild("Interface")
		if ifaceNode != nil {
			decl = ifaceNode.textBlob()
		}
		implNode := objNode.namedChild("Implementation")
		if implNode != nil {
			impl = implNode.textBlob()
		}
	}

	return &importedObj{
		name:     nameNode.text(),
		typeGUID: typeGUID,
		path:     pathParts,
		decl:     decl,
		impl:     impl,
		isRoot:   isRoot,
	}
}

// ── ST detection & stub generation ───────────────────────────────────────────

func isST(impl string) bool {
	t := strings.TrimSpace(impl)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "<") {
		return false
	}
	return true
}

func pouEndKeyword(decl string) string {
	for _, line := range strings.Split(decl, "\n") {
		u := strings.ToUpper(strings.TrimSpace(line))
		if strings.HasPrefix(u, "PROGRAM ") || u == "PROGRAM" {
			return "END_PROGRAM"
		}
		if strings.HasPrefix(u, "FUNCTION_BLOCK ") || u == "FUNCTION_BLOCK" {
			return "END_FUNCTION_BLOCK"
		}
		if strings.HasPrefix(u, "FUNCTION ") || u == "FUNCTION" {
			return "END_FUNCTION"
		}
	}
	return "END_PROGRAM"
}

func stubBody(decl string) string {
	var lines []string
	lines = append(lines, "// ** GENERATED STUB — original implementation is non-ST (FBD/Ladder/SFC) **")
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
				typeName := strings.ToUpper(strings.Fields(rest)[0])
				switch {
				case typeName == "BOOL" || strings.HasSuffix(typeName, "BOOL"):
					defaultVal = "FALSE"
				case strings.HasPrefix(typeName, "STRING"):
					defaultVal = "''"
				default:
					defaultVal = "0"
				}
			}
			lines = append(lines, fmt.Sprintf("%s := %s;", varName, defaultVal))
		}
	}
	return strings.Join(lines, "\n")
}

// ── File generation ───────────────────────────────────────────────────────────

func generateST(obj *importedObj, strip int) (content, relPath string) {
	stripped := obj.path
	if strip > 0 && len(stripped) > strip {
		stripped = stripped[strip:]
	}
	relPath = filepath.Join(append(stripped, obj.name+".st")...)

	switch obj.typeGUID {
	case tDUT:
		content = strings.TrimRight(obj.decl, "\n") + "\n"

	case tGVL:
		d := strings.TrimSpace(obj.decl)
		if strings.HasPrefix(strings.ToUpper(d), "CONFIGURATION") {
			content = d + "\n"
		} else {
			content = fmt.Sprintf("// trust-LSP wrapper — compiler extracts VAR_GLOBAL automatically\n"+
				"CONFIGURATION %s\n%s\nEND_CONFIGURATION\n",
				obj.name, indentBlock(obj.decl, "    "))
		}

	case tPOU:
		endKW := pouEndKeyword(obj.decl)
		body := obj.impl
		stubFlag := ""
		if !isST(body) {
			body = stubBody(obj.decl)
			stubFlag = "\n// NOTE: Original implementation was non-ST (FBD/Ladder/SFC). Stub generated."
		}
		decl := strings.TrimRight(obj.decl, "\n")
		body = strings.TrimRight(body, "\n")
		content = fmt.Sprintf("%s%s\n%s\n%s\n", decl, stubFlag, body, endKW)

	default:
		content = fmt.Sprintf("// Unknown TypeGuid: %s\n// Name: %s\n%s\n",
			obj.typeGUID, obj.name, obj.decl)
	}
	return
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

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	inFile := flag.String("in", "", "input .export file (required)")
	outDir := flag.String("out", "src", "output root directory")
	basePath := flag.String("base", "Device,PLC Logic,Application", "CoDeSys tree prefix to strip from Path")
	stripN := flag.Int("strip", 0, "number of leading path elements to strip (overrides -base)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "exp2st35 - Import CoDeSys 3.5 .export XML files to IEC 61131-3 .st format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *inFile == "" {
		fmt.Fprintln(os.Stderr, "usage: exp2st35 -in <file.export> [-out <dir>] [-base <prefix>] [-strip <n>]")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*inFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read input:", err)
		os.Exit(1)
	}

	strip := *stripN
	if strip == 0 {
		base := strings.Split(*basePath, ",")
		strip = len(base)
	}

	var root xmlNode
	if err := xml.Unmarshal(raw, &root); err != nil {
		fmt.Fprintln(os.Stderr, "XML parse error:", err)
		os.Exit(1)
	}

	svNode := root.child("StructuredView")
	if svNode == nil {
		fmt.Fprintln(os.Stderr, "no StructuredView element found")
		os.Exit(1)
	}
	topSingle := svNode.child("Single")
	if topSingle == nil {
		fmt.Fprintln(os.Stderr, "no top Single element found")
		os.Exit(1)
	}
	list2 := topSingle.namedChild("EntryList")
	if list2 == nil {
		fmt.Fprintln(os.Stderr, "no EntryList element found")
		os.Exit(1)
	}

	entries := list2.allChildren("Single")
	written, skipped, stubbed := 0, 0, 0

	for _, entry := range entries {
		obj := parseEntry(entry)
		if obj == nil {
			continue
		}
		if obj.isRoot && obj.typeGUID == tFolder {
			skipped++
			continue
		}
		if obj.decl == "" && obj.impl == "" {
			skipped++
			continue
		}

		wasStubbed := obj.typeGUID == tPOU && !isST(obj.impl)
		content, relPath := generateST(obj, strip)

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
		fmt.Printf("  %-10s  %s%s\n", kindLabel(obj.typeGUID), outPath, tag)
		written++
	}

	fmt.Printf("\nDone: %d written (%d stubs), %d skipped\n", written, stubbed, skipped)
}

func kindLabel(typeGUID string) string {
	switch typeGUID {
	case tGVL:
		return "GVL"
	case tDUT:
		return "DUT"
	case tPOU:
		return "POU"
	case tFolder:
		return "folder"
	default:
		return "unknown"
	}
}
