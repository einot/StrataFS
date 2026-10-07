package main

// Clean-room tests for cmd/strata's conversion of -max-dirty to
// Config.MaxDirtyBytes, written from docs/adr/0003-write-backpressure.md as
// revised through 2026-09-24: §1 (maxDirtyBytes and its overflow rule) and
// Assumptions 16 and 17.
//
// This file also holds clean-room tests of cmd/strata's conversion of
// -chunk-size to Config.ChunkSize and of -cache to Config.CacheBytes, written
// from docs/adr/0008-no-size-flag-wraps.md: §2 (chunkSizeBytes), §3
// (cacheBytes) and §6 (the test surface).
//
// It also holds clean-room tests of cmd/strata's check of -commit-interval,
// written from docs/adr/0009-a-negative-commit-interval-is-refused.md: §3
// (commitInterval) and §5 (the test surface).
//
// It also holds clean-room tests of cmd/strata's resolution of -uid and -gid,
// written from docs/adr/0010-a-new-filesystem-is-never-owned-by-root-by-accident.md:
// §3 (ownerID), §4 (currentIDs) and §7 (the test surface).

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMaxDirtyBytesConversion (M1) pins ADR 0003 §1: "maxDirtyBytes returns -1
// for mib <= 0; -1 for int64(mib) > math.MaxInt64>>20, a limit too large to
// express in bytes and so one no workload could reach; and int64(mib) << 20
// otherwise." Assumption 16: a -max-dirty above math.MaxInt64>>20 MiB "maps to
// -1, no limit, rather than wrapping, as the first version's shift did, or
// clamping to math.MaxInt64". Assumption 17: "-max-dirty 0 means no limit".
//
// The cases past 32 bits are built at run time from int64 values, never from
// untyped constants, so that this file still compiles where int is 32 bits.
func TestMaxDirtyBytesConversion(t *testing.T) {
	type testCase struct {
		mib  int
		want int64
		why  string
	}
	cases := []testCase{
		{256, 256 << 20, "the default, 256 MiB, in bytes"},
		{1, 1 << 20, "1 MiB in bytes"},
		{0, -1, "-max-dirty 0 means no limit (Assumption 17)"},
		{-5, -1, "a negative -max-dirty means no limit"},
		{math.MinInt, -1, "the most negative int means no limit"},
	}
	if strconv.IntSize == 64 {
		var top int64 = math.MaxInt64 >> 20
		var maxInt64 int64 = math.MaxInt64
		cases = append(cases,
			testCase{int(top), top << 20, "the largest MiB count expressible in bytes"},
			testCase{int(top + 1), -1, "one MiB more is too large to express in bytes, and maps to no limit rather than wrapping (Assumption 16)"},
			testCase{int(maxInt64), -1, "math.MaxInt is too large to express in bytes, and maps to no limit rather than wrapping (Assumption 16)"},
		)
	} else {
		t.Logf("int is %d bits, so the cases past 32 bits are not run", strconv.IntSize)
	}
	for _, tc := range cases {
		if got := maxDirtyBytes(tc.mib); got != tc.want {
			t.Errorf("maxDirtyBytes(%d) = %d, want %d: %s (ADR 0003 §1)", tc.mib, got, tc.want, tc.why)
		}
	}
}

