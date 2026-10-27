package main

// Clean-room tests for runCheck's handling of an endpoint without conditional
// writes, written without reading the implementation, from ADR 0021
// (docs/adr/0021-the-unconditional-root-write-needs-the-operators-opt-in.md)
// sections 2, 3 and 6 and Assumption 10, as revised on 2026-10-25: C1 to C3
// of section 6, and C4 and C1's last assertion, added on review.
//
// ADR 0021 section 2: runCheck(ctx, s, out, allowUnconditional). A
// conditional-write probe whose PutIfMatch answers store.ErrUnsupported, and
// every probe skipped after it, fails with required equal to
// !allowUnconditional, whichever of the three it is; every row keeps the name
// and detail it has at 247a276.
// Without the opt-in runCheck prints guidance that names
// -allow-unconditional-root after "It will not work correctly." and returns
// an error; with it the probes are optional, the summary is the existing
// caveat text, and it returns nil. An endpoint that honours conditional
// writes passes either way.
//
// Each case runs over its own *store.Local under t.TempDir(), since -check
// writes and deletes probe objects under strata-check/ (ADR 0021 section 6).
// The output is captured in a bytes.Buffer and asserted only by containment of
// the strings section 6 lists; the rest of it is not pinned.
//
// Not covered, on purpose: that run passes -allow-unconditional-root to
// runCheck and to blobfs.Config, the flag's help text, and that -check's
// severity turns on store.ErrUnsupported from any of the three probes, with
// the probes skipped after it, and on nothing else, which ADR 0021 section 6
// leaves to review.

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strata/internal/store"
)

// ck91Bound is how long a test waits for runCheck before it reports it as
// hung.
const ck91Bound = 30 * time.Second

// ck91FailRows are the three conditional-write probes' rows when they fail
// (ADR 0021 section 6, C1 and C2).
var ck91FailRows = []string{
	"[FAIL] conditional create (If-None-Match)",
	"[FAIL] conditional update (If-Match)",
	"[FAIL] stale If-Match is REJECTED",
}

// ck91NoCAS is ADR 0021 section 6's fake: a store.Store that passes every
// call through to a *store.Local and answers every PutIfMatch with
// store.ErrUnsupported without calling the inner store. It does not embed
// store.Store, so that every method it has is one written here.
type ck91NoCAS struct {
	inner *store.Local
}

var _ store.Store = (*ck91NoCAS)(nil)

func (s *ck91NoCAS) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *ck91NoCAS) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *ck91NoCAS) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	return s.inner.Head(ctx, key)
}

func (s *ck91NoCAS) Put(ctx context.Context, key string, data []byte) error {
	return s.inner.Put(ctx, key, data)
}

func (s *ck91NoCAS) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *ck91NoCAS) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

// PutIfMatch answers store.ErrUnsupported, as an S3 endpoint does when it
// answers a conditional PUT with HTTP 501, without calling the inner store.
func (s *ck91NoCAS) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	return "", store.ErrUnsupported
}

func (s *ck91NoCAS) Name() string {
	return s.inner.Name()
}

func (s *ck91NoCAS) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	return s.inner.GetWithETag(ctx, key)
}

// ck91CreateOnly is C4's fake (ADR 0021 section 6): a store.Store over a
// *store.Local that passes every call through, except that it answers every
// PutIfMatch whose etag is not "" with store.ErrUnsupported without calling
// the inner store, and passes one whose etag is "" through: an endpoint that
// honours If-None-Match but answers If-Match 501, the Swift shape of ADR
// 0021's Context. Every other method is ck91NoCAS's pass-through.
type ck91CreateOnly struct {
	ck91NoCAS
}

var _ store.Store = (*ck91CreateOnly)(nil)

func (s *ck91CreateOnly) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	if etag != "" {
		return "", store.ErrUnsupported
	}
	return s.inner.PutIfMatch(ctx, key, data, etag)
}

// ck91Local makes a fresh local bucket under the test's temporary directory.
func ck91Local(t *testing.T) *store.Local {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	return local
}

