package blobfs

// Clean-room tests for ADR 0011, "A read-only FS writes nothing to its bucket"
// (docs/adr/0011-a-read-only-fs-writes-nothing-to-its-bucket.md), which are the
// tests for #68 in package blobfs. They are written from §1 to §4 and §7 of
// that ADR, without reading the implementation. They cover:
//
//   - §1 and §2, §7 items 1 and 2: a read-only New refuses a bucket that holds
//     no filesystem, that is one where Get of the root pointer reports
//     store.ErrNotFound: an empty bucket, one holding only an unrelated object,
//     and one whose filesystem lost its root pointer. It returns a nil *FS and
//     a non-nil error whose text contains "no filesystem" and the store's
//     name, makes no call of Put, Delete or PutIfMatch, and leaves the bucket
//     as it was. A writable New over an empty bucket still creates a
//     filesystem there.
//   - §4, §7 item 3: a read-only mount of a bucket that holds a filesystem
//     serves reads, answers vfs.ErrROFS to every call that would change it,
//     grants no modify, extend or delete through Access, returns nil from
//     Commit and Sync, and lets Run return once its context is cancelled;
//     through all of it no write reaches the store and the bucket is
//     unchanged.
//   - §3, §7 item 4: a read-only FS whose FS.dirty has been set, standing for a
//     build in which some path dirties a read-only mount, still writes nothing
//     when it is synced.
//   - §1, §7 item 5: a bucket whose root pointer is present but whose
//     snapshots are all gone is never taken for an empty one, read-only or not.
//
// Not covered, on purpose:
//   - cmd/strata and the NFS server (§5, §6): that the refusal comes before the
//     listener and the mount instructions, and the exit status.
//   - Which reads New makes, and how many (§2, §7): only the writes are
//     counted.
//   - §3's mechanism beyond test 3 (§7): that the FS's store is
//     store.ReadOnly, that it carries every call, and what Sync returns when a
//     write is refused there.
//   - The local directory that store.NewLocal creates, and -check under
//     -read-only, which ADR 0011 leaves open (*What this does not decide*).
//   - The error's text beyond its two parts, whether it wraps
//     store.ErrNotFound, and what is logged (§2, §7).
//
// ADR 0011 §7 says how a test stays safe against a build without it, and every
// test here follows it:
//   - every Config sets SnapshotRetention to −1, so that a commit starts no
//     pruning goroutine: ciConfig's, or bpNew's when seeding through
//     bpSeedFile;
//   - nothing calls Run, Sync or any other method on an FS that New returned
//     for a bucket with no root pointer, or with no snapshots; the case is
//     reported failed instead;
//   - every New and every FS call runs in bpGo, bpDo or a bp* helper, which
//     recovers a panic, and is waited for under btHangBound, with one
//     exception: seeding with bpSeedFile (in TestNewOverABucketWithNoRootPointer,
//     TestReadOnlySyncWritesNothingEvenWhenDirty and
//     TestNewNeverTreatsADamagedFilesystemAsEmpty) calls New directly, through
//     bpNew, on a writable mount and before anything under test runs; that New
//     is the one call not run under a panic-recovering bound. Goroutines never
//     touch t, and nothing here is parallel;
//   - ciStore's counts are read before the bucket is inspected, and the bucket
//     is inspected through the *store.Local beneath ciStore, never through
//     ciStore;
//   - every bucket is a local directory under the test's temporary directory
//     (bpLocal).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// ro11Writes names the three store.Store methods that write (ADR 0011 §2, §3;
// Assumption 6).
var ro11Writes = []string{"Put", "Delete", "PutIfMatch"}

// ro11Result is what one New returned.
type ro11Result struct {
	fs  *FS
	err error
}

// ro11New runs New with cfg in bpGo and waits for it under btHangBound.
func ro11New(t *testing.T, what string, cfg Config) ro11Result {
	t.Helper()
	return bpGo(func() ro11Result {
		fs, err := New(context.Background(), cfg)
		return ro11Result{fs, err}
	}).wait(t, what)
}

