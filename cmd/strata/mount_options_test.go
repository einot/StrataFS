package main

// Clean-room tests for cmd/strata's list of mount options, written from
// docs/adr/0015-every-mount-command-carries-nosuid.md: §1 (the rule), §2
// (mountOptions and its table) and §7 (the test surface, T1).

import (
	"slices"
	"strings"
	"testing"
)

// TestMountOptions (T1) pins ADR 0015 §2: mountOptions returns, in order,
// "vers=3", "tcp", "port="+port, "mountport="+port, "noresvport", "hard",
// "nosuid"; then "nolocks", "locallocks" if goos == "darwin", and "nolock" for
// any other goos, the empty string included; then "ro" if readOnly. "It
// returns a new slice on each call, does no I/O and keeps no state." There is
// one case for each row of §2's table, compared as an exact slice.
//
// ADR 0015 §7, T1: "In every row "nosuid" occurs exactly once and "suid" not
// at all, as elements. Changing a returned slice does not change the next
// call's result." §1: every mount command strata prints carries nosuid.
func TestMountOptions(t *testing.T) {
	type testCase struct {
		name     string
		goos     string
		port     string
		readOnly bool
		want     []string
	}
	cases := []testCase{
		{"darwin, 20490, read-write", "darwin", "20490", false, []string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nolocks", "locallocks"}},
		{"darwin, 20490, read-only", "darwin", "20490", true, []string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nolocks", "locallocks", "ro"}},
		{"linux, 20490, read-write", "linux", "20490", false, []string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nolock"}},
		{"linux, 12345, read-only", "linux", "12345", true, []string{"vers=3", "tcp", "port=12345", "mountport=12345", "noresvport", "hard", "nosuid", "nolock", "ro"}},
		{"freebsd, 2049, read-write: the Linux form", "freebsd", "2049", false, []string{"vers=3", "tcp", "port=2049", "mountport=2049", "noresvport", "hard", "nosuid", "nolock"}},
		{"empty goos, 20490, read-write: the Linux form", "", "20490", false, []string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nolock"}},
	}
	for _, tc := range cases {
		got := mountOptions(tc.goos, tc.port, tc.readOnly)
		if !slices.Equal(got, tc.want) {
			t.Errorf("row %q: mountOptions(%q, %q, %v) = %q (joined %q), want %q (joined %q) (ADR 0015 §2)",
				tc.name, tc.goos, tc.port, tc.readOnly, got, strings.Join(got, ","), tc.want, strings.Join(tc.want, ","))
		}

		nosuid := 0
		for _, opt := range got {
			if opt == "nosuid" {
				nosuid++
			}
			if opt == "suid" {
				t.Errorf("row %q: mountOptions(%q, %q, %v) = %q has the element \"suid\", want none (ADR 0015 §2, §7 T1)",
					tc.name, tc.goos, tc.port, tc.readOnly, got)
			}
		}
		if nosuid != 1 {
			t.Errorf("row %q: mountOptions(%q, %q, %v) = %q has the element \"nosuid\" %d times, want exactly once (ADR 0015 §2, §7 T1)",
				tc.name, tc.goos, tc.port, tc.readOnly, got, nosuid)
		}

		// Overwrite every element of the slice the first call returned. A
		// second call must still give the pinned result: mountOptions returns
		// a new slice on each call and keeps no state (§2).
		for i := range got {
			got[i] = "overwritten-by-test"
		}
		again := mountOptions(tc.goos, tc.port, tc.readOnly)
		if !slices.Equal(again, tc.want) {
			t.Errorf("row %q: after the test overwrote every element of the result of a first call, mountOptions(%q, %q, %v) = %q, want %q again: it must return a new slice on each call and keep no state (ADR 0015 §2, §7 T1)",
				tc.name, tc.goos, tc.port, tc.readOnly, again, tc.want)
		}
	}
}
