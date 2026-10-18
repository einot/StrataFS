package main

// Clean-room tripwire for the mount commands strata documents and scripts,
// written from docs/adr/0015-every-mount-command-carries-nosuid.md: §1 (the
// rule), §2 (the options strata prints, which the documented commands mirror)
// and §7 (the test surface, T2).
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

// checkT2MountLines applies ADR 0015 §7, T2 (a), to the file at path: every
// line containing t2MountText has t2OptionFlag after that text, and the
// whitespace-delimited token after t2OptionFlag, split on ",", has an element
// equal to "nosuid". It returns how many lines contained t2MountText. A file
// that cannot be read is fatal, not a skip.
func checkT2MountLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (ADR 0015 §7, T2)", path, err)
	}
	found := 0
	for i, line := range strings.Split(string(data), "\n") {
		at := strings.Index(line, t2MountText)
		if at < 0 {
			continue
		}
		found++
		after := line[at+len(t2MountText):]
		o := strings.Index(after, t2OptionFlag)
		if o < 0 {
			t.Errorf("%s:%d: the line contains %q but has no %q after it, so it gives no option list carrying nosuid (ADR 0015 §1, §7 T2 (a)): %s",
				path, i+1, t2MountText, t2OptionFlag, line)
			continue
		}
		opts := after[o+len(t2OptionFlag):]
		if end := strings.IndexFunc(opts, unicode.IsSpace); end >= 0 {
			opts = opts[:end]
		}
		if !slices.Contains(strings.Split(opts, ","), "nosuid") {
			t.Errorf("%s:%d: the mount command's options %q have no element equal to \"nosuid\" (ADR 0015 §1, §7 T2 (a)): %s",
				path, i+1, opts, line)
		}
	}
	return found
}

// TestDocumentedMountCommandsCarryNosuid (T2) pins ADR 0015 §1: "Every mount
// command that strata prints, and every one that README.md, the Markdown files
// under docs/ other than docs/adr/, or scripts/ gives, carries nosuid."
// docs/adr/ is excluded because an ADR records history and may quote a command
// as it was.
//
// ADR 0015 §7, T2: (a) every line of README.md, and of every .md file under
// docs/ at any depth outside docs/adr/, that contains "mount -t nfs" has " -o "
// after that text, and the token after " -o ", split on ",", has an element
// equal to "nosuid"; (b) README.md has at least one such line; (c)
// scripts/demo.sh and scripts/mount.sh each contain "nosuid".
func TestDocumentedMountCommandsCarryNosuid(t *testing.T) {
	root := filepath.Join("..", "..")

	// (a) and (b): README.md.
	readme := filepath.Join(root, "README.md")
	if n := checkT2MountLines(t, readme); n == 0 {
		t.Errorf("%s has no line containing %q, so the check of its mount commands would pass vacuously (ADR 0015 §7, T2 (b))",
			readme, t2MountText)
	}

	// (a): every .md file under docs/, at any depth, outside docs/adr/.
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
		checkT2MountLines(t, path)
	}

	// (c): the scripts. This is a substring check only, because mount.sh
	// assembles its options in a variable (ADR 0015 §7, T2 (c)). It shows that
	// the option is present in each script, not that every command in it
	// carries the option; that is left to review.
	for _, path := range []string{
		filepath.Join(root, "scripts", "demo.sh"),
		filepath.Join(root, "scripts", "mount.sh"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v (ADR 0015 §7, T2 (c))", path, err)
		}
		if !strings.Contains(string(data), "nosuid") {
			t.Errorf("%s does not contain \"nosuid\" (ADR 0015 §1, §4, §7 T2 (c))", path)
		}
	}
}
