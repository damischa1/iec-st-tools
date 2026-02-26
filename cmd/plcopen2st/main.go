// plcopen2st — PLCOpen XML (TC6) importer
// Converts PLCOpen XML files to IEC 61131-3 compatible .st source files.
//
// Reads a PLCOpen XML file (as exported by CoDeSys 3.5, TwinCAT 3, etc.)
// and writes one .st file per POU, GVL, and DUT found.
//
// GVL objects are wrapped in a CONFIGURATION block as required by trust-LSP
// (IEC 61131-3 Ed.3).
//
// Non-ST body types (FBD, Ladder, SFC) are replaced with a minimal ST stub
// preserving the interface declaration.
//
// Usage:
//
//	plcopen2st -in <file.xml> [-out <dir>] [-flat]
//
// Flags:
//
//	-in    input PLCOpen XML file (required)
//	-out   output root directory for .st files (default "src")
//	-flat  write all files to -out directly, no subdirectories
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
// Reusable approach: parse PLCOpen XML into a generic tree, then extract
// content from known paths. This avoids needing exact struct definitions
// for the full TC6 schema.

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
	return n.Content
}

// textDeep recursively collects all text content within the node.
func (n *xmlNode) textDeep() string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(n.Content)
	for _, c := range n.Children {
		sb.WriteString(c.textDeep())
	}
	return sb.String()
}

// findChildByAttr finds a child element with a specific attribute value.
func (n *xmlNode) findChildByAttr(tag, attrName, attrValue string) *xmlNode {
	for _, c := range n.Children {
		if c.XMLName.Local == tag && c.attr(attrName) == attrValue {
			return c
		}
	}
	return nil
}

// ── POU extraction ────────────────────────────────────────────────────────────

type importedPOU struct {
	name     string
	pouType  string // "function", "functionBlock", "program"
	declText string // plain-text declaration
	bodyText string // ST body text
	bodyType string // "ST", "FBD", "LD", "SFC", "IL"
	folder   string // project folder path
}

// extractInterfaceAsPlainText gets the CoDeSys-specific InterfaceAsPlainText
// from addData sections, which is the most reliable source for declarations.
func extractInterfaceAsPlainText(node *xmlNode) string {
	addData := node.child("addData")
	if addData == nil {
		return ""
	}
	for _, data := range addData.allChildren("data") {
		if strings.Contains(data.attr("name"), "interfaceasplaintext") {
			ipt := data.child("InterfaceAsPlainText")
			if ipt != nil {
				xhtml := ipt.child("xhtml")
				if xhtml != nil {
					return strings.TrimSpace(xhtml.textDeep())
				}
			}
		}
	}
	return ""
}

// reconstructDeclaration rebuilds the text declaration from structured XML vars.
func reconstructDeclaration(pouNode *xmlNode) string {
	pouType := pouNode.attr("pouType")
	pouName := pouNode.attr("name")
	iface := pouNode.child("interface")
	if iface == nil {
		return ""
	}

	var sb strings.Builder

	// POU header line
	switch pouType {
	case "function":
		retType := iface.child("returnType")
		ret := "BOOL"
		if retType != nil {
			ret = extractTypeName(retType)
		}
		fmt.Fprintf(&sb, "FUNCTION %s : %s\n", pouName, ret)
	case "functionBlock":
		fmt.Fprintf(&sb, "FUNCTION_BLOCK %s\n", pouName)
	case "program":
		fmt.Fprintf(&sb, "PROGRAM %s\n", pouName)
	}

	// VAR blocks
	varSections := []struct {
		tag string
		kw  string
	}{
		{"inputVars", "VAR_INPUT"},
		{"outputVars", "VAR_OUTPUT"},
		{"inOutVars", "VAR_IN_OUT"},
		{"localVars", "VAR"},
		{"tempVars", "VAR_TEMP"},
	}

	for _, sec := range varSections {
		varList := iface.child(sec.tag)
		if varList == nil {
			continue
		}
		vars := varList.allChildren("variable")
		if len(vars) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s\n", sec.kw)
		for _, v := range vars {
			name := v.attr("name")
			addr := v.attr("address")
			typeName := "BOOL"
			typeNode := v.child("type")
			if typeNode != nil {
				typeName = extractTypeName(typeNode)
			}

			initVal := ""
			ivNode := v.child("initialValue")
			if ivNode != nil {
				sv := ivNode.child("simpleValue")
				if sv != nil {
					initVal = sv.attr("value")
				}
			}

			// Build declaration line
			decl := "    "
			if addr != "" {
				decl += fmt.Sprintf("%s AT %s : %s", name, addr, typeName)
			} else {
				decl += fmt.Sprintf("%s : %s", name, typeName)
			}
			if initVal != "" {
				decl += " := " + initVal
			}
			decl += ";"

			// Comment from documentation
			docNode := v.child("documentation")
			if docNode != nil {
				xhtml := docNode.child("xhtml")
				if xhtml != nil {
					comment := strings.TrimSpace(xhtml.textDeep())
					if comment != "" {
						decl += " // " + comment
					}
				}
			}

			fmt.Fprintf(&sb, "%s\n", decl)
		}
		fmt.Fprintf(&sb, "END_VAR\n")
	}

	return sb.String()
}

