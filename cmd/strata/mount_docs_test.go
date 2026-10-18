package main

// Clean-room tripwire for the mount commands strata documents and scripts,
// written from docs/adr/0015-every-mount-command-carries-nosuid.md (as
// revised): §1 (the rule), §2 (the options strata prints, which the documented
// commands mirror), §4 (the scripts) and §7 (the test surface, T2).
//
// go test runs a package's tests in that package's source directory, so the
// repository root is ../.. from here (ADR 0015 §7, T2).

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// t2MountText is the text that marks a line as a mount command (ADR 0015 §7,
// T2 (a)).
const t2MountText = "mount -t nfs"

// t2OptionFlag is the text that introduces a mount command's option list.
const t2OptionFlag = " -o "

// t2Required are the options every mount command must carry, each as an
// element of its comma-separated option list (ADR 0015 §1, §7 T2).
var t2Required = []string{"nosuid", "nodev"}

// readT2Lines reads the file at path and splits it into lines. A file that
// cannot be read is fatal, not a skip (ADR 0015 §7, T2).
func readT2Lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (ADR 0015 §7, T2)", path, err)
	}
	return strings.Split(string(data), "\n")
}

// checkT2Options reports an error for each of t2Required that is not an
// element of opts, split on ",". clause names the part of ADR 0015 §7 T2 being
// checked, for the message.
func checkT2Options(t *testing.T, path string, lineNo int, line, opts, clause string) {
	t.Helper()
	elems := strings.Split(opts, ",")
	for _, req := range t2Required {
		if !slices.Contains(elems, req) {
			t.Errorf("%s:%d: the mount command's options %q have no element equal to %q (ADR 0015 §1, §7 T2 %s): %s",
				path, lineNo, opts, req, clause, line)
		}
	}
}

// checkT2MountLines applies ADR 0015 §7, T2 (a), to the file at path: every
// line containing t2MountText has t2OptionFlag after that text, and the
// whitespace-delimited token after t2OptionFlag, split on ",", has an element
// equal to "nosuid" and an element equal to "nodev". It returns how many lines
// contained t2MountText. clause names the part of T2 being checked, for the
// message.
func checkT2MountLines(t *testing.T, path, clause string) int {
	t.Helper()
	found := 0
	for i, line := range readT2Lines(t, path) {
		at := strings.Index(line, t2MountText)
		if at < 0 {
			continue
		}
		found++
		after := line[at+len(t2MountText):]
		o := strings.Index(after, t2OptionFlag)
		if o < 0 {
			t.Errorf("%s:%d: the line contains %q but has no %q after it, so it gives no option list carrying nosuid and nodev (ADR 0015 §1, §7 T2 %s): %s",
				path, i+1, t2MountText, t2OptionFlag, clause, line)
			continue
		}
		opts := after[o+len(t2OptionFlag):]
		if end := strings.IndexFunc(opts, unicode.IsSpace); end >= 0 {
			opts = opts[:end]
		}
		checkT2Options(t, path, i+1, line, opts, clause)
	}
	return found
}

// checkT2MountShBase applies ADR 0015 §7, T2 (c), to scripts/mount.sh at path.
// The base options assignment is the line that, after leading whitespace,
// begins "opts=" and whose assigned value begins "vers=" (once an opening
// double quote is set aside). That excludes comment lines, and the
// opts="$opts,..." lines that append the lock options, whose value begins
// "$opts". On each such line, the value between the first pair of double
// quotes, split on ",", must have an element equal to "nosuid" and an element
// equal to "nodev". It returns how many base assignments it found.
func checkT2MountShBase(t *testing.T, path string) int {
	t.Helper()
	found := 0
	for i, line := range readT2Lines(t, path) {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		value, ok := strings.CutPrefix(trimmed, "opts=")
		if !ok {
			continue
		}
		if !strings.HasPrefix(strings.TrimPrefix(value, `"`), "vers=") {
			continue
		}
		found++
		open := strings.Index(line, `"`)
		if open < 0 {
			t.Errorf("%s:%d: the base options assignment has no double-quoted value (ADR 0015 §4, §7 T2 (c)): %s",
				path, i+1, line)
			continue
		}
		rest := line[open+1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			t.Errorf("%s:%d: the base options assignment has no closing double quote (ADR 0015 §4, §7 T2 (c)): %s",
				path, i+1, line)
			continue
		}
		checkT2Options(t, path, i+1, line, rest[:end], "(c)")
	}
	return found
}

// TestDocumentedMountCommandsCarryNosuid (T2) pins ADR 0015 §1: "Every mount
// command that strata prints, and every one that README.md, the Markdown files
// under docs/ other than docs/adr/, or scripts/ gives, carries nosuid and
// nodev." docs/adr/ is excluded because an ADR records history and may quote a
// command as it was.
//
// ADR 0015 §7, T2: (a) every line of README.md, and of every .md file under
// docs/ at any depth outside docs/adr/, that contains "mount -t nfs" has " -o "
// after that text, and the token after " -o ", split on ",", has an element
// equal to "nosuid" and an element equal to "nodev"; (b) README.md has at
// least one such line; (c) the scripts, checked on the line that assembles or
// runs the command rather than as a whole-file substring: in scripts/mount.sh
// the base opts= assignment whose value begins vers= (§4), and in
// scripts/demo.sh every line containing "mount -t nfs", as in (a), with at
// least one of each found.
func TestDocumentedMountCommandsCarryNosuid(t *testing.T) {
	root := filepath.Join("..", "..")

	// (a) and (b): README.md.
	readme := filepath.Join(root, "README.md")
	if n := checkT2MountLines(t, readme, "(a)"); n == 0 {
		t.Errorf("%s has no line containing %q, so the check of its mount commands would pass vacuously (ADR 0015 §7, T2 (b))",
			readme, t2MountText)
	}

	// (a): every .md file under docs/, at any depth, outside docs/adr/. A
	// file with no mount command contributes no checks.
	docs := filepath.Join(root, "docs")
	adr := filepath.Join(docs, "adr")
	var files []string
	err := filepath.WalkDir(docs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Clean(path) == adr {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v (ADR 0015 §7, T2 (a))", docs, err)
	}
	for _, path := range files {
		checkT2MountLines(t, path, "(a)")
	}

	// (c): scripts/mount.sh, on its base options assignment. A whole-file
	// substring check would pass on the comment line that names the options
	// even if they were dropped from the command.
	mountSh := filepath.Join(root, "scripts", "mount.sh")
	if n := checkT2MountShBase(t, mountSh); n == 0 {
		t.Errorf("%s has no base options assignment (a line beginning opts= whose value begins vers=), so its check would pass vacuously (ADR 0015 §4, §7 T2 (c))",
			mountSh)
	}

	// (c): scripts/demo.sh, every line containing "mount -t nfs", as in (a).
	demoSh := filepath.Join(root, "scripts", "demo.sh")
	if n := checkT2MountLines(t, demoSh, "(c)"); n == 0 {
		t.Errorf("%s has no line containing %q, so its check would pass vacuously (ADR 0015 §4, §7 T2 (c))",
			demoSh, t2MountText)
	}
}