// TestChunkSizeBytesConversion pins ADR 0008 §2: "For `0 <= kib <=
// math.MaxUint32>>10`, which is 4194303, or 2^22 − 1, it returns
// `uint32(kib) * 1024` and a nil error." "For `kib < 0`, or `kib > 4194303`,
// it returns 0 and a non-nil error." "The error's text contains `-chunk-size`
// and the value as given, in decimal as `strconv.Itoa` writes it." ADR 0008 §6:
// a test may rely on "every row of §2's and §3's tables" and, "for
// `chunkSizeBytes`, that the error is non-nil exactly where §2 refuses, that
// the `uint32` is then 0, and that the error's text contains `-chunk-size` and
// `strconv.Itoa(kib)`".
//
// A refused case whose error comes back nil is reported and skipped, so that an
// implementation that does not refuse yields a list of failures rather than a
// panic. The cases past 32 bits are built at run time from int64 values, never
// from untyped constants, so that this file still compiles where int is 32
// bits.
func TestChunkSizeBytesConversion(t *testing.T) {
	type testCase struct {
		kib     int
		want    uint32
		refused bool
		why     string
	}
	cases := []testCase{
		{0, 0, false, "-chunk-size 0 is 0, which New takes as the default, 1 MiB (row 0)"},
		{1, 1024, false, "1 KiB converts exactly, and is left to New to refuse (row 1 to 3)"},
		{3, 3072, false, "3 KiB converts exactly, and is left to New to refuse (row 1 to 3)"},
		{4, 4096, false, "4 KiB converts exactly (row 4)"},
		{1024, 1048576, false, "the default, 1024 KiB, converts exactly (row 1024)"},
		{4194303, 4294966272, false, "4194303 KiB, 2^22 − 1, is the largest a uint32 holds in bytes (row 4194303)"},
		{-1, 0, true, "a negative -chunk-size is refused, not wrapped to 4 GiB less 1 KiB (row −1)"},
		{math.MinInt, 0, true, "the most negative int is refused (row math.MinInt)"},
		{math.MaxInt, 0, true, "the largest int is refused, not wrapped (row math.MaxInt)"},
		{4194304, 0, true, "4194304 KiB is 2^32 bytes, one more than a uint32 holds, and is refused (row 4194304)"},
		{4194308, 0, true, "4194308 KiB is refused, not wrapped to 4 KiB chunks (row 4194308)"},
	}
	if strconv.IntSize == 64 {
		var two32 int64 = 1 << 32
		cases = append(cases,
			testCase{int(two32), 0, true, "2^32 KiB is refused, not wrapped to the default (row 2^32)"},
			testCase{int(two32 + 4), 0, true, "2^32 + 4 KiB is refused, not wrapped to 4 KiB chunks (row 2^32 + 4)"},
			testCase{int(two32 + 1024), 0, true, "2^32 + 1024 KiB is refused, not wrapped to 1 MiB chunks (row 2^32 + 1024)"},
		)
	} else {
		t.Logf("int is %d bits, so the cases past 32 bits are not run", strconv.IntSize)
	}
	for _, tc := range cases {
		got, err := chunkSizeBytes(tc.kib)
		if !tc.refused {
			if err != nil {
				t.Errorf("chunkSizeBytes(%d) = %d, error %q; want %d and a nil error: %s (ADR 0008 §2)", tc.kib, got, err.Error(), tc.want, tc.why)
			} else if got != tc.want {
				t.Errorf("chunkSizeBytes(%d) = %d, want %d: %s (ADR 0008 §2)", tc.kib, got, tc.want, tc.why)
			}
			continue
		}
		if err == nil {
			t.Errorf("chunkSizeBytes(%d) = %d and a nil error, want 0 and a non-nil error: %s (ADR 0008 §2)", tc.kib, got, tc.why)
			continue
		}
		if got != 0 {
			t.Errorf("chunkSizeBytes(%d) = %d with error %q, want 0 with the error: %s (ADR 0008 §2, §6)", tc.kib, got, err.Error(), tc.why)
		}
		msg := err.Error()
		if !strings.Contains(msg, "-chunk-size") {
			t.Errorf("chunkSizeBytes(%d) error %q, want it to contain %q: %s (ADR 0008 §2, §6)", tc.kib, msg, "-chunk-size", tc.why)
		}
		if given := strconv.Itoa(tc.kib); !strings.Contains(msg, given) {
			t.Errorf("chunkSizeBytes(%d) error %q, want it to contain the value as given, %q: %s (ADR 0008 §2, §6)", tc.kib, msg, given, tc.why)
		}
	}
}