// extractTypeName gets a human-readable type name from a PLCOpen type element.
func extractTypeName(typeNode *xmlNode) string {
	for _, c := range typeNode.Children {
		tag := c.XMLName.Local
		switch strings.ToUpper(tag) {
		case "BOOL", "BYTE", "WORD", "DWORD", "LWORD",
			"SINT", "INT", "DINT", "LINT",
			"USINT", "UINT", "UDINT", "ULINT",
			"REAL", "LREAL",
			"TIME", "DATE", "TOD", "DT",
			"TIME_OF_DAY", "DATE_AND_TIME",
			"WSTRING":
			return strings.ToUpper(tag)

		case "STRING":
			length := c.attr("length")
			if length != "" {
				return fmt.Sprintf("STRING(%s)", length)
			}
			return "STRING"

		case "DERIVED":
			return c.attr("name")

		case "ARRAY":
			dim := c.child("dimension")
			baseType := c.child("baseType")
			if dim != nil && baseType != nil {
				lo := dim.attr("lower")
				hi := dim.attr("upper")
				bt := extractTypeName(baseType)
				return fmt.Sprintf("ARRAY[%s..%s] OF %s", lo, hi, bt)
			}
			return "ARRAY"

		case "STRUCT":
			return "STRUCT"

		case "ENUM":
			return "ENUM"
		}
	}
	return "BOOL"
}

// extractBodyText gets the ST body text from a POU's body element.
// Returns the text and the body type ("ST", "FBD", "LD", "SFC", "IL").
func extractBodyText(bodyNode *xmlNode) (string, string) {
	if bodyNode == nil {
		return "", ""
	}

	// Check for ST body
	stNode := bodyNode.child("ST")
	if stNode != nil {
		xhtml := stNode.child("xhtml")
		if xhtml != nil {
			return strings.TrimSpace(xhtml.textDeep()), "ST"
		}
		return "", "ST"
	}

	// Check for other body types
	for _, tag := range []string{"FBD", "LD", "SFC", "IL"} {
		if bodyNode.child(tag) != nil {
			return "", tag
		}
	}

	return "", ""
}

// ── DUT extraction ────────────────────────────────────────────────────────────

type importedDUT struct {
	name     string
	declText string
}