// ck91Run runs runCheck(ctx, s, &out, allow) in its own goroutine, which
// recovers a panic, waits for it under ck91Bound, and returns what it wrote
// and its error. A hang or a panic fails the test.
func ck91Run(t *testing.T, s store.Store, allow bool) (string, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var out bytes.Buffer
	done := make(chan struct{})
	var err error
	var panicked bool
	var pval any
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				pval = r
			}
		}()
		err = runCheck(ctx, s, &out, allow)
	}()

	what := fmt.Sprintf("runCheck with allowUnconditional %v", allow)
	select {
	case <-done:
	case <-time.After(ck91Bound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, ck91Bound)
	}
	if panicked {
		t.Fatalf("%s panicked with %v", what, pval)
	}
	return out.String(), err
}

// ck91Line returns the first line of out that contains marker, and whether
// there is one.
func ck91Line(out, marker string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, marker) {
			return line, true
		}
	}
	return "", false
}

// ck91LineHolds requires the first line of out that contains marker to
// contain every string in want too.
func ck91LineHolds(t *testing.T, out, marker string, want []string, what, where string) {
	t.Helper()
	line, ok := ck91Line(out, marker)
	if !ok {
		t.Errorf("%s: the report has no line containing %q (%s). The report:\n%s", what, marker, where, out)
		return
	}
	for _, w := range want {
		if !strings.Contains(line, w) {
			t.Errorf("%s: the line %q does not contain %q: the row keeps the detail it has at 247a276 (%s). "+
				"The report:\n%s", what, line, w, where, out)
		}
	}
}

// ck91Contains requires out to contain every string in want.
func ck91Contains(t *testing.T, out string, want []string, what, where string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s: the report does not contain %q (%s). The report:\n%s", what, w, where, out)
		}
	}
}

// TestCheckRefusesAnEndpointWithoutConditionalWrites is C1 of ADR 0021
// section 6: on an endpoint that answers every conditional write
// store.ErrUnsupported, runCheck without the opt-in reports the three
// conditional-write probes as failed and required, says the endpoint will not
// work, names -allow-unconditional-root, and returns an error (ADR 0021
// section 2; section 3, the first case).
func TestCheckRefusesAnEndpointWithoutConditionalWrites(t *testing.T) {
	out, err := ck91Run(t, &ck91NoCAS{inner: ck91Local(t)}, false)
	what := "runCheck(ctx, fake, &out, false) on an endpoint without conditional writes"
	if err == nil {
		t.Errorf("%s = nil, want an error: without the opt-in the conditional-write probes are required, and "+
			"a required failure makes runCheck return an error (ADR 0021 section 2; section 6, C1). The "+
			"report:\n%s", what, out)
	}
	want := append(append([]string(nil), ck91FailRows...), "It will not work correctly.", "-allow-unconditional-root")
	ck91Contains(t, out, want, what, "ADR 0021 section 2; section 6, C1")
	for _, row := range ck91FailRows {
		ck91LineHolds(t, out, row, []string{"not supported by this endpoint"}, what,
			"ADR 0021 section 2, Assumption 10; section 6, C1, added on review")
	}
}

// TestCheckAllowsAnEndpointWithoutConditionalWritesWithTheOptIn is C2 of ADR
// 0021 section 6: on the same endpoint, runCheck with the opt-in reports the
// three conditional-write probes as failed but optional, prints the existing
// caveat text, and returns nil (ADR 0021 section 2; section 3, the second
// case).
func TestCheckAllowsAnEndpointWithoutConditionalWritesWithTheOptIn(t *testing.T) {
	out, err := ck91Run(t, &ck91NoCAS{inner: ck91Local(t)}, true)
	what := "runCheck(ctx, fake, &out, true) on an endpoint without conditional writes"
	if err != nil {
		t.Errorf("%s = %v, want nil: with the opt-in the conditional-write probes are optional, and the "+
			"summary is the caveat text with a nil return (ADR 0021 section 2; section 6, C2). The report:\n%s",
			what, err, out)
	}
	want := append(append([]string(nil), ck91FailRows...), "with the caveats noted above", "Run exactly one mount per bucket.")
	ck91Contains(t, out, want, what, "ADR 0021 section 2; section 6, C2")
}