// TestCommitIntervalFlag pins ADR 0009 §3: "For `d >= 0` it returns `d` and a
// nil error. Zero passes through for `New` to take as the default." "For `d <
// 0` it returns 0 and a non-nil error. The error's text contains
// `-commit-interval` and `d.String()`." There is one case for each row of §3's
// table. ADR 0009 §5: a test may rely on "every row of §3's table" and "that
// the error is non-nil exactly where §3 refuses, that the `time.Duration` is
// then 0, and that the error's text contains `-commit-interval` and
// `d.String()`".
//
// A refused case whose error comes back nil is reported and skipped, so that an
// implementation that does not refuse yields a list of failures rather than a
// panic. A time.Duration is an int64 on every platform, so no case depends on
// the width of int (§5).
func TestCommitIntervalFlag(t *testing.T) {
	type testCase struct {
		d       time.Duration
		refused bool
		why     string
	}
	cases := []testCase{
		{time.Duration(math.MinInt64), true, "the most negative duration is refused (row math.MinInt64)"},
		{-5 * time.Second, true, "a negative interval is refused, not clamped or taken as the default (row −5 s)"},
		{-time.Second, true, "a negative interval is refused (row −1 s)"},
		{-time.Nanosecond, true, "the negative interval nearest zero is refused (row −1 ns)"},
		{0, false, "0 passes through, for New to take as the default, 5 s (row 0)"},
		{time.Nanosecond, false, "the smallest positive interval is used as given, with no floor (row 1 ns)"},
		{5 * time.Second, false, "the default, 5 s, is used as given (row 5 s)"},
		{time.Hour, false, "1 h is used as given (row 1 h)"},
		{time.Duration(math.MaxInt64), false, "the largest duration is used as given, with no ceiling (row math.MaxInt64)"},
	}
	for _, tc := range cases {
		got, err := commitInterval(tc.d)
		if !tc.refused {
			if err != nil {
				t.Errorf("commitInterval(%v) = %v, error %q; want %v and a nil error: %s (ADR 0009 §3)", tc.d, got, err.Error(), tc.d, tc.why)
			} else if got != tc.d {
				t.Errorf("commitInterval(%v) = %v, want %v: %s (ADR 0009 §3)", tc.d, got, tc.d, tc.why)
			}
			continue
		}
		if err == nil {
			t.Errorf("commitInterval(%v) = %v and a nil error, want 0 and a non-nil error: %s (ADR 0009 §3)", tc.d, got, tc.why)
			continue
		}
		if got != 0 {
			t.Errorf("commitInterval(%v) = %v with error %q, want 0 with the error: %s (ADR 0009 §3, §5)", tc.d, got, err.Error(), tc.why)
		}
		msg := err.Error()
		if !strings.Contains(msg, "-commit-interval") {
			t.Errorf("commitInterval(%v) error %q, want it to contain %q: %s (ADR 0009 §3, §5)", tc.d, msg, "-commit-interval", tc.why)
		}
		if given := tc.d.String(); !strings.Contains(msg, given) {
			t.Errorf("commitInterval(%v) error %q, want it to contain the value as time.Duration's String method writes it, %q: %s (ADR 0009 §3, §5)", tc.d, msg, given, tc.why)
		}
	}
}