func reconstructDUT(dtNode *xmlNode) string {
	name := dtNode.attr("name")
	if name == "" {
		return ""
	}

	// Check for InterfaceAsPlainText first
	ipt := extractInterfaceAsPlainText(dtNode)
	if ipt != "" {
		return ipt
	}

	baseType := dtNode.child("baseType")
	if baseType == nil {
		return fmt.Sprintf("TYPE %s :\n    // Unknown type\nEND_TYPE\n", name)
	}

	var sb strings.Builder

	// STRUCT
	structNode := baseType.child("struct")
	if structNode != nil {
		fmt.Fprintf(&sb, "TYPE %s :\nSTRUCT\n", name)
		for _, v := range structNode.allChildren("variable") {
			varName := v.attr("name")
			typeName := "BOOL"
			typeNode := v.child("type")
			if typeNode != nil {
				typeName = extractTypeName(typeNode)
			}
			initVal := ""
			ivNode := v.child("initialValue")
			if ivNode != nil {
				sv := ivNode.child("simpleValue")
				if sv != nil {
					initVal = sv.attr("value")
				}
			}
			line := fmt.Sprintf("    %s : %s", varName, typeName)
			if initVal != "" {
				line += " := " + initVal
			}
			line += ";"
			fmt.Fprintf(&sb, "%s\n", line)
		}
		fmt.Fprintf(&sb, "END_STRUCT\nEND_TYPE\n")
		return sb.String()
	}

	// ENUM
	enumNode := baseType.child("enum")
	if enumNode != nil {
		fmt.Fprintf(&sb, "TYPE %s :\n(\n", name)
		valuesNode := enumNode.child("values")
		if valuesNode != nil {
			vals := valuesNode.allChildren("value")
			for i, v := range vals {
				eName := v.attr("name")
				eVal := ""
				sv := v.child("simpleValue")
				if sv != nil {
					eVal = sv.attr("value")
				}
				line := "    " + eName
				if eVal != "" {
					line += " := " + eVal
				}
				if i < len(vals)-1 {
					line += ","
				}
				fmt.Fprintf(&sb, "%s\n", line)
			}
		}
		fmt.Fprintf(&sb, ");\nEND_TYPE\n")
		return sb.String()
	}

	return fmt.Sprintf("TYPE %s :\n    // Unsupported base type structure\nEND_TYPE\n", name)
}

// ── GVL extraction ────────────────────────────────────────────────────────────

type importedGVL struct {
	name     string
	declText string
}

func reconstructGVL(gvlNode *xmlNode) string {
	_ = gvlNode.attr("name")

	// Check for InterfaceAsPlainText first
	ipt := extractInterfaceAsPlainText(gvlNode)
	if ipt != "" {
		return ipt
	}

	// Reconstruct from structured vars
	var sb strings.Builder
	fmt.Fprintf(&sb, "VAR_GLOBAL\n")
	for _, v := range gvlNode.allChildren("variable") {
		varName := v.attr("name")
		typeName := "BOOL"
		typeNode := v.child("type")
		if typeNode != nil {
			typeName = extractTypeName(typeNode)
		}
		initVal := ""
		ivNode := v.child("initialValue")
		if ivNode != nil {
			sv := ivNode.child("simpleValue")
			if sv != nil {
				initVal = sv.attr("value")
			}
		}
		line := fmt.Sprintf("    %s : %s", varName, typeName)
		if initVal != "" {
			line += " := " + initVal
		}
		line += ";"
		fmt.Fprintf(&sb, "%s\n", line)
	}
	fmt.Fprintf(&sb, "END_VAR\n")
	return sb.String()
}

// ── Stub generation ──────────────────────────────────────────────────────────

func pouEndKeyword(pouType string) string {
	switch pouType {
	case "function":
		return "END_FUNCTION"
	case "functionBlock":
		return "END_FUNCTION_BLOCK"
	default:
		return "END_PROGRAM"
	}
}

