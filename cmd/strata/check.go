package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"strata/internal/store"
)

// runCheck probes an endpoint for the behaviour strata depends on and prints a
// report. It exists because "S3-compatible" is a spectrum: on-premise and
// third-party implementations vary most in exactly the places this filesystem
// is most sensitive to, and reading vendor documentation is a poor substitute
// for asking the cluster in front of you.
//
// Everything it writes is removed before it returns.
func runCheck(ctx context.Context, s store.Store, out io.Writer) error {
	prefix := fmt.Sprintf("strata-check/%d/", time.Now().UnixNano())
	var results []result
	defer cleanup(ctx, s, prefix)

	fmt.Fprintf(out, "probing %s\n\n", s.Name())

	results = append(results,
		checkRoundTrip(ctx, s, prefix),
		checkAwkwardKeys(ctx, s, prefix),
		checkRange(ctx, s, prefix),
		checkHeadETag(ctx, s, prefix),
		checkMissingIsNotFound(ctx, s, prefix),
		checkList(ctx, s, prefix),
		checkDelete(ctx, s, prefix),
	)
	cond := checkConditionalWrites(ctx, s, prefix)
	results = append(results, cond...)

	var failed, required int
	for _, r := range results {
		mark := "ok  "
		if !r.ok {
			mark = "FAIL"
			failed++
			if r.required {
				required++
			}
		}
		fmt.Fprintf(out, "  [%s] %-34s %s\n", mark, r.name, r.detail)
	}

	fmt.Fprintln(out)
	switch {
	case failed == 0:
		fmt.Fprintln(out, "This endpoint supports everything strata needs.")
		return nil
	case required > 0:
		fmt.Fprintln(out, "This endpoint is missing behaviour strata requires. It will not work correctly.")
		return errors.New("endpoint failed a required check")
	default:
		fmt.Fprintln(out, "This endpoint works, with the caveats noted above.")
		fmt.Fprintln(out, "Without conditional writes strata cannot detect a second writer:")
		fmt.Fprintln(out, "two mounts of the same bucket will silently overwrite each other.")
		fmt.Fprintln(out, "Run exactly one mount per bucket.")
		return nil
	}
}

type result struct {
	name     string
	ok       bool
	required bool
	detail   string
}

func pass(name, detail string) result {
	return result{name: name, ok: true, required: true, detail: detail}
}

func fail(name string, required bool, format string, args ...any) result {
	return result{name: name, ok: false, required: required, detail: fmt.Sprintf(format, args...)}
}