// TestCacheBytesConversion pins ADR 0008 §3: "For `mib <= 0` it returns 0,
// which selects the default, 256 MiB." "For `int64(mib) >
// math.MaxInt64>>20`, which is 8796093022207, or 2^43 − 1, it returns
// `math.MaxInt64`." "Otherwise it returns `int64(mib) << 20`." ADR 0008 §6: a
// test may rely on "every row of §2's and §3's tables".
//
// The cases past 32 bits are built at run time from int64 values, never from
// untyped constants, so that this file still compiles where int is 32 bits.
func TestCacheBytesConversion(t *testing.T) {
	type testCase struct {
		mib  int
		want int64
		why  string
	}
	cases := []testCase{
		{256, 256 << 20, "the default, 256 MiB, in bytes (row 256)"},
		{1, 1 << 20, "1 MiB in bytes (row 1)"},
		{0, 0, "-cache 0 is 0, which selects the default (row 0)"},
		{-1, 0, "a negative -cache is 0, the default, not a negative byte count (row −1)"},
		{-5, 0, "a negative -cache is 0, the default"},
		{math.MinInt, 0, "the most negative int is 0, the default (row math.MinInt)"},
	}
	if strconv.IntSize == 64 {
		var two43 int64 = 1 << 43
		var two44 int64 = 1 << 44
		var maxInt64 int64 = math.MaxInt64
		var largestBound int64 = math.MaxInt64 - (1<<20 - 1) // 2^63 − 2^20
		cases = append(cases,
			testCase{int(two43 - 1), largestBound, "2^43 − 1 MiB, the largest whole number of MiB, converts exactly to 2^63 − 2^20 bytes (row 2^43 − 1)"},
			testCase{int(two43), maxInt64, "2^43 MiB cannot be expressed in bytes, and is math.MaxInt64 rather than wrapping (row 2^43)"},
			testCase{int(two44 + 1), maxInt64, "2^44 + 1 MiB is math.MaxInt64 rather than wrapping to 1 MiB (row 2^44 + 1)"},
			testCase{int(maxInt64), maxInt64, "math.MaxInt MiB is math.MaxInt64 rather than wrapping (row math.MaxInt)"},
			testCase{int(-two43 - 1), 0, "−2^43 − 1 MiB is 0, the default, rather than wrapping to an effectively unbounded cache (row −2^43 − 1)"},
			testCase{int(-two44 + 1), 0, "−2^44 + 1 MiB is 0, the default, rather than wrapping to 1 MiB (row −2^44 + 1)"},
		)
	} else {
		t.Logf("int is %d bits, so the cases past 32 bits are not run", strconv.IntSize)
	}
	for _, tc := range cases {
		if got := cacheBytes(tc.mib); got != tc.want {
			t.Errorf("cacheBytes(%d) = %d, want %d: %s (ADR 0008 §3)", tc.mib, got, tc.want, tc.why)
		}
	}
}

