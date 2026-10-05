package blobfs

// Clean-room tests for ADR 0009, "A negative commit interval is refused before
// anything starts" (docs/adr/0009-a-negative-commit-interval-is-refused.md),
// which are the tests for #73 in package blobfs. They are written from §1, §2,
// §4 and §5 of that ADR, without reading the implementation. They cover:
//
//   - §1, what Config.CommitInterval means: every negative value is refused, 0
//     becomes 5 s, and any positive value is kept as given, read through
//     FS.commitInterval, with Config.ReadOnly set and not;
//   - §2, New refuses a negative interval before it touches the store: a nil
//     *FS, a non-nil error whose text contains "commit interval" and
//     d.String(), no call of any of the eight store.Store methods, Name
//     included, and a bucket left exactly as it was, with Config.ReadOnly set
//     and not, over an empty bucket and over one that holds a filesystem;
//   - §4 and §5, Run on a writable mount, for every interval New accepts,
//     returns once its context is cancelled, without panicking.
//
// "Starts no goroutine" (§2) is shown by New's nil *FS and by no call on the
// store, not by counting goroutines: a refused New leaves nothing that could
// run one.
//
// Not covered, on purpose, because ADR 0009 §5 leaves them to review: that
// run, in cmd/strata, calls commitInterval before openStore and passes its
// result into Config; the exit status; that no usage text is printed; and that
// the committer is unchanged. cmd/strata's commitInterval is covered by
// cmd/strata/main_test.go. Nor do these tests rely on which fault New reports
// when cfg has more than one (§2), on what it logs, on what it does with a
// read-only mount of an empty bucket (#68), or on Run's internals.
//
// ADR 0009 §5 says how a test stays safe against a build without §2, and every
// test here follows it:
//   - nothing calls Run on, or otherwise uses, an FS that New returned for a
//     negative interval; the case is reported failed instead;
//   - Run is called only through bpGo, which recovers a panic, and is waited
//     for with bpCall.wait, under btHangBound, so that a panic or a hang fails
//     the test instead of ending the test binary. New, in
//     TestNewRefusesANegativeCommitInterval, runs the same way.
//
// A read-only mount that New must accept is over a bucket that already holds a
// filesystem, made first by a writable mount (ciFill), never over an empty one
// (§5, #68). ciStore's counts are read before the bucket is inspected, and the
// bucket is inspected through the *store.Local that ciStore wraps, never
// through ciStore. Goroutines never touch t, and nothing here is parallel.
//
// rootKey, the root pointer's key, is used only as fs_test.go uses it: as the
// key that a listed object is compared with. ADR 0009 does not pin it.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
)

// ciRunFor is how long TestRunWithEveryAcceptedCommitInterval lets Run run
// before it cancels Run's context. It is this file's one fixed wait, and it
// only checks that something does NOT happen: that Run does not panic while it
// runs, as a committer that makes a ticker from a non-positive interval does at
// once (ADR 0009, Context).
const ciRunFor = 20 * time.Millisecond

// ciMethods names the eight store.Store methods, in the order ADR 0009 §5
// restates them.
var ciMethods = []string{"Get", "GetRange", "Head", "Put", "Delete", "List", "PutIfMatch", "Name"}

// ciStore is a store.Store that wraps another and counts every call of each of
// its eight methods, Name included, before it delegates the call. It is safe
// for concurrent use. It does not embed store.Store, so that every method it
// has is one that counts.
type ciStore struct {
	inner store.Store
	mu    sync.Mutex
	calls map[string]int
}

var _ store.Store = (*ciStore)(nil)

// newCIStore returns a ciStore over inner that has counted nothing.
func newCIStore(inner store.Store) *ciStore {
	return &ciStore{inner: inner, calls: make(map[string]int, len(ciMethods))}
}

// count records one call of method.
func (s *ciStore) count(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[method]++
}