// TestCheckPassesAnEndpointWithConditionalWritesEitherWay is C3 of ADR 0021
// section 6: on a plain *store.Local, whose PutIfMatch honours its condition
// and never answers store.ErrUnsupported, runCheck passes with the opt-in and
// without it: it returns nil, says the endpoint supports everything strata
// needs, and reports no failed probe (ADR 0021 section 2; section 3, a local
// directory).
func TestCheckPassesAnEndpointWithConditionalWritesEitherWay(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowUnconditional=%v", allow), func(t *testing.T) {
			out, err := ck91Run(t, ck91Local(t), allow)
			what := fmt.Sprintf("runCheck(ctx, local, &out, %v) on a plain *store.Local", allow)
			if err != nil {
				t.Errorf("%s = %v, want nil: the endpoint honours conditional writes (ADR 0021 section 2; "+
					"section 6, C3). The report:\n%s", what, err, out)
			}
			ck91Contains(t, out, []string{"This endpoint supports everything strata needs."}, what,
				"ADR 0021 section 2; section 6, C3")
			if strings.Contains(out, "[FAIL]") {
				t.Errorf("%s: the report contains %q, want no failed probe (ADR 0021 section 6, C3). The "+
					"report:\n%s", what, "[FAIL]", out)
			}
		})
	}
}

// TestCheckRefusesAnEndpointHonouringOnlyIfNoneMatch is C4 of ADR 0021
// section 6, added on review: -check's severity turns on a 501 from any
// conditional-write probe, not only the first, and so does every probe
// skipped after it (ADR 0021 section 2; Assumption 10). On an endpoint that
// honours If-None-Match but answers If-Match 501, the create probe passes,
// the update probe fails with today's detail, and the stale probe fails as
// skipped. Without the opt-in runCheck says the endpoint will not work,
// names -allow-unconditional-root, and returns an error, as the mount there
// creates the filesystem and then commits nothing (section 3, the second
// case); with it runCheck prints the caveat text and returns nil. Each call
// runs over its own *store.Local.
//
// At 53abeb7 the rows read the same, and the first call returns nil with
// "with the caveats noted above" in its output.
func TestCheckRefusesAnEndpointHonouringOnlyIfNoneMatch(t *testing.T) {
	const (
		okCreate  = "[ok  ] conditional create (If-None-Match)"
		failUpd   = "[FAIL] conditional update (If-Match)"
		failStale = "[FAIL] stale If-Match is REJECTED"
	)
	checkRows := func(t *testing.T, out, what, where string) {
		t.Helper()
		ck91Contains(t, out, []string{okCreate}, what, where)
		ck91LineHolds(t, out, failUpd, []string{"failed with a correct ETag", "not supported"}, what, where)
		ck91LineHolds(t, out, failStale, []string{"skipped"}, what, where)
	}

	t.Run("allowUnconditional=false", func(t *testing.T) {
		out, err := ck91Run(t, &ck91CreateOnly{ck91NoCAS{inner: ck91Local(t)}}, false)
		what := "runCheck(ctx, fake, &out, false) on an endpoint that honours only If-None-Match"
		where := "ADR 0021 section 2, Assumption 10; section 6, C4"
		if err == nil {
			t.Errorf("%s = nil, want an error: without the opt-in a probe answered store.ErrUnsupported is "+
				"required, whichever of the three it is, and so is every probe skipped after it (%s). The "+
				"report:\n%s", what, where, out)
		}
		checkRows(t, out, what, where)
		ck91Contains(t, out, []string{"It will not work correctly.", "-allow-unconditional-root"}, what, where)
	})

	t.Run("allowUnconditional=true", func(t *testing.T) {
		out, err := ck91Run(t, &ck91CreateOnly{ck91NoCAS{inner: ck91Local(t)}}, true)
		what := "runCheck(ctx, fake, &out, true) on an endpoint that honours only If-None-Match"
		where := "ADR 0021 section 2; section 6, C4"
		if err != nil {
			t.Errorf("%s = %v, want nil: with the opt-in the conditional-write probes are optional (%s). The "+
				"report:\n%s", what, err, where, out)
		}
		checkRows(t, out, what, where)
		ck91Contains(t, out, []string{"with the caveats noted above", "Run exactly one mount per bucket."}, what,
			where)
	})
}
