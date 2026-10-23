package blobfs_test

// The tripwire between the TLA+ specs and the Go code.
//
// Spec: ADR 0013 section 6 ("The tripwire"): a Go test, standard library
// only, fails when a symbol docs/tla/trace.tsv names is no longer in the file
// it names. docs/tla/README.md "Tables" sets trace.tsv's layout, and
// "Tripwire" says this test rejects a trace.tsv that breaks it. It checks
// presence only; whether the symbol still performs the action is review's.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// traceHeader is the exact first line of trace.tsv (README.md "Tables").
const traceHeader = "#spec\taction\tfile\tsymbol\tlocks\tdecided"

// traceFields names trace.tsv's fields, in order.
var traceFields = []string{"spec", "action", "file", "symbol", "locks", "decided"}

// traceParsed is one Go file the trace names, parsed once per check.
type traceParsed struct {
	file *ast.File
	err  error
}

// checkTrace judges tsv, the contents of a trace.tsv, against the Go files
// and TLA+ modules under root. It returns every problem it finds, each
// prefixed with the 1-based number of the line it is on, except the one
// problem that belongs to no line: a trace with no rows.
func checkTrace(root, tsv string) []string {
	var problems []string
	report := func(line int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: ", line)+fmt.Sprintf(format, args...))
	}
	cache := map[string]traceParsed{}
	rows := 0

	// LF line endings: every line ends in LF, the last one included. The
	// last line's LF ends it, and does not begin a blank line after it. An
	// empty tsv has no line, so only its header and its rows are missing.
	lines := strings.Split(strings.TrimSuffix(tsv, "\n"), "\n")
	if tsv != "" && !strings.HasSuffix(tsv, "\n") {
		report(len(lines), "does not end in LF: every line must, the last one included")
	}
	for i, line := range lines {
		n := i + 1
		for j := 0; j < len(line); j++ {
			if c := line[j]; c == '\r' {
				report(n, "has a carriage return: line endings must be LF")
				break
			} else if c >= 0x80 {
				report(n, "has byte %#x at offset %d, which is not ASCII", c, j)
				break
			} else if c != '\t' && (c < 0x20 || c == 0x7f) {
				report(n, "has control byte %#x at offset %d: only printable ASCII and the tab between fields", c, j)
				break
			}
		}
		if n == 1 {
			if line != traceHeader {
				report(n, "header is %q, want %q", line, traceHeader)
			}
			continue
		}
		if line == "" {
			report(n, "is blank")
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		rows++

		fields := strings.Split(line, "\t")
		if len(fields) != len(traceFields) {
			report(n, "has %d tab-separated fields, want %d", len(fields), len(traceFields))
			continue
		}
		for k, f := range fields {
			if f == "" {
				report(n, "field %s is empty", traceFields[k])
			}
		}
		spec, action, file, symbol := fields[0], fields[1], fields[2], fields[3]

		if spec != "" {
			if !traceGoName(spec) {
				report(n, "spec %q is not a module name", spec)
			} else if _, err := os.Stat(filepath.Join(root, "docs", "tla", spec+".tla")); err != nil {
				report(n, "spec %s has no module docs/tla/%s.tla: %v", spec, spec, err)
			}
		}

		if action != "" && !traceActionName(action) {
			report(n, "action %q does not match ^[A-Z][A-Za-z0-9]*$", action)
		}

		fileOK := false
		if file != "" {
			bad := tracePathProblems(file)
			for _, b := range bad {
				report(n, "file %q %s", file, b)
			}
			fileOK = len(bad) == 0
		}

		recv, name, symOK := traceSplitSymbol(symbol)
		if symbol != "" && !symOK {
			report(n, "symbol %q is neither Name nor Recv.Name", symbol)
		}

		if !fileOK {
			continue
		}
		p := traceParse(root, file, cache)
		if p.err != nil {
			report(n, "%v", p.err)
			continue
		}
		if !symOK {
			continue
		}
		found, others := traceFindFunc(p.file, recv, name)
		if !found {
			msg := fmt.Sprintf("%s declares no %s", file, traceDescribe(recv, name))
			if len(others) > 0 {
				msg += "; it declares " + strings.Join(others, ", ")
			}
			report(n, "%s", msg)
		}
	}
	if rows == 0 {
		problems = append(problems, "no rows: the trace has nothing but its header and comments")
	}
	return problems
}

// tracePathProblems says what is wrong with a trace row's file field: it
// must be a slash path relative to the repository's root, ending in .go,
// with no empty, "." or ".." element.
func tracePathProblems(file string) []string {
	var bad []string
	if strings.HasPrefix(file, "/") {
		bad = append(bad, "is absolute")
	}
	if !strings.HasSuffix(file, ".go") {
		bad = append(bad, "does not end in .go")
	}
	for _, elem := range strings.Split(file, "/") {
		switch elem {
		case "":
			bad = append(bad, "has an empty element")
		case ".", "..":
			bad = append(bad, fmt.Sprintf("has a %q element", elem))
		}
	}
	return bad
}

// traceSplitSymbol splits "Name" or "Recv.Name". recv is empty for a
// function. ok is false unless every part matches ^[A-Za-z_][A-Za-z0-9_]*$.
func traceSplitSymbol(symbol string) (recv, name string, ok bool) {
	parts := strings.Split(symbol, ".")
	switch len(parts) {
	case 1:
		return "", parts[0], traceGoName(parts[0])
	case 2:
		return parts[0], parts[1], traceGoName(parts[0]) && traceGoName(parts[1])
	}
	return "", "", false
}

// traceGoName reports whether s matches ^[A-Za-z_][A-Za-z0-9_]*$.
func traceGoName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && '0' <= c && c <= '9':
		default:
			return false
		}
	}
	return true
}

