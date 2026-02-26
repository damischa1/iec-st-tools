// st2exp35 — CoDeSys 3.5 XML export generator
// Converts IEC 61131-3 compatible .st files to the CoDeSys 3.5 .export XML format.
//
// CoDeSys 3.5 uses a structured XML format with GUID-based object trees,
// completely different from the CoDeSys 2.3 plain-text .EXP format.
//
// Usage:
//
//	st2exp35 [-src <dir>] [-out <dir>] [-name <file>] [-base <path>]
//
// Flags:
//
//	-src   source root directory (default "src")
//	-out   output directory (default "build")
//	-name  output filename without extension (default "export")
//	-base  comma-separated CoDeSys tree base path (default "Device,PLC Logic,Application")
//
// Object types detected from first code line:
//
//	FUNCTION         → POU
//	FUNCTION_BLOCK   → POU
//	PROGRAM          → POU  (split into Interface declaration + Implementation body)
//	TYPE             → DUT (data unit type)
//	VAR_GLOBAL       → GVL (global variable list)
//	CONFIGURATION    → stripped to VAR_GLOBAL blocks (trust-LSP compatibility)
//
// Directory structure below -src maps to CoDeSys tree path:
//
//	src/UserCode/Handlers/Foo.st → base + ["UserCode","Handlers"] path
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── CoDeSys 3.5 well-known type GUIDs ────────────────────────────────────────

const (
	tFolder   = "738bea1e-99bb-4f04-90bb-a7a567e74e3a" // folder / namespace
	tGVL      = "ffbfa93a-b94d-45fc-a329-229860183b1d" // Global Variable List
	tDUT      = "2db5746d-d284-4425-9f7f-2663a34b0ebc" // Data Unit Type (TYPE)
	tPOU      = "6f9dac99-8de1-4efc-8465-68ac443b7d08" // POU (PROGRAM/FB/FUNCTION)
	tTextIF   = "a9ed5b7e-75c5-4651-af16-d2c27e98cb94" // text interface (declaration)
	tTextImpl = "3b83b776-fb25-43b8-99f2-3c507c9143fc" // text implementation (body)
	tTextDoc  = "f3878285-8e4f-490b-bb1b-9acbb7eb04db" // text document
)

// Known minimal profile blob. This encodes CoDeSys 3.5 version metadata.
const profileBlob = "AAEAAAD/////AQAAAAAAAAAMAgAAAAAAAAUBAAAAIVN5c3RlbS5Db2xsZWN0aW9ucy5IYXNoU" +
	"GFibGUHAAAACkxvYWRGYWN0b3IHVmVyc2lvbghDb21wYXJlchBIYXNoQ29kZVByb3ZpZGVyCEhhc2hTaXplBEtleXM" +
	"GVmFsdWVzAAADAAAFBQsIHFN5c3RlbS5Db2xsZWN0aW9ucy5JQ29tcGFyZXIkU3lzdGVtLkNvbGxlY3Rpb25zLklI" +
	"YXNoQ29kZVByb3ZpZGVyCOxROD97AAAACQMAAAAJBAAAAA=="

// ── Helpers ───────────────────────────────────────────────────────────────────

func newGUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func dotnetTicks() int64 {
	const ticksPerSecond int64 = 10_000_000
	const epochOffset int64 = 621355968000000000
	return time.Now().UTC().UnixNano()/100 + epochOffset
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// ── Source parsing ────────────────────────────────────────────────────────────

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

func extractGVLFromConfiguration(lines []string) string {
	inVarGlobal := false
	depth := 0
	var out []string
	for _, l := range lines {
		u := strings.ToUpper(strings.TrimSpace(l))
		if strings.HasPrefix(u, "VAR_GLOBAL") {
			inVarGlobal = true
			depth = 1
		}
		if inVarGlobal {
			trimmed := strings.TrimPrefix(l, "\t")
			if trimmed == l && len(l) >= 4 && l[:4] == "    " {
				trimmed = l[4:]
			}
			out = append(out, trimmed)
			if strings.HasPrefix(u, "END_VAR") {
				depth--
				if depth == 0 {
					inVarGlobal = false
				}
			}
		}
	}
	return strings.Join(out, "\n")
}

func isPOUWithImpl(k pouKind) bool {
	return k == kindProgram || k == kindFunctionBlock || k == kindFunction
}

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

// ── Object model ─────────────────────────────────────────────────────────────

type stObject struct {
	guid      string
	name      string
	kind      pouKind
	decl      string
	impl      string
	pathParts []string
}

func (o *stObject) typeGUID() string {
	switch o.kind {
	case kindGVL:
		return tGVL
	case kindDUT:
		return tDUT
	default:
		return tPOU
	}
}

func (o *stObject) embeddedGuids() []string {
	if isPOUWithImpl(o.kind) {
		return []string{tTextIF, tTextImpl}
	}
	return []string{tTextIF}
}

// ── Folder tree ───────────────────────────────────────────────────────────────

type folderNode struct {
	guid     string
	name     string
	parent   *folderNode
	children map[string]*folderNode
	objects  []*stObject
}

func newFolderNode(name string, parent *folderNode) *folderNode {
	return &folderNode{
		guid:     newGUID(),
		name:     name,
		parent:   parent,
		children: make(map[string]*folderNode),
	}
}

func ensurePath(root *folderNode, parts []string) *folderNode {
	cur := root
	for _, p := range parts {
		if cur.children[p] == nil {
			cur.children[p] = newFolderNode(p, cur)
		}
		cur = cur.children[p]
	}
	return cur
}

func codeysFolderPath(base []string, f *folderNode) []string {
	var parts []string
	for cur := f; cur != nil && cur.name != ""; cur = cur.parent {
		parts = append([]string{cur.name}, parts...)
	}
	return append(base, parts...)
}

// ── XML writer ────────────────────────────────────────────────────────────────

func writePathArray(w *os.File, codeysParts []string) {
	fmt.Fprintf(w, "      <Array Name=\"Path\" Type=\"string\">\n")
	for _, p := range codeysParts {
		fmt.Fprintf(w, "        <Single Type=\"string\">%s</Single>\n", xmlEscape(p))
	}
	fmt.Fprintf(w, "      </Array>\n")
}

func writePropertiesForObject(w *os.File, parentSVGuid string, parentGUID string) {
	fmt.Fprintf(w, "        <Dictionary Type=\"{2c41fa04-1834-41c1-816e-303c7aa2c05b}\" Name=\"Properties\">\n")
	fmt.Fprintf(w, "          <Entry>\n")
	fmt.Fprintf(w, "            <Key>\n")
	fmt.Fprintf(w, "              <Single Type=\"System.Guid\">24568a24-c491-472c-a21f-ee5d33859fab</Single>\n")
	fmt.Fprintf(w, "            </Key>\n")
	fmt.Fprintf(w, "            <Value>\n")
	fmt.Fprintf(w, "              <Single Type=\"{24568a24-c491-472c-a21f-ee5d33859fab}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "                <Single Name=\"MemoryReserveForOnlineChange\" Type=\"int\">0</Single>\n")
	fmt.Fprintf(w, "                <Single Name=\"ExcludeFromBuild\" Type=\"bool\">False</Single>\n")
	fmt.Fprintf(w, "                <Single Name=\"External\" Type=\"bool\">False</Single>\n")
	fmt.Fprintf(w, "                <Single Name=\"EnableSystemCall\" Type=\"bool\">False</Single>\n")
	fmt.Fprintf(w, "                <Single Name=\"CompilerDefines\" Type=\"string\"></Single>\n")
	fmt.Fprintf(w, "                <Single Name=\"LinkAlways\" Type=\"bool\">False</Single>\n")
	fmt.Fprintf(w, "                <Array Name=\"Undefines\" Type=\"string\" />\n")
	fmt.Fprintf(w, "              </Single>\n")
	fmt.Fprintf(w, "            </Value>\n")
	fmt.Fprintf(w, "          </Entry>\n")
	fmt.Fprintf(w, "          <Entry>\n")
	fmt.Fprintf(w, "            <Key>\n")
	fmt.Fprintf(w, "              <Single Type=\"System.Guid\">829a18f2-c514-4f6e-9634-1df173429203</Single>\n")
	fmt.Fprintf(w, "            </Key>\n")
	fmt.Fprintf(w, "            <Value>\n")
	fmt.Fprintf(w, "              <Single Type=\"{829a18f2-c514-4f6e-9634-1df173429203}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "                <Dictionary Type=\"{fa2ee218-a39b-4b6d-b249-49dbddbd168a}\" Name=\"ParentObjects\">\n")
	fmt.Fprintf(w, "                  <Entry>\n")
	fmt.Fprintf(w, "                    <Key>\n")
	fmt.Fprintf(w, "                      <Single Type=\"System.Guid\">%s</Single>\n", parentSVGuid)
	fmt.Fprintf(w, "                    </Key>\n")
	fmt.Fprintf(w, "                    <Value>\n")
	fmt.Fprintf(w, "                      <Single Type=\"System.Guid\">%s</Single>\n", parentGUID)
	fmt.Fprintf(w, "                    </Value>\n")
	fmt.Fprintf(w, "                  </Entry>\n")
	fmt.Fprintf(w, "                </Dictionary>\n")
	fmt.Fprintf(w, "              </Single>\n")
	fmt.Fprintf(w, "            </Value>\n")
	fmt.Fprintf(w, "          </Entry>\n")
	fmt.Fprintf(w, "        </Dictionary>\n")
}

func writeTextDocument(w *os.File, text, lineInfoID, tag string) {
	fmt.Fprintf(w, "          <Single Name=\"%s\" Type=\"{%s}\" Method=\"IArchivable\">\n", tag, tTextDoc)
	fmt.Fprintf(w, "            <Single Name=\"TextBlobForSerialisation\" Type=\"string\">%s</Single>\n", xmlEscape(text))
	fmt.Fprintf(w, "            <Single Name=\"LineInfoPersistence\" Type=\"string\">%s</Single>\n", lineInfoID)
	fmt.Fprintf(w, "          </Single>\n")
}

func writeFolderEntry(w *os.File, f *folderNode, svRootGUID string, base []string) {
	parentGUID := "00000000-0000-0000-0000-000000000000"
	parentSVGUID := svRootGUID
	if f.parent != nil && f.parent.name != "" {
		parentGUID = f.parent.guid
		parentSVGUID = f.parent.guid
	}
	path := codeysFolderPath(base, f)

	fmt.Fprintf(w, "    <Single Type=\"{6198ad31-4b98-445c-927f-3258a0e82fe3}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "      <Single Name=\"IsRoot\" Type=\"bool\">True</Single>\n")
	fmt.Fprintf(w, "      <Single Name=\"MetaObject\" Type=\"{81297157-7ec9-45ce-845e-84cab2b88ade}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "        <Single Name=\"Guid\" Type=\"System.Guid\">%s</Single>\n", f.guid)
	fmt.Fprintf(w, "        <Single Name=\"ParentGuid\" Type=\"System.Guid\">%s</Single>\n", parentGUID)
	fmt.Fprintf(w, "        <Single Name=\"Name\" Type=\"string\">%s</Single>\n", xmlEscape(f.name))
	fmt.Fprintf(w, "        <Dictionary Type=\"{2c41fa04-1834-41c1-816e-303c7aa2c05b}\" Name=\"Properties\">\n")
	fmt.Fprintf(w, "          <Entry>\n")
	fmt.Fprintf(w, "            <Key>\n")
	fmt.Fprintf(w, "              <Single Type=\"System.Guid\">829a18f2-c514-4f6e-9634-1df173429203</Single>\n")
	fmt.Fprintf(w, "            </Key>\n")
	fmt.Fprintf(w, "            <Value>\n")
	fmt.Fprintf(w, "              <Single Type=\"{829a18f2-c514-4f6e-9634-1df173429203}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "                <Dictionary Type=\"{fa2ee218-a39b-4b6d-b249-49dbddbd168a}\" Name=\"ParentObjects\">\n")
	fmt.Fprintf(w, "                  <Entry>\n")
	fmt.Fprintf(w, "                    <Key>\n")
	fmt.Fprintf(w, "                      <Single Type=\"System.Guid\">%s</Single>\n", svRootGUID)
	fmt.Fprintf(w, "                    </Key>\n")
	fmt.Fprintf(w, "                    <Value>\n")
	fmt.Fprintf(w, "                      <Single Type=\"System.Guid\">%s</Single>\n", parentSVGUID)
	fmt.Fprintf(w, "                    </Value>\n")
	fmt.Fprintf(w, "                  </Entry>\n")
	fmt.Fprintf(w, "                </Dictionary>\n")
	fmt.Fprintf(w, "              </Single>\n")
	fmt.Fprintf(w, "            </Value>\n")
	fmt.Fprintf(w, "          </Entry>\n")
	fmt.Fprintf(w, "        </Dictionary>\n")
	fmt.Fprintf(w, "        <Single Name=\"TypeGuid\" Type=\"System.Guid\">%s</Single>\n", tFolder)
	fmt.Fprintf(w, "        <Null Name=\"EmbeddedTypeGuids\" />\n")
	fmt.Fprintf(w, "        <Single Name=\"Timestamp\" Type=\"long\">%d</Single>\n", dotnetTicks())
	fmt.Fprintf(w, "      </Single>\n")
	fmt.Fprintf(w, "      <Single Name=\"Object\" Type=\"{%s}\" Method=\"IArchivable\">\n", tFolder)
	fmt.Fprintf(w, "        <Single Name=\"StructuredViewGuid\" Type=\"System.Guid\">%s</Single>\n", svRootGUID)
	fmt.Fprintf(w, "      </Single>\n")
	fmt.Fprintf(w, "      <Single Name=\"ParentSVNodeGuid\" Type=\"System.Guid\">%s</Single>\n", parentSVGUID)
	writePathArray(w, path)
	fmt.Fprintf(w, "      <Single Name=\"Index\" Type=\"int\">-1</Single>\n")
	fmt.Fprintf(w, "    </Single>\n")
}

func writeObjectEntry(w *os.File, obj *stObject, parentFolder *folderNode, svRootGUID string, base []string) {
	parentFolderGUID := svRootGUID
	if parentFolder != nil && parentFolder.name != "" {
		parentFolderGUID = parentFolder.guid
	}
	tyGUID := obj.typeGUID()
	ts := dotnetTicks()
	codeyPath := append(base, obj.pathParts...)

	fmt.Fprintf(w, "    <Single Type=\"{6198ad31-4b98-445c-927f-3258a0e82fe3}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "      <Single Name=\"IsRoot\" Type=\"bool\">False</Single>\n")
	fmt.Fprintf(w, "      <Single Name=\"MetaObject\" Type=\"{81297157-7ec9-45ce-845e-84cab2b88ade}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "        <Single Name=\"Guid\" Type=\"System.Guid\">%s</Single>\n", obj.guid)
	fmt.Fprintf(w, "        <Single Name=\"ParentGuid\" Type=\"System.Guid\">%s</Single>\n", parentFolderGUID)
	fmt.Fprintf(w, "        <Single Name=\"Name\" Type=\"string\">%s</Single>\n", xmlEscape(obj.name))
	writePropertiesForObject(w, svRootGUID, parentFolderGUID)
	fmt.Fprintf(w, "        <Single Name=\"TypeGuid\" Type=\"System.Guid\">%s</Single>\n", tyGUID)
	embGuids := obj.embeddedGuids()
	fmt.Fprintf(w, "        <Array Name=\"EmbeddedTypeGuids\" Type=\"System.Guid\">\n")
	for _, g := range embGuids {
		fmt.Fprintf(w, "          <Single Type=\"System.Guid\">%s</Single>\n", g)
	}
	fmt.Fprintf(w, "        </Array>\n")
	fmt.Fprintf(w, "        <Single Name=\"Timestamp\" Type=\"long\">%d</Single>\n", ts)
	fmt.Fprintf(w, "      </Single>\n")

	fmt.Fprintf(w, "      <Single Name=\"Object\" Type=\"{%s}\" Method=\"IArchivable\">\n", tyGUID)
	lineBase := fmt.Sprintf("%s_%s", obj.guid, obj.name)
	switch obj.kind {
	case kindGVL:
		fmt.Fprintf(w, "        <Single Name=\"Interface\" Type=\"{%s}\" Method=\"IArchivable\">\n", tTextIF)
		writeTextDocument(w, obj.decl, lineBase+"_Decl_LineIds", "TextDocument")
		fmt.Fprintf(w, "        </Single>\n")
		fmt.Fprintf(w, "        <Null Name=\"NetVarProperties\" />\n")
		fmt.Fprintf(w, "        <Single Name=\"ParameterList\" Type=\"bool\">False</Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"AddAttributeSubsequent\" Type=\"bool\">False</Single>\n")
	case kindDUT:
		fmt.Fprintf(w, "        <Single Name=\"Interface\" Type=\"{%s}\" Method=\"IArchivable\">\n", tTextIF)
		writeTextDocument(w, obj.decl, lineBase+"_Decl_LineIds", "TextDocument")
		fmt.Fprintf(w, "        </Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"UniqueIdGenerator\" Type=\"string\">0</Single>\n")
	default:
		fmt.Fprintf(w, "        <Single Name=\"SpecialFunc\" Type=\"{0db3d7bb-cde0-4416-9a7b-ce49a0124323}\">None</Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"Implementation\" Type=\"{%s}\" Method=\"IArchivable\">\n", tTextImpl)
		writeTextDocument(w, obj.impl, lineBase+"_Impl_LineIds", "TextDocument")
		fmt.Fprintf(w, "        </Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"Interface\" Type=\"{%s}\" Method=\"IArchivable\">\n", tTextIF)
		writeTextDocument(w, obj.decl, lineBase+"_Decl_LineIds", "TextDocument")
		fmt.Fprintf(w, "        </Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"UniqueIdGenerator\" Type=\"string\">0</Single>\n")
		fmt.Fprintf(w, "        <Single Name=\"POULevel\" Type=\"{8e575c5b-1d37-49c6-941b-5c0ec7874787}\">Standard</Single>\n")
		fmt.Fprintf(w, "        <List Name=\"ChildObjectGuids\" Type=\"System.Collections.ArrayList\" />\n")
		fmt.Fprintf(w, "        <Single Name=\"AddAttributeSubsequent\" Type=\"bool\">False</Single>\n")
	}
	fmt.Fprintf(w, "      </Single>\n")

	fmt.Fprintf(w, "      <Single Name=\"ParentSVNodeGuid\" Type=\"System.Guid\">%s</Single>\n", parentFolderGUID)
	writePathArray(w, codeyPath)
	fmt.Fprintf(w, "      <Single Name=\"Index\" Type=\"int\">-1</Single>\n")
	fmt.Fprintf(w, "    </Single>\n")
}

func walkFolders(w *os.File, f *folderNode, svRootGUID string, base []string) {
	if f.name != "" {
		writeFolderEntry(w, f, svRootGUID, base)
	}
	for _, key := range sortedKeys(f.children) {
		walkFolders(w, f.children[key], svRootGUID, base)
	}
	for _, obj := range f.objects {
		writeObjectEntry(w, obj, f, svRootGUID, base)
	}
}

func sortedKeys(m map[string]*folderNode) []string {
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
		guid:      newGUID(),
		name:      name,
		kind:      kind,
		decl:      strings.TrimRight(decl, "\n") + "\n",
		impl:      strings.TrimRight(impl, "\n") + "\n",
		pathParts: pathParts,
	}, nil
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	srcDir := flag.String("src", "src", "source root directory")
	outDir := flag.String("out", "build", "output directory")
	outName := flag.String("name", "export", "output filename (without extension)")
	basePath := flag.String("base", "Device,PLC Logic,Application", "comma-separated CoDeSys tree base path")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "st2exp35 - Convert IEC 61131-3 .st files to CoDeSys 3.5 .export XML format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
	}
	flag.Parse()

	base := strings.Split(*basePath, ",")
	for i, b := range base {
		base[i] = strings.TrimSpace(b)
	}

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

	syntheticRoot := newFolderNode("", nil)
	svRootGUID := newGUID()

	var allObjs []*stObject
	for _, f := range files {
		obj, err := convertFile(f, *srcDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error processing %s: %v\n", f, err)
			continue
		}
		folderParts := obj.pathParts[:len(obj.pathParts)-1]
		folder := ensurePath(syntheticRoot, folderParts)
		folder.objects = append(folder.objects, obj)
		allObjs = append(allObjs, obj)
		fmt.Printf("  %-30s  kind=%-15s  path=%s\n",
			filepath.Base(f), kindName(obj.kind), strings.Join(obj.pathParts, "/"))
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output dir:", err)
		os.Exit(1)
	}

	outFile := filepath.Join(*outDir, *outName+".export")
	w, err := os.Create(outFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output file:", err)
		os.Exit(1)
	}
	defer w.Close()

	fmt.Fprintf(w, "<ExportFile>\n")
	fmt.Fprintf(w, "  <StructuredView Guid=\"{%s}\">\n", svRootGUID)
	fmt.Fprintf(w, "<Single xml:space=\"preserve\" Type=\"{3daac5e4-660e-42e4-9cea-3711b98bfb63}\" Method=\"IArchivable\">\n")
	fmt.Fprintf(w, "  <Array Name=\"Profile\" Type=\"byte\">%s</Array>\n", profileBlob)
	fmt.Fprintf(w, "  <List2 Name=\"EntryList\">\n")

	walkFolders(w, syntheticRoot, svRootGUID, base)

	fmt.Fprintf(w, "  </List2>\n")
	fmt.Fprintf(w, "  <Single Name=\"ProfileName\" Type=\"string\">CODESYS V3.5 SP19 Patch 6</Single>\n")
	fmt.Fprintf(w, "</Single>  </StructuredView>\n")
	fmt.Fprintf(w, "</ExportFile>\n")

	fmt.Printf("\nWritten: %s  (%d objects)\n", outFile, len(allObjs))
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
