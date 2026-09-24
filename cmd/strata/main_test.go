package main

// Clean-room tests for cmd/strata's conversion of -max-dirty to
// Config.MaxDirtyBytes, written from docs/adr/0003-write-backpressure.md as
// revised through 2026-09-24: §1 (maxDirtyBytes and its overflow rule) and
// Assumptions 16 and 17.

import (
	"math"
	"strconv"
	"testing"
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