// TestOwnerIDConversion pins ADR 0010 §3: "For `0 <= given <= 4294967294`,
// which is `math.MaxUint32 - 1`, it returns `uint32(given)` and a nil error.
// It does not use `current`." "For `given == -1` it returns `current` and a nil
// error, unless `current` is 4294967295, when it returns 0 and a non-nil
// error." "For `given < -1`, or `given > 4294967294`, it returns 0 and a
// non-nil error." "The error's text contains `flagName`, and `given` in
// decimal as `strconv.Itoa` writes it." There is one case for each row of §3's
// table. ADR 0010 §7: a test may rely on "every row of §3's table, for
// `flagName` `"-uid"` and `"-gid"` alike" and "that the error is non-nil
// exactly where §3 refuses, that the `uint32` is then 0, and that the error's
// text contains `flagName` and `strconv.Itoa(given)`".
//
// A refused case whose error comes back nil is reported and skipped, so that an
// implementation that does not refuse yields a list of failures rather than a
// panic. The cases past 2^31 − 1 are built at run time from int64 values, never
// from untyped constants, so that this file still compiles where int is 32
// bits (§7).
func TestOwnerIDConversion(t *testing.T) {
	type testCase struct {
		given   int
		current uint32
		want    uint32
		refused bool
		why     string
	}
	cases := []testCase{
		{math.MinInt, 501, 0, true, "the most negative int is refused, not taken as the current id (row math.MinInt, 501)"},
		{-2, 501, 0, true, "a negative value other than -1 is refused, not taken as the current id (row −2, 501)"},
		{-2, 4294967295, 0, true, "a negative value other than -1 is refused, whatever the current id (row −2, 4294967295)"},
		{-1, 501, 501, false, "the default, -1, is the current id (row −1, 501)"},
		{-1, 0, 0, false, "the default, -1, is root when strata runs as root (row −1, 0)"},
		{-1, 4294967294, 4294967294, false, "the default, -1, is the current id, up to 4294967294 (row −1, 4294967294)"},
		{-1, 4294967295, 0, true, "the default, -1, is refused when the platform reports no id (row −1, 4294967295)"},
		{0, 501, 0, false, "0, root, is accepted when asked for (row 0, 501)"},
		{0, 4294967295, 0, false, "0 is accepted without using the current id (row 0, 4294967295)"},
		{1, 501, 1, false, "1 is used as given (row 1, 501)"},
		{501, 4294967295, 501, false, "a given id needs no current one (row 501, 4294967295)"},
		{65534, 501, 65534, false, "65534 is used as given (row 65534, 501)"},
		{2147483647, 501, 2147483647, false, "2^31 − 1 is accepted everywhere (row 2147483647, 501)"},
	}
	if strconv.IntSize == 64 {
		var two32 int64 = 1 << 32
		cases = append(cases,
			testCase{int(two32 - 2), 501, 4294967294, false, "2^32 − 2 is the largest id accepted (row 4294967294, 501)"},
			testCase{int(two32 - 1), 501, 0, true, "2^32 − 1 is (uid_t)-1, which no Linux client accepts, and is refused (row 4294967295, 501)"},
			testCase{int(two32), 501, 0, true, "2^32 is refused, not wrapped to root (row 4294967296, 501)"},
			testCase{int(two32 + 1), 501, 0, true, "2^32 + 1 is refused, not wrapped to 1 (row 4294967297, 501)"},
			testCase{int(two32 + 501), 501, 0, true, "2^32 + 501 is refused, not wrapped to 501 by accident (row 4294967797, 501)"},
			testCase{math.MaxInt, 501, 0, true, "math.MaxInt is refused, not wrapped to 4294967295 (row math.MaxInt, 501)"},
		)
	} else {
		t.Logf("int is %d bits, so the cases past 2^31 − 1 are not run", strconv.IntSize)
	}
	for _, flagName := range []string{"-uid", "-gid"} {
		for _, tc := range cases {
			got, err := ownerID(flagName, tc.given, tc.current)
			if !tc.refused {
				if err != nil {
					t.Errorf("ownerID(%q, %d, %d) = %d, error %q; want %d and a nil error: %s (ADR 0010 §3)", flagName, tc.given, tc.current, got, err.Error(), tc.want, tc.why)
				} else if got != tc.want {
					t.Errorf("ownerID(%q, %d, %d) = %d, want %d: %s (ADR 0010 §3)", flagName, tc.given, tc.current, got, tc.want, tc.why)
				}
				continue
			}
			if err == nil {
				t.Errorf("ownerID(%q, %d, %d) = %d and a nil error, want 0 and a non-nil error: %s (ADR 0010 §3)", flagName, tc.given, tc.current, got, tc.why)
				continue
			}
			if got != 0 {
				t.Errorf("ownerID(%q, %d, %d) = %d with error %q, want 0 with the error: %s (ADR 0010 §3, §7)", flagName, tc.given, tc.current, got, err.Error(), tc.why)
			}
			msg := err.Error()
			if !strings.Contains(msg, flagName) {
				t.Errorf("ownerID(%q, %d, %d) error %q, want it to contain %q: %s (ADR 0010 §3, §7)", flagName, tc.given, tc.current, msg, flagName, tc.why)
			}
			if given := strconv.Itoa(tc.given); !strings.Contains(msg, given) {
				t.Errorf("ownerID(%q, %d, %d) error %q, want it to contain the value as given, %q: %s (ADR 0010 §3, §7)", flagName, tc.given, tc.current, msg, given, tc.why)
			}
		}
	}
}

// TestCurrentIDsAreTheProcessIDs pins ADR 0010 §4: "It returns
// `uint32(os.Getuid())` and `uint32(os.Getgid())`, the real uid and gid of the
// process." ADR 0010 §7: a test may rely on "that `currentIDs` returns
// `uint32(os.Getuid())` and `uint32(os.Getgid())` on whatever host runs the
// test".
func TestCurrentIDsAreTheProcessIDs(t *testing.T) {
	uid, gid := currentIDs()
	if want := uint32(os.Getuid()); uid != want {
		t.Errorf("currentIDs() uid = %d, want uint32(os.Getuid()) = %d (ADR 0010 §4, §7)", uid, want)
	}
	if want := uint32(os.Getgid()); gid != want {
		t.Errorf("currentIDs() gid = %d, want uint32(os.Getgid()) = %d (ADR 0010 §4, §7)", gid, want)
	}
}