func checkRoundTrip(ctx context.Context, s store.Store, prefix string) result {
	const name = "put and get"
	key := prefix + "roundtrip"
	want := []byte("strata compatibility probe")
	if err := s.Put(ctx, key, want); err != nil {
		return fail(name, true, "put failed: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		return fail(name, true, "get failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		return fail(name, true, "returned %d bytes, wrote %d", len(got), len(want))
	}
	return pass(name, "objects round-trip intact")
}

func checkAwkwardKeys(ctx context.Context, s store.Store, prefix string) result {
	const name = "keys needing escaping"
	// Percent-encoding in the canonical URI is the usual cause of signature
	// mismatches against a non-AWS implementation.
	for _, n := range []string{"with space.txt", "plus+sign", "unicode-ünïcödé", "parens(1)"} {
		key := prefix + n
		if err := s.Put(ctx, key, []byte(n)); err != nil {
			return fail(name, true, "put %q failed: %v", n, err)
		}
		got, err := s.Get(ctx, key)
		if err != nil {
			return fail(name, true, "get %q failed: %v", n, err)
		}
		if string(got) != n {
			return fail(name, true, "%q round-tripped incorrectly", n)
		}
	}
	return pass(name, "signing handles spaces and non-ASCII")
}

func checkRange(ctx context.Context, s store.Store, prefix string) result {
	const name = "ranged reads"
	key := prefix + "range"
	if err := s.Put(ctx, key, []byte("0123456789abcdefghij")); err != nil {
		return fail(name, true, "put failed: %v", err)
	}
	got, err := s.GetRange(ctx, key, 5, 5)
	if err != nil {
		return fail(name, true, "range get failed: %v", err)
	}
	if string(got) != "56789" {
		return fail(name, true, "returned %q, want %q", got, "56789")
	}
	return pass(name, "partial reads work; chunks fetch individually")
}

func checkHeadETag(ctx context.Context, s store.Store, prefix string) result {
	const name = "head returns an etag"
	key := prefix + "head"
	if err := s.Put(ctx, key, []byte("abcdef")); err != nil {
		return fail(name, true, "put failed: %v", err)
	}
	info, err := s.Head(ctx, key)
	if err != nil {
		return fail(name, true, "head failed: %v", err)
	}
	if info.Size != 6 {
		return fail(name, true, "reported size %d, want 6", info.Size)
	}
	if info.ETag == "" {
		return fail(name, true, "no ETag returned; conditional writes cannot work")
	}
	return pass(name, "etag "+short(info.ETag))
}

func checkMissingIsNotFound(ctx context.Context, s store.Store, prefix string) result {
	const name = "missing key reports 404"
	if _, err := s.Get(ctx, prefix+"definitely-absent"); !errors.Is(err, store.ErrNotFound) {
		return fail(name, true, "got %v, want a not-found error", err)
	}
	return pass(name, "absent objects are distinguishable")
}

func checkList(ctx context.Context, s store.Store, prefix string) result {
	const name = "list with prefix"
	lp := prefix + "list/"
	for _, n := range []string{"a", "b", "c"} {
		if err := s.Put(ctx, lp+n, []byte(n)); err != nil {
			return fail(name, true, "put failed: %v", err)
		}
	}
	objs, err := s.List(ctx, lp, "", 100)
	if err != nil {
		return fail(name, true, "list failed: %v", err)
	}
	if len(objs) != 3 {
		return fail(name, true, "returned %d objects, want 3", len(objs))
	}
	return pass(name, "listing is needed to prune old snapshots")
}

func checkDelete(ctx context.Context, s store.Store, prefix string) result {
	const name = "delete"
	key := prefix + "doomed"
	if err := s.Put(ctx, key, []byte("x")); err != nil {
		return fail(name, true, "put failed: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		return fail(name, true, "delete failed: %v", err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, store.ErrNotFound) {
		return fail(name, true, "object still readable after delete")
	}
	return pass(name, "objects can be reclaimed")
}

// checkConditionalWrites probes the compare-and-swap that makes a commit
// atomic. These are reported as optional, because strata falls back to an
// unconditional write, but losing them means losing all protection against a
// second mount of the same bucket.
func checkConditionalWrites(ctx context.Context, s store.Store, prefix string) []result {
	const (
		nCreate = "conditional create (If-None-Match)"
		nUpdate = "conditional update (If-Match)"
		nReject = "stale If-Match is REJECTED"
	)
	key := prefix + "cas"

	etag, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":1}`), "")
	if err != nil {
		if errors.Is(err, store.ErrUnsupported) {
			return []result{
				fail(nCreate, false, "not supported by this endpoint"),
				fail(nUpdate, false, "not supported by this endpoint"),
				fail(nReject, false, "not supported by this endpoint"),
			}
		}
		return []result{
			fail(nCreate, false, "failed: %v", err),
			fail(nUpdate, false, "skipped"),
			fail(nReject, false, "skipped"),
		}
	}
	out := []result{pass(nCreate, "create-if-absent honoured")}

	if etag == "" {
		info, herr := s.Head(ctx, key)
		if herr != nil {
			return append(out, fail(nUpdate, false, "head failed: %v", herr), fail(nReject, false, "skipped"))
		}
		etag = info.ETag
	}

	if _, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":2}`), etag); err != nil {
		return append(out, fail(nUpdate, false, "failed with a correct ETag: %v", err), fail(nReject, false, "skipped"))
	}
	out = append(out, pass(nUpdate, "update with the current etag succeeds"))

	// The one that actually matters: a write conditioned on a superseded ETag
	// must be refused. An endpoint that accepts it will silently let two mounts
	// overwrite each other's commits.
	_, err = s.PutIfMatch(ctx, key, []byte(`{"epoch":3}`), etag)
	switch {
	case errors.Is(err, store.ErrPrecondition):
		out = append(out, pass(nReject, "a second writer is detected, not silently lost"))
	case err == nil:
		out = append(out, fail(nReject, false, "ACCEPTED a stale ETag; concurrent mounts are unsafe"))
	default:
		out = append(out, fail(nReject, false, "failed with an unexpected error: %v", err))
	}
	return out
}

func cleanup(ctx context.Context, s store.Store, prefix string) {
	objs, err := s.List(ctx, prefix, "", 1000)
	if err != nil {
		return
	}
	for _, o := range objs {
		s.Delete(ctx, o.Key)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}