func stubBody(decl string) string {
	lines := []string{
		"// ** GENERATED STUB — original implementation is non-ST (FBD/Ladder/SFC) **",
		"// Adapt this body for your application logic.",
		"",
	}

	// Assign default values to output variables
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

// ── File generation ───────────────────────────────────────────────────────────

func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

func generatePOUST(pou importedPOU) string {
	endKW := pouEndKeyword(pou.pouType)
	body := pou.bodyText
	stubNote := ""

	if pou.bodyType != "ST" || body == "" {
		body = stubBody(pou.declText)
		if pou.bodyType != "ST" && pou.bodyType != "" {
			stubNote = "\n// NOTE: Original implementation was non-ST (" + pou.bodyType + "). Stub generated."
		}
	}

	decl := strings.TrimRight(pou.declText, "\n")
	body = strings.TrimRight(body, "\n")

	if body == "" {
		return fmt.Sprintf("%s%s\n%s\n", decl, stubNote, endKW)
	}
	return fmt.Sprintf("%s%s\n%s\n%s\n", decl, stubNote, body, endKW)
}

func generateGVLST(gvl importedGVL) string {
	d := strings.TrimSpace(gvl.declText)
	// If already wrapped in CONFIGURATION, keep as-is
	if strings.HasPrefix(strings.ToUpper(d), "CONFIGURATION") {
		return d + "\n"
	}
	// Wrap in CONFIGURATION for trust-LSP
	return fmt.Sprintf("// trust-LSP wrapper — compiler extracts VAR_GLOBAL automatically\nCONFIGURATION %s\n%s\nEND_CONFIGURATION\n",
		gvl.name, indentBlock(gvl.declText, "    "))
}

func generateDUTST(dut importedDUT) string {
	return strings.TrimRight(dut.declText, "\n") + "\n"
}

// ── ProjectStructure folder resolution ────────────────────────────────────────

func extractProjectFolders(root *xmlNode) map[string]string {
	// Builds a map of object-name → folder-path from ProjectStructure
	folders := make(map[string]string)

	addData := root.child("addData")
	if addData == nil {
		return folders
	}

	for _, data := range addData.allChildren("data") {
		if strings.Contains(data.attr("name"), "projectstructure") {
			ps := data.child("ProjectStructure")
			if ps != nil {
				walkProjectStructure(ps, "", folders)
			}
		}
	}
	return folders
}

func walkProjectStructure(node *xmlNode, prefix string, folders map[string]string) {
	for _, c := range node.Children {
		switch c.XMLName.Local {
		case "Folder":
			name := c.attr("Name")
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			walkProjectStructure(c, path, folders)
		case "Object":
			name := c.attr("Name")
			folders[name] = prefix
		}
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	inFile := flag.String("in", "", "input PLCOpen XML file (required)")
	outDir := flag.String("out", "src", "output root directory")
	flat := flag.Bool("flat", false, "write all files flat, no subdirectories")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "plcopen2st — Import PLCOpen XML (TC6) files to IEC 61131-3 .st format\n")
		fmt.Fprintln(os.Stderr, "Usage:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *inFile == "" {
		fmt.Fprintln(os.Stderr, "usage: plcopen2st -in <file.xml> [-out <dir>] [-flat]")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*inFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read input:", err)
		os.Exit(1)
	}

	var root xmlNode
	if err := xml.Unmarshal(raw, &root); err != nil {
		fmt.Fprintln(os.Stderr, "XML parse error:", err)
		os.Exit(1)
	}

	// Extract ProjectStructure for folder mapping
	projFolders := extractProjectFolders(&root)

	// Collect POUs
	var allPOUs []importedPOU
	var allDUTs []importedDUT
	var allGVLs []importedGVL

	// ── POUs from <types><pous> ───────────────────────────────────────────
	types := root.child("types")
	if types != nil {
		pous := types.child("pous")
		if pous != nil {
			for _, pouNode := range pous.allChildren("pou") {
				pou := extractPOU(pouNode, projFolders)
				allPOUs = append(allPOUs, pou)
			}
		}

		// DUTs from <types><dataTypes>
		dataTypes := types.child("dataTypes")
		if dataTypes != nil {
			for _, dtNode := range dataTypes.allChildren("dataType") {
				name := dtNode.attr("name")
				declText := reconstructDUT(dtNode)
				allDUTs = append(allDUTs, importedDUT{
					name:     name,
					declText: declText,
				})
			}
		}
	}

	// ── POUs from addData (CoDeSys-specific) ──────────────────────────────
	// CoDeSys puts POUs in addData > data > pou elements
	extractAddDataPOUs(&root, projFolders, &allPOUs)

	// ── GVLs from instances ───────────────────────────────────────────────
	instances := root.child("instances")
	if instances != nil {
		configs := instances.child("configurations")
		if configs != nil {
			extractGVLsRecursive(configs, &allGVLs)
		}
	}

	// ── GVLs from addData ────────────────────────────────────────────────
	addData := root.child("addData")
	if addData != nil {
		for _, data := range addData.allChildren("data") {
			res := data.child("resource")
			if res != nil {
				extractGVLsRecursive(res, &allGVLs)
			}
		}
	}

	// Write output files
	written, skipped, stubbed := 0, 0, 0

	for _, pou := range allPOUs {
		content := generatePOUST(pou)
		relPath := pou.name + ".st"
		if !*flat {
			folder := projFolders[pou.name]
			if folder == "" {
				folder = pou.folder
			}
			if folder != "" {
				relPath = filepath.Join(strings.ReplaceAll(folder, "/", string(os.PathSeparator)), relPath)
			}
		}

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
		if pou.bodyType != "ST" && pou.bodyType != "" {
			tag = fmt.Sprintf(" [STUB:%s]", pou.bodyType)
			stubbed++
		}
		fmt.Printf("  %-10s  %s%s\n", pouTypeLabel(pou.pouType), outPath, tag)
		written++
	}

	for _, dut := range allDUTs {
		content := generateDUTST(dut)
		relPath := dut.name + ".st"
		if !*flat {
			folder := projFolders[dut.name]
			if folder != "" {
				relPath = filepath.Join(strings.ReplaceAll(folder, "/", string(os.PathSeparator)), relPath)
			}
		}

		outPath := filepath.Join(*outDir, relPath)
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create dir for %s: %v\n", outPath, err)
			continue
		}
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", outPath, err)
			continue
		}

		fmt.Printf("  %-10s  %s\n", "DUT", outPath)
		written++
	}

	for _, gvl := range allGVLs {
		content := generateGVLST(gvl)
		relPath := gvl.name + ".st"
		if !*flat {
			folder := projFolders[gvl.name]
			if folder != "" {
				relPath = filepath.Join(strings.ReplaceAll(folder, "/", string(os.PathSeparator)), relPath)
			}
		}

		outPath := filepath.Join(*outDir, relPath)
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create dir for %s: %v\n", outPath, err)
			continue
		}
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", outPath, err)
			continue
		}

		fmt.Printf("  %-10s  %s\n", "GVL", outPath)
		written++
	}

	fmt.Printf("\nDone: %d written (%d stubs), %d skipped\n", written, stubbed, skipped)
}