// counts returns a copy of the counts so far, with an entry for each of the
// eight methods.
func (s *ciStore) counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(ciMethods))
	for _, m := range ciMethods {
		out[m] = s.calls[m]
	}
	return out
}

func (s *ciStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.count("Get")
	return s.inner.Get(ctx, key)
}

func (s *ciStore) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	s.count("GetRange")
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *ciStore) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	s.count("Head")
	return s.inner.Head(ctx, key)
}

func (s *ciStore) Put(ctx context.Context, key string, data []byte) error {
	s.count("Put")
	return s.inner.Put(ctx, key, data)
}

func (s *ciStore) Delete(ctx context.Context, key string) error {
	s.count("Delete")
	return s.inner.Delete(ctx, key)
}

func (s *ciStore) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	s.count("List")
	return s.inner.List(ctx, prefix, after, max)
}

func (s *ciStore) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	s.count("PutIfMatch")
	return s.inner.PutIfMatch(ctx, key, data, etag)
}

func (s *ciStore) Name() string {
	s.count("Name")
	return s.inner.Name()
}

// ciConfig is the Config every New in this file is given.
func ciConfig(st store.Store, d time.Duration, readOnly bool) Config {
	return Config{
		Store:             st,
		ChunkSize:         bpCS,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		CommitInterval:    d,
		ReadOnly:          readOnly,
		Log:               quietLog(),
	}
}

// ciMount describes a mount for test names and failure messages.
func ciMount(readOnly, filled bool) string {
	mode, bucket := "writable", "an empty bucket"
	if readOnly {
		mode = "read-only"
	}
	if filled {
		bucket = "a bucket holding a filesystem"
	}
	return mode + ", over " + bucket
}

// ciFill puts a filesystem into the empty bucket st, through a writable mount
// with the default commit interval. Run is never called on that mount.
func ciFill(t *testing.T, st *store.Local) {
	t.Helper()
	mfsMount(t, "setup: New, writable, over an empty bucket, to put a filesystem in it",
		ciConfig(st, 0, false))
	if objs, err := st.List(context.Background(), "", "", 0); err != nil {
		t.Fatalf("setup: listing the bucket after the writable mount: %v", err)
	} else if len(objs) == 0 {
		t.Fatalf("setup: the bucket is still empty after a writable New over it, so it holds " +
			"no filesystem for the test to use (ADR 0009, Context: New creates one in an empty " +
			"bucket and commits it)")
	}
}

// ciBucket is what a bucket held at one moment: the size of every object, by
// key, and the bytes of the root pointer, if the listing has one.
type ciBucket struct {
	sizes    map[string]int64
	rootName string
	root     []byte
}

// ciRecord reads st's listing, and the root pointer's bytes, directly from st.
func ciRecord(t *testing.T, st *store.Local, when string) ciBucket {
	t.Helper()
	ctx := context.Background()
	objs, err := st.List(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("%s: listing the bucket: %v", when, err)
	}
	b := ciBucket{sizes: make(map[string]int64, len(objs))}
	for _, o := range objs {
		b.sizes[o.Key] = o.Size
		if o.Key == rootKey {
			data, err := st.Get(ctx, o.Key)
			if err != nil {
				t.Fatalf("%s: reading the root pointer %q: %v", when, o.Key, err)
			}
			b.rootName, b.root = o.Key, data
		}
	}
	return b
}