// traceActionName reports whether s matches ^[A-Z][A-Za-z0-9]*$.
func traceActionName(s string) bool {
	if s == "" || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// traceParse reads and parses root/file once per check.
func traceParse(root, file string, cache map[string]traceParsed) traceParsed {
	if p, ok := cache[file]; ok {
		return p
	}
	var p traceParsed
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		p.err = fmt.Errorf("cannot read %s: %v", file, err)
	} else if p.file, err = parser.ParseFile(token.NewFileSet(), file, src, parser.SkipObjectResolution); err != nil {
		p.err = fmt.Errorf("%s does not parse: %v", file, err)
	}
	cache[file] = p
	return p
}

// traceFindFunc reports whether f declares, at top level, the function name
// (recv empty: no receiver) or the method name on the type recv (with a
// pointer receiver or not, whatever its type parameters). others describes
// every other top-level function or method in f that is called name.
func traceFindFunc(f *ast.File, recv, name string) (found bool, others []string) {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name {
			continue
		}
		isMethod := fd.Recv != nil
		base := ""
		if isMethod {
			base = traceReceiverBase(fd.Recv)
		}
		if isMethod == (recv != "") && base == recv {
			return true, nil
		}
		if isMethod {
			others = append(others, "method "+base+"."+name)
		} else {
			others = append(others, "function "+name)
		}
	}
	return false, others
}

// traceReceiverBase names a receiver's base type: T for t T, t *T,
// t T[K] and t *T[K, V]. It is empty for anything else.
func traceReceiverBase(fl *ast.FieldList) string {
	if len(fl.List) != 1 {
		return ""
	}
	expr := fl.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// traceDescribe names a symbol for a problem report.
func traceDescribe(recv, name string) string {
	if recv == "" {
		return "function " + name + " (with no receiver)"
	}
	return "method " + name + " on type " + recv
}

// traceRepoRoot walks up from the working directory to the first directory
// that holds go.mod.
func traceRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod in any directory above the working directory")
		}
		dir = parent
	}
}