// ── POU extraction helpers ────────────────────────────────────────────────────

func extractPOU(pouNode *xmlNode, projFolders map[string]string) importedPOU {
	name := pouNode.attr("name")
	pouType := pouNode.attr("pouType")

	// Try InterfaceAsPlainText first (most reliable)
	declText := extractInterfaceAsPlainText(pouNode)

	// Fall back to reconstructing from structured interface
	if declText == "" {
		declText = reconstructDeclaration(pouNode)
	}

	// Extract body
	bodyText := ""
	bodyType := ""
	bodyNode := pouNode.child("body")
	if bodyNode != nil {
		bodyText, bodyType = extractBodyText(bodyNode)
	}

	folder := projFolders[name]

	return importedPOU{
		name:     name,
		pouType:  pouType,
		declText: declText,
		bodyText: bodyText,
		bodyType: bodyType,
		folder:   folder,
	}
}

func extractAddDataPOUs(node *xmlNode, projFolders map[string]string, allPOUs *[]importedPOU) {
	// Look for POUs in addData sections (CoDeSys-specific)
	addData := node.child("addData")
	if addData != nil {
		for _, data := range addData.allChildren("data") {
			// Check for pou elements
			for _, pouNode := range data.allChildren("pou") {
				name := pouNode.attr("name")
				// Skip if already found in standard location
				found := false
				for _, existing := range *allPOUs {
					if existing.name == name {
						found = true
						break
					}
				}
				if !found {
					pou := extractPOU(pouNode, projFolders)
					*allPOUs = append(*allPOUs, pou)
				}
			}

			// Also check in resource elements
			res := data.child("resource")
			if res != nil {
				extractAddDataPOUs(res, projFolders, allPOUs)
			}
		}
	}

	// Also check in nested elements (configuration, resource, etc.)
	for _, c := range node.Children {
		if c.XMLName.Local == "configuration" || c.XMLName.Local == "resource" {
			extractAddDataPOUs(c, projFolders, allPOUs)
		}
	}
}

func extractGVLsRecursive(node *xmlNode, allGVLs *[]importedGVL) {
	for _, c := range node.Children {
		switch c.XMLName.Local {
		case "globalVars":
			name := c.attr("name")
			if name == "" {
				name = "GlobalVars"
			}
			declText := reconstructGVL(c)
			*allGVLs = append(*allGVLs, importedGVL{
				name:     name,
				declText: declText,
			})
		case "configuration", "resource":
			extractGVLsRecursive(c, allGVLs)
		}
	}
}

func pouTypeLabel(pouType string) string {
	switch pouType {
	case "function":
		return "FUNCTION"
	case "functionBlock":
		return "FB"
	case "program":
		return "PROGRAM"
	default:
		return pouType
	}
}