// ciDiff describes every way after differs from before.
func ciDiff(before, after ciBucket) []string {
	keys := make([]string, 0, len(before.sizes)+len(after.sizes))
	for k := range before.sizes {
		keys = append(keys, k)
	}
	for k := range after.sizes {
		if _, ok := before.sizes[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		was, inBefore := before.sizes[k]
		is, inAfter := after.sizes[k]
		switch {
		case !inBefore:
			out = append(out, fmt.Sprintf("object %q (%d bytes) appeared", k, is))
		case !inAfter:
			out = append(out, fmt.Sprintf("object %q (%d bytes) was deleted", k, was))
		case was != is:
			out = append(out, fmt.Sprintf("object %q changed size from %d to %d bytes", k, was, is))
		}
	}
	if before.rootName != "" && before.rootName == after.rootName && !bytes.Equal(before.root, after.root) {
		out = append(out, fmt.Sprintf("the root pointer %q changed from %q to %q",
			before.rootName, before.root, after.root))
	}
	return out
}

// TestNewRefusesANegativeCommitInterval pins ADR 0009 §2: "If
// `cfg.CommitInterval < 0`, `New` returns a nil `*FS` and a non-nil error."
// "It refuses before it makes any call on `cfg.Store`, of any `store.Store`
// method, `Name` included. So a refused `New` reads and writes nothing in the
// bucket, creates no filesystem in an empty one, builds no `FS`, and starts no
// goroutine." "`cfg.ReadOnly` makes no difference." "The error's text contains
// `commit interval` and the value as `time.Duration`'s `String` method writes
// it". ADR 0009 §5: a test may rely on that "for every negative interval, with
// `Config.ReadOnly` set and not, over an empty bucket and over one that holds a
// filesystem". §1's first row: every value from math.MinInt64 to −1 ns is
// refused.
//
// Each case records the bucket (every key and size, and the root pointer's
// bytes) before New, and checks after it that nothing changed. An FS that New
// returns here is never used (§5).
func TestNewRefusesANegativeCommitInterval(t *testing.T) {
	for _, d := range []time.Duration{-time.Nanosecond, -time.Second, -5 * time.Second, time.Duration(math.MinInt64)} {
		for _, readOnly := range []bool{false, true} {
			for _, filled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%v, %s", d, ciMount(readOnly, filled)), func(t *testing.T) {
					ciWantRefused(t, d, readOnly, filled)
				})
			}
		}
	}
}

// ciWantRefused is one case of TestNewRefusesANegativeCommitInterval.
func ciWantRefused(t *testing.T, d time.Duration, readOnly, filled bool) {
	t.Helper()
	st := bpLocal(t)
	if filled {
		ciFill(t, st)
	}
	before := ciRecord(t, st, "before New")
	cs := newCIStore(st)
	what := fmt.Sprintf("New with CommitInterval %v, %s", d, ciMount(readOnly, filled))

	type result struct {
		fs  *FS
		err error
	}
	r := bpGo(func() result {
		fs, err := New(context.Background(), ciConfig(cs, d, readOnly))
		return result{fs, err}
	}).wait(t, what)
	counts := cs.counts()

	if r.fs != nil {
		t.Errorf("%s returned a non-nil *FS, want nil: a refused New builds no FS (ADR 0009 "+
			"§2). The FS is not used, because Run on it would panic (§5)", what)
	}
	if r.err == nil {
		t.Errorf("%s returned a nil error, want a non-nil one (ADR 0009 §2)", what)
	} else {
		msg := r.err.Error()
		if !strings.Contains(msg, "commit interval") {
			t.Errorf("%s: error %q, want it to contain %q (ADR 0009 §2)", what, msg, "commit interval")
		}
		if given := d.String(); !strings.Contains(msg, given) {
			t.Errorf("%s: error %q, want it to contain the value as time.Duration's String "+
				"method writes it, %q (ADR 0009 §2)", what, msg, given)
		}
	}
	for _, m := range ciMethods {
		if n := counts[m]; n != 0 {
			t.Errorf("%s made %d call(s) of %s on cfg.Store, want none: New refuses before it "+
				"makes any call on cfg.Store, of any store.Store method, Name included (ADR "+
				"0009 §2, §5)", what, n, m)
		}
	}
	after := ciRecord(t, st, "after New")
	for _, change := range ciDiff(before, after) {
		t.Errorf("%s: %s, want the bucket unchanged: a refused New reads and writes nothing "+
			"in the bucket, and creates no filesystem in an empty one (ADR 0009 §2)", what, change)
	}
}