// TestTLATraceSymbolsExist is the tripwire itself (ADR 0013 section 6): every
// row of docs/tla/trace.tsv keeps the layout README.md "Tables" sets, names a
// module that exists, and names a Go symbol its file still declares. A
// missing trace.tsv, or one with no rows, fails; this test never skips.
func TestTLATraceSymbolsExist(t *testing.T) {
	root := traceRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "tla", "trace.tsv"))
	if err != nil {
		t.Fatalf("docs/tla/trace.tsv: %v", err)
	}
	for _, p := range checkTrace(root, string(data)) {
		t.Errorf("docs/tla/trace.tsv: %s", p)
	}
}

// traceFixtureSource is the Go file the checker's own test traces: a
// function, a method on a pointer receiver, and methods on generic types
// with one and with two type parameters.
const traceFixtureSource = `package p

func F() {}

type T struct{}

func (t *T) M() {}

type G[K comparable] struct{}

func (g G[K]) N() {}

type H[K comparable, V any] struct{}

func (h *H[K, V]) O() {}
`

// traceDoc joins lines into a trace.tsv, each ending in LF.
func traceDoc(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// traceRow joins fields with tabs.
func traceRow(fields ...string) string {
	return strings.Join(fields, "\t")
}

// writeTraceFixture writes content to root/rel, making its directories.
func writeTraceFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type traceCase struct {
	name string
	tsv  string
	line int
}

// TestTLATraceCheckerCatchesProblems shows that the tripwire's checker
// accepts a trace that keeps README.md "Tables" and names symbols that exist,
// and that it rejects, on the right line, each way a trace can break that
// layout or point at nothing (ADR 0013 section 6).
func TestTLATraceCheckerCatchesProblems(t *testing.T) {
	root := t.TempDir()
	writeTraceFixture(t, root, "docs/tla/M.tla", "---- MODULE M ----\n====\n")
	writeTraceFixture(t, root, "p/x.go", traceFixtureSource)
	writeTraceFixture(t, root, "p/x.txt", "not Go\n")
	writeTraceFixture(t, root, "p/bad.go", "package p\n\nfunc (\n")

	valid := []string{
		traceRow("M", "Init", "p/x.go", "F", "none", "-"),
		traceRow("M", "Step", "p/x.go", "T.M", "T.mu (write), then G.mu", "ADR 0013 section 6; README.md Tables"),
		traceRow("M", "Step2", "p/x.go", "G.N", "none: free text", "docs/DESIGN.md section 2"),
		traceRow("M", "StepThree", "p/x.go", "H.O", "-", "-"),
	}

	t.Run("valid", func(t *testing.T) {
		lines := []string{traceHeader, valid[0], "# a comment between rows", valid[1], valid[2], valid[3]}
		if problems := checkTrace(root, traceDoc(lines...)); len(problems) != 0 {
			t.Errorf("a valid trace has problems:\n%s", strings.Join(problems, "\n"))
		}
	})

	// Each bad row is line 3, after the header and a valid row, so every
	// problem the checker finds must be on line 3.
	bad := func(line string) string {
		return traceDoc(traceHeader, valid[0], line)
	}
	row := func(spec, action, file, symbol string) string {
		return traceRow(spec, action, file, symbol, "none", "-")
	}
	abs := filepath.ToSlash(filepath.Join(root, "p", "x.go"))

	cases := []traceCase{
		{"unknown function", bad(row("M", "Init", "p/x.go", "Nope")), 3},
		{"unknown method", bad(row("M", "Init", "p/x.go", "T.Nope")), 3},
		{"unknown receiver", bad(row("M", "Init", "p/x.go", "U.M")), 3},
		{"method on the wrong type", bad(row("M", "Init", "p/x.go", "G.M")), 3},
		{"function named as a method", bad(row("M", "Init", "p/x.go", "T.F")), 3},
		{"method named as a function", bad(row("M", "Init", "p/x.go", "M")), 3},
		{"generic method named as a function", bad(row("M", "Init", "p/x.go", "N")), 3},
		{"symbol with three parts", bad(row("M", "Init", "p/x.go", "p.T.M")), 3},
		{"symbol with an empty name", bad(row("M", "Init", "p/x.go", "T.")), 3},
		{"symbol starting with a digit", bad(row("M", "Init", "p/x.go", "1F")), 3},
		{"missing file", bad(row("M", "Init", "p/y.go", "F")), 3},
		{"file that does not parse", bad(row("M", "Init", "p/bad.go", "F")), 3},
		{"dot-dot path", bad(row("M", "Init", "p/../p/x.go", "F")), 3},
		{"dot path", bad(row("M", "Init", "./p/x.go", "F")), 3},
		{"empty path element", bad(row("M", "Init", "p//x.go", "F")), 3},
		{"absolute path", bad(row("M", "Init", abs, "F")), 3},
		{"non-Go file", bad(row("M", "Init", "p/x.txt", "F")), 3},
		{"five fields", bad(traceRow("M", "Init", "p/x.go", "F", "none")), 3},
		{"seven fields", bad(traceRow("M", "Init", "p/x.go", "F", "none", "-", "extra")), 3},
		{"empty locks field", bad(traceRow("M", "Init", "p/x.go", "F", "", "-")), 3},
		{"empty decided field", bad(traceRow("M", "Init", "p/x.go", "F", "none", "")), 3},
		{"empty symbol field", bad(traceRow("M", "Init", "p/x.go", "", "none", "-")), 3},
		{"unknown spec", bad(row("Z", "Init", "p/x.go", "F")), 3},
		{"spec that is a path", bad(row("../tla/M", "Init", "p/x.go", "F")), 3},
		{"lower-case action", bad(row("M", "init", "p/x.go", "F")), 3},
		{"action with an underscore", bad(row("M", "Init_2", "p/x.go", "F")), 3},
		{"carriage return", bad(valid[0] + "\r"), 3},
		{"non-ASCII byte", bad(traceRow("M", "Init", "p/x.go", "F", "n\xc3\xb6ne", "-")), 3},
		{"NUL byte", bad(traceRow("M", "Init", "p/x.go", "F", "no\x00ne", "-")), 3},
		{"form feed", bad(traceRow("M", "Init", "p/x.go", "F", "no\x0cne", "-")), 3},
		{"DEL byte", bad(traceRow("M", "Init", "p/x.go", "F", "no\x7fne", "-")), 3},
		{"blank line", bad(""), 3},
		{"last row without LF", strings.TrimSuffix(traceDoc(traceHeader, valid[0], valid[1]), "\n"), 3},
		{"last comment without LF", strings.TrimSuffix(traceDoc(traceHeader, valid[0], "# a comment"), "\n"), 3},
		{"wrong header", traceDoc("#spec\taction\tfile\tsymbol\tlocks", valid[0]), 1},
		{"header with spaces", traceDoc(strings.ReplaceAll(traceHeader, "\t", " "), valid[0]), 1},
		{"no header", traceDoc(valid[0], valid[1]), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := checkTrace(root, c.tsv)
			if len(problems) == 0 {
				t.Fatalf("no problem reported for %q", c.tsv)
			}
			prefix := fmt.Sprintf("line %d: ", c.line)
			for _, p := range problems {
				if !strings.HasPrefix(p, prefix) {
					t.Errorf("problem %q is not on line %d", p, c.line)
				}
			}
		})
	}

	// A trace with nothing but its header and comments has no rows, and
	// that is a problem in itself.
	for _, tsv := range []string{"", traceDoc(traceHeader), traceDoc(traceHeader, "# only a comment")} {
		if problems := checkTrace(root, tsv); len(problems) == 0 {
			t.Errorf("no problem reported for the row-less trace %q", tsv)
		}
	}
}