// ro11MustNew is ro11New for a New that must succeed.
func ro11MustNew(t *testing.T, what string, cfg Config) *FS {
	t.Helper()
	r := ro11New(t, what, cfg)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error", what)
	}
	return r.fs
}

// ro11WantNoWrites requires counts, read from the fake before the bucket is
// inspected, to hold no call of Put, Delete or PutIfMatch, and st, read
// directly, to hold what before recorded.
func ro11WantNoWrites(t *testing.T, what string, counts map[string]int, st *store.Local, before ciBucket, why string) {
	t.Helper()
	for _, m := range ro11Writes {
		if n := counts[m]; n != 0 {
			t.Errorf("%s: %d call(s) of %s reached cfg.Store, want none: %s", what, n, m, why)
		}
	}
	after := ciRecord(t, st, what+": after")
	for _, change := range ciDiff(before, after) {
		t.Errorf("%s: %s, want the bucket unchanged: %s", what, change, why)
	}
}

// ro11HasKey reports whether b's listing holds key.
func ro11HasKey(b ciBucket, key string) bool {
	_, ok := b.sizes[key]
	return ok
}

// ro11Count returns how many keys in b's listing start with prefix.
func ro11Count(b ciBucket, prefix string) int {
	n := 0
	for k := range b.sizes {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// TestNewOverABucketWithNoRootPointer pins ADR 0011 §1 and §2, and §7 items 1
// and 2. §1: "A bucket holds no filesystem when `Get` of the root pointer's
// key, `root`, returns an error for which `errors.Is(err, store.ErrNotFound)`
// holds", whatever else it holds. §2: with cfg.ReadOnly set, New then "returns
// a nil `*FS` and a non-nil error", "makes no call of `Put`, `Delete` or
// `PutIfMatch` on `cfg.Store`, creates nothing", and "The error's text
// contains `no filesystem` and `cfg.Store.Name()`". §2: "A writable `New` is
// unchanged", so over an empty bucket it creates a filesystem (§7 item 2).
func TestNewOverABucketWithNoRootPointer(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, st *store.Local)
	}{
		{"read-only, an empty bucket", func(t *testing.T, st *store.Local) {}},
		{"read-only, a bucket holding only an unrelated object", func(t *testing.T, st *store.Local) {
			t.Helper()
			if err := st.Put(context.Background(), "unrelated/object", []byte("not strata's")); err != nil {
				t.Fatalf("setup: Put of unrelated/object directly on the bucket: %v", err)
			}
		}},
		{"read-only, a filesystem whose root pointer was deleted", func(t *testing.T, st *store.Local) {
			t.Helper()
			bpSeedFile(t, st, "seed.bin", 3000)
			if err := st.Delete(context.Background(), rootKey); err != nil {
				t.Fatalf("setup: Delete of the root pointer %q directly on the bucket: %v", rootKey, err)
			}
			b := ciRecord(t, st, "setup: after deleting the root pointer")
			if ro11HasKey(b, rootKey) {
				t.Fatalf("setup: the bucket still lists the root pointer %q after it was deleted", rootKey)
			}
			if ro11Count(b, snapshotPrefix) == 0 {
				t.Fatalf("setup: the seeded bucket lists no object under %q, so it does not stand for "+
					"a filesystem whose root pointer was deleted", snapshotPrefix)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := bpLocal(t)
			tc.setup(t, st)
			before := ciRecord(t, st, "before New")
			cs := newCIStore(st)
			what := "New, " + tc.name

			r := ro11New(t, what, ciConfig(cs, 0, true))
			counts := cs.counts()

			if r.fs != nil {
				t.Errorf("%s returned a non-nil *FS, want nil: a read-only New refuses a bucket that "+
					"holds no filesystem (ADR 0011 §2, §7 item 1). The FS is not used (§7)", what)
			}
			if r.err == nil {
				t.Errorf("%s returned a nil error, want a non-nil one (ADR 0011 §2)", what)
			} else {
				msg := r.err.Error()
				if !strings.Contains(msg, "no filesystem") {
					t.Errorf("%s: error %q, want it to contain %q (ADR 0011 §2)", what, msg, "no filesystem")
				}
				if name := st.Name(); !strings.Contains(msg, name) {
					t.Errorf("%s: error %q, want it to contain the store's name as given, %q "+
						"(ADR 0011 §2)", what, msg, name)
				}
			}
			ro11WantNoWrites(t, what, counts, st, before, "a read-only New refuses after the read "+
				"that tells it the bucket holds no filesystem, and before any write, and creates "+
				"nothing (ADR 0011 §2, §7 item 1)")
			if after := ciRecord(t, st, what+": after, for the root pointer"); ro11HasKey(after, rootKey) {
				t.Errorf("%s: the bucket holds a root pointer %q afterwards, want none (ADR 0011 §7 "+
					"item 1)", what, rootKey)
			}
		})
	}

	t.Run("writable, an empty bucket", func(t *testing.T) {
		st := bpLocal(t)
		what := "New, writable, over an empty bucket"
		r := ro11New(t, what, ciConfig(st, 0, false))
		if r.err != nil {
			t.Fatalf("%s = %v, want success: a writable New is unchanged and creates a filesystem "+
				"in an empty bucket (ADR 0011 §2, §5, §7 item 2)", what, r.err)
		}
		if r.fs == nil {
			t.Fatalf("%s returned a nil *FS and a nil error, want a non-nil *FS (ADR 0011 §7 item 2)", what)
		}
		if after := ciRecord(t, st, what+": after"); !ro11HasKey(after, rootKey) {
			t.Errorf("%s: the bucket holds no object under %q afterwards, want one: a writable New "+
				"over an empty bucket commits a new filesystem (ADR 0011 §7 item 2)", what, rootKey)
		}
	})
}

// ro11Seed puts a filesystem into the empty bucket st through a writable mount
// that is never run: a regular file named file, holding unique bytes, and an
// empty directory named dir, both in the root, committed by a Sync. It returns
// the file's bytes.
func ro11Seed(t *testing.T, st *store.Local, file, dir string) []byte {
	t.Helper()
	fs := ro11MustNew(t, "setup: New, writable, over an empty bucket", ciConfig(st, 0, false))
	h, _ := bpCreate(t, fs, file, 0o644)
	data := bpUnique(3000)
	if r := bpWrite(t, fs, "setup: Write of "+file, h, 0, data, vfs.Unstable); r.err != nil || int(r.n) != len(data) {
		t.Fatalf("setup: Write of %d bytes to %s = %v, want them all written", len(data), file, r)
	}
	err := bpDo(t, "setup: Mkdir "+dir, func() error {
		mode := uint32(0o755)
		_, _, err := fs.Mkdir(context.Background(), testCaller, fs.Root(), dir, vfs.SetAttr{Mode: &mode})
		return err
	})
	if err != nil {
		t.Fatalf("setup: Mkdir %s = %v, want success", dir, err)
	}
	bpMustSync(t, fs, "setup: Sync of the seeded filesystem")
	return data
}

// ro11ReadDirNames lists dir to its end, with plus as given, under the hang
// bound, and returns the names it saw.
func ro11ReadDirNames(t *testing.T, fs *FS, dir vfs.Handle, plus bool, what string) []string {
	t.Helper()
	type res struct {
		names []string
		err   error
	}
	r := bpGo(func() res {
		var (
			names  []string
			cookie uint64
			verf   [8]byte
		)
		for page := 0; page < 100; page++ {
			entries, eof, outVerf, err := fs.ReadDir(context.Background(), testCaller, dir, cookie, verf, 64*1024, plus)
			if err != nil {
				return res{names, err}
			}
			for _, e := range entries {
				names = append(names, e.Name)
				cookie = e.Cookie
			}
			verf = outVerf
			if eof || len(entries) == 0 {
				return res{names, nil}
			}
		}
		return res{names, fmt.Errorf("no eof after 100 pages")}
	}).wait(t, what)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	return r.names
}

// ro11Has reports whether names holds name.
func ro11Has(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// ro11WantROFS requires err to be one that errors.Is finds to be vfs.ErrROFS.
func ro11WantROFS(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, vfs.ErrROFS) {
		t.Errorf("%s on a read-only mount = %v, want vfs.ErrROFS: given arguments a writable mount "+
			"would accept, it answers vfs.ErrROFS (ADR 0011 §4, §7 item 3)", what, err)
	}
}

// TestReadOnlyMountWritesNothing pins ADR 0011 §4 and §7 item 3: with
// Config.ReadOnly set, over a bucket that holds a filesystem, New "returns a
// non-nil `*FS` and a nil error. Then, whatever `vfs.FS` methods, `Sync`,
// `Commit` and `Run` are called, no call of `Put`, `Delete` or `PutIfMatch`
// reaches `cfg.Store`; the bucket's objects, their sizes and the root
// pointer's bytes stay as they were; and the calls behave as §4 lists."
func TestReadOnlyMountWritesNothing(t *testing.T) {
	const (
		file = "file.bin"
		dir  = "subdir"
	)
	st := bpLocal(t)
	data := ro11Seed(t, st, file, dir)
	before := ciRecord(t, st, "before the read-only New")
	cs := newCIStore(st)
	fs := ro11MustNew(t, "New, read-only, over a bucket that holds a filesystem (ADR 0011 §7 item 3)",
		ciConfig(cs, 0, true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := bpGo(func() struct{} {
		fs.Run(ctx)
		return struct{}{}
	})

	bg := context.Background()
	root := bpGo(func() vfs.Handle { return fs.Root() }).wait(t, "Root on the read-only mount")

	// Reads.
	h, attr := bpLookup(t, fs, file)
	if attr.Type != vfs.TypeReg || attr.Size != uint64(len(data)) {
		t.Errorf("Lookup %s on the read-only mount: type %d, size %d, want a regular file of %d bytes",
			file, attr.Type, attr.Size, len(data))
	}
	bpLookup(t, fs, dir)
	bpGetAttr(t, fs, root, "GetAttr of the root on the read-only mount")
	bpGetAttr(t, fs, h, "GetAttr of "+file+" on the read-only mount")
	if got := bpReadAll(t, fs, h, "Read of the whole of "+file+" on the read-only mount"); !bytes.Equal(got, data) {
		t.Errorf("Read of the whole of %s on the read-only mount returned %d bytes that differ from "+
			"the %d seeded", file, len(got), len(data))
	}
	for _, plus := range []bool{false, true} {
		what := fmt.Sprintf("ReadDir of the root with plus %v on the read-only mount", plus)
		names := ro11ReadDirNames(t, fs, root, plus, what)
		for _, want := range []string{file, dir} {
			if !ro11Has(names, want) {
				t.Errorf("%s listed %q, want it to include %q", what, names, want)
			}
		}
	}
	if err := bpDo(t, "FSStat on the read-only mount", func() error {
		_, err := fs.FSStat(bg, root)
		return err
	}); err != nil {
		t.Errorf("FSStat on the read-only mount = %v, want success", err)
	}
	const all = vfs.AccessRead | vfs.AccessLookup | vfs.AccessModify | vfs.AccessExtend |
		vfs.AccessDelete | vfs.AccessExecute
	const writeBits = vfs.AccessModify | vfs.AccessExtend | vfs.AccessDelete
	for _, target := range []struct {
		name string
		h    vfs.Handle
	}{{"the root", root}, {file, h}} {
		what := "Access of " + target.name + " on the read-only mount"
		type res struct {
			got uint32
			err error
		}
		r := bpGo(func() res {
			got, err := fs.Access(bg, testCaller, target.h, all)
			return res{got, err}
		}).wait(t, what)
		if r.err != nil {
			t.Errorf("%s = %v, want success", what, r.err)
		} else if r.got&writeBits != 0 {
			t.Errorf("%s granted %#x, want none of AccessModify, AccessExtend and AccessDelete "+
				"(%#x) (ADR 0011 §4)", what, r.got, writeBits)
		}
	}

	// Every call that would change the filesystem.
	mode := uint32(0o600)
	ro11WantROFS(t, "SetAttr of the mode", bpDo(t, "SetAttr of the mode", func() error {
		_, err := fs.SetAttr(bg, testCaller, h, vfs.SetAttr{Mode: &mode})
		return err
	}))
	size := uint64(10)
	ro11WantROFS(t, "SetAttr of the size", bpDo(t, "SetAttr of the size", func() error {
		_, err := fs.SetAttr(bg, testCaller, h, vfs.SetAttr{Size: &size})
		return err
	}))
	for _, how := range []vfs.Stability{vfs.Unstable, vfs.FileSync} {
		what := "Write " + bpHow(how)
		r := bpWrite(t, fs, what, h, 0, []byte("refused"), how)
		ro11WantROFS(t, what, r.err)
		if r.n != 0 {
			t.Errorf("%s on the read-only mount = %v, want a count of 0 (ADR 0011 §4)", what, r)
		}
	}
	ro11WantROFS(t, "Create of a new name, exclusive", bpDo(t, "Create of a new name, exclusive", func() error {
		_, _, err := fs.Create(bg, testCaller, root, "new.bin", vfs.SetAttr{}, true)
		return err
	}))
	ro11WantROFS(t, "Create of the existing file, non-exclusive", bpDo(t, "Create of the existing file, non-exclusive", func() error {
		_, _, err := fs.Create(bg, testCaller, root, file, vfs.SetAttr{}, false)
		return err
	}))
	ro11WantROFS(t, "Mkdir", bpDo(t, "Mkdir", func() error {
		dmode := uint32(0o755)
		_, _, err := fs.Mkdir(bg, testCaller, root, "newdir", vfs.SetAttr{Mode: &dmode})
		return err
	}))
	ro11WantROFS(t, "Symlink", bpDo(t, "Symlink", func() error {
		_, _, err := fs.Symlink(bg, testCaller, root, "newlink", file, vfs.SetAttr{})
		return err
	}))
	ro11WantROFS(t, "Remove of "+file, bpDo(t, "Remove of "+file, func() error {
		return fs.Remove(bg, testCaller, root, file)
	}))
	ro11WantROFS(t, "Rmdir of "+dir, bpDo(t, "Rmdir of "+dir, func() error {
		return fs.Rmdir(bg, testCaller, root, dir)
	}))
	ro11WantROFS(t, "Rename of "+file+" to a new name", bpDo(t, "Rename of "+file, func() error {
		return fs.Rename(bg, testCaller, root, file, root, "renamed.bin")
	}))

	// Commit and Sync, with nothing to commit.
	if err := bpDo(t, "Commit of "+file, func() error { return fs.Commit(bg, h, 0, 0) }); err != nil {
		t.Errorf("Commit of %s on the read-only mount = %v, want nil (ADR 0011 §4)", file, err)
	}
	if err := bpSync(t, fs, "Sync on the read-only mount"); err != nil {
		t.Errorf("Sync on the read-only mount = %v, want nil (ADR 0011 §4)", err)
	}

	// Run returns once its context is cancelled; Sync afterwards still answers nil.
	cancel()
	run.wait(t, "Run on the read-only mount, once its context was cancelled (ADR 0011 §4)")
	if err := bpSync(t, fs, "Sync on the read-only mount after Run returned"); err != nil {
		t.Errorf("Sync on the read-only mount after Run returned = %v, want nil (ADR 0011 §4)", err)
	}

	counts := cs.counts()
	ro11WantNoWrites(t, "the read-only mount", counts, st, before, "no call of Put, Delete or "+
		"PutIfMatch reaches cfg.Store from a read-only FS, and the bucket's objects, their sizes "+
		"and the root pointer's bytes stay as they were (ADR 0011 §3, §4, §7 item 3)")
}

// TestReadOnlySyncWritesNothingEvenWhenDirty pins ADR 0011 §3 and §7 item 4:
// with Config.ReadOnly set, a bucket that holds a filesystem, and FS.dirty set
// to true holding FS.mu for writing after New has returned, "`Sync` makes no
// call of `Put`, `Delete` or `PutIfMatch` on `cfg.Store`, and the bucket stays
// as it was. What `Sync` returns is not pinned."
func TestReadOnlySyncWritesNothingEvenWhenDirty(t *testing.T) {
	st := bpLocal(t)
	bpSeedFile(t, st, "seed.bin", 3000)
	before := ciRecord(t, st, "before the read-only New")
	cs := newCIStore(st)
	fs := ro11MustNew(t, "New, read-only, over a bucket that holds a filesystem (ADR 0011 §7 item 4)",
		ciConfig(cs, 0, true))

	bpGo(func() struct{} {
		fs.mu.Lock()
		fs.dirty = true
		fs.mu.Unlock()
		return struct{}{}
	}).wait(t, "setting FS.dirty holding FS.mu for writing")

	err := bpSync(t, fs, "Sync on a read-only mount with FS.dirty set")
	counts := cs.counts()
	ro11WantNoWrites(t, fmt.Sprintf("Sync on a read-only mount with FS.dirty set (it returned %v)", err),
		counts, st, before, "a read-only FS reaches its store only through store.ReadOnly, so no "+
			"write reaches cfg.Store whatever is called on it (ADR 0011 §3, §7 item 4)")
}

// TestNewNeverTreatsADamagedFilesystemAsEmpty pins ADR 0011 §1 and §7 item 5:
// "For a bucket whose root pointer is present but whose snapshots have all been
// deleted, with `Config.ReadOnly` set or not: `New` returns a nil `*FS` and a
// non-nil error, makes no call of `Put`, `Delete` or `PutIfMatch` on
// `cfg.Store`, and leaves the bucket as it was." §1's table: a root pointer
// "that names a snapshot that cannot be read" fails, "writing nothing", in
// either mode.
//
// Each subtest records the bucket itself, so that a change made by one is not
// reported again by the next.
func TestNewNeverTreatsADamagedFilesystemAsEmpty(t *testing.T) {
	st := bpLocal(t)
	bpSeedFile(t, st, "seed.bin", 3000)
	ctx := context.Background()
	snaps, err := st.List(ctx, snapshotPrefix, "", 0)
	if err != nil {
		t.Fatalf("setup: listing %q directly on the bucket: %v", snapshotPrefix, err)
	}
	if len(snaps) == 0 {
		t.Fatalf("setup: the seeded bucket lists no object under %q", snapshotPrefix)
	}
	for _, o := range snaps {
		if err := st.Delete(ctx, o.Key); err != nil {
			t.Fatalf("setup: Delete of %q directly on the bucket: %v", o.Key, err)
		}
	}
	b := ciRecord(t, st, "setup: after deleting every snapshot")
	if !ro11HasKey(b, rootKey) {
		t.Fatalf("setup: the seeded bucket lists no root pointer %q", rootKey)
	}
	if n := ro11Count(b, snapshotPrefix); n != 0 {
		t.Fatalf("setup: the bucket still lists %d object(s) under %q after they were deleted", n, snapshotPrefix)
	}

	for _, readOnly := range []bool{true, false} {
		name := "writable"
		if readOnly {
			name = "read-only"
		}
		t.Run(name, func(t *testing.T) {
			before := ciRecord(t, st, "before New")
			cs := newCIStore(st)
			what := "New, " + name + ", over a bucket whose root pointer names a deleted snapshot"
			r := ro11New(t, what, ciConfig(cs, 0, readOnly))
			counts := cs.counts()
			if r.fs != nil {
				t.Errorf("%s returned a non-nil *FS, want nil: a damaged filesystem is never taken "+
					"for an empty bucket (ADR 0011 §1, §7 item 5). The FS is not used (§7)", what)
			}
			if r.err == nil {
				t.Errorf("%s returned a nil error, want a non-nil one (ADR 0011 §1, §7 item 5)", what)
			}
			ro11WantNoWrites(t, what, counts, st, before, "New fails, writing nothing, when the "+
				"root pointer names a snapshot that cannot be read (ADR 0011 §1, §7 item 5)")
		})
	}
}