// TestNewAcceptsAZeroOrPositiveCommitInterval pins ADR 0009 §1's last two
// rows: New accepts 0 and sets FS.commitInterval to 5 s, the default, and
// accepts every interval from 1 ns to math.MaxInt64 and keeps it as given;
// "The table holds whether `Config.ReadOnly` is set or not." ADR 0009 §5:
// FS.commitInterval is "the value in §1's last column", and a test may read it
// once New has returned. A writable mount is over an empty bucket, and a
// read-only one over a bucket that holds a filesystem (§5, #68).
func TestNewAcceptsAZeroOrPositiveCommitInterval(t *testing.T) {
	for _, d := range []time.Duration{0, time.Nanosecond, time.Millisecond, 5 * time.Second, time.Hour, time.Duration(math.MaxInt64)} {
		want, why := d, "a positive interval is used as given, with no floor or ceiling (ADR 0009 §1, §4)"
		if d == 0 {
			want, why = 5*time.Second, "0 selects the default, 5 s (ADR 0009 §1)"
		}
		for _, readOnly := range []bool{false, true} {
			filled := readOnly
			t.Run(fmt.Sprintf("%v, %s", d, ciMount(readOnly, filled)), func(t *testing.T) {
				st := bpLocal(t)
				if filled {
					ciFill(t, st)
				}
				what := fmt.Sprintf("New with CommitInterval %v, %s", d, ciMount(readOnly, filled))
				fs := mfsMount(t, what+", which ADR 0009 §1 accepts", ciConfig(st, d, readOnly))
				if got := fs.commitInterval; got != want {
					t.Errorf("%s: FS.commitInterval = %v, want %v: %s; §1's table holds whether "+
						"Config.ReadOnly is set or not", what, got, want, why)
				}
			})
		}
	}
}

// TestRunWithEveryAcceptedCommitInterval pins ADR 0009 §4 and §5: on a writable
// mount, "for every interval `New` accepts", Run "returns once its context is
// cancelled, without panicking". It covers 0, which New takes as 5 s, the
// smallest interval, 1 ns, which §4 says is used as given however small, and a
// large one, 1 h, and the largest, math.MaxInt64 (ADR 0009, *What this does not
// decide*: time.NewTicker accepts it).
//
// Run runs in bpGo for ciRunFor, the one fixed wait, which only checks that it
// does not panic while it runs; then its context is cancelled and bpCall.wait
// requires it to return, without a panic, within btHangBound.
func TestRunWithEveryAcceptedCommitInterval(t *testing.T) {
	for _, d := range []time.Duration{0, time.Nanosecond, time.Hour, time.Duration(math.MaxInt64)} {
		t.Run(fmt.Sprintf("%v", d), func(t *testing.T) {
			fs := mfsMount(t, fmt.Sprintf("New with CommitInterval %v, writable, over an empty "+
				"bucket, which ADR 0009 §1 accepts", d), ciConfig(bpLocal(t), d, false))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			run := bpGo(func() struct{} {
				fs.Run(ctx)
				return struct{}{}
			})

			time.Sleep(ciRunFor)
			if run.finished() && run.panicked {
				t.Fatalf("Run on a writable mount with CommitInterval %v panicked within %v of "+
					"starting, before its context was cancelled, with %v; want no panic: every "+
					"FS New returns has a positive commitInterval, and the ticker the committer "+
					"makes from it never panics (ADR 0009 §1, §4, §5)", d, ciRunFor, run.pval)
			}

			cancel()
			run.wait(t, fmt.Sprintf("Run on a writable mount with CommitInterval %v, once its "+
				"context was cancelled (ADR 0009 §4, §5: it returns once its context is cancelled, "+
				"without panicking)", d))
		})
	}
}
