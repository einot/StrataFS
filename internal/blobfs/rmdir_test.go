package blobfs

// Tests for ADR 0017, "Removing a directory removes its inode": the test
// surface its §6 gives as L1 to L5, against the rule of §1, the link counts
// of §3 (R1 to R3), the FSSTAT count of §4, and the doc comment above
// vfs.FS.Remove.
//
// Safety (ADR 0017 §6; ADR 0012 §9; ADR 0011 §7): every bucket is a local
// directory under t.TempDir(), and every FS, including every remount, is
// built with SnapshotRetention -1, so that no commit starts a pruning
// goroutine that outlives the test. That is why these tests build their FS
// through rmdirFS and rmdirMount rather than newTestFS.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// rmdirMount mounts st with SnapshotRetention -1 (ADR 0017 §6, Safety).
func rmdirMount(t *testing.T, st *store.Local) *FS {
	t.Helper()
	fs, err := New(context.Background(), Config{Store: st, ChunkSize: 4096, OwnerUID: 501, OwnerGID: 20, SnapshotRetention: -1, Log: quietLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fs
}

// rmdirFS builds a fresh FS over a new local bucket under t.TempDir().
func rmdirFS(t *testing.T) (*FS, *store.Local) {
	t.Helper()
	st, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("store.NewLocal: %v", err)
	}
	return rmdirMount(t, st), st
}

// rmdirMkdir makes dir/name as testCaller and fails the test if it cannot.
func rmdirMkdir(t *testing.T, fs *FS, dir vfs.Handle, name string) (vfs.Handle, vfs.Attr) {
	t.Helper()
	h, attr, err := fs.Mkdir(context.Background(), testCaller, dir, name, vfs.SetAttr{})
	if err != nil {
		t.Fatalf("Mkdir %q: %v", name, err)
	}
	return h, attr
}

// rmdirWantNLink checks GetAttr(h).NLink against ADR 0017 §3.
func rmdirWantNLink(t *testing.T, fs *FS, h vfs.Handle, what string, want uint32, when string) {
	t.Helper()
	attr, err := fs.GetAttr(context.Background(), h)
	if err != nil {
		t.Fatalf("%s: GetAttr(%s): %v", when, what, err)
	}
	if attr.NLink != want {
		t.Errorf("%s: NLink of %s = %d, want %d (ADR 0017 §3)", when, what, attr.NLink, want)
	}
}

// rmdirUsed is FSSTAT's TotalFiles - FreeFiles for the root, which ADR 0017
// §4 pins as the number of inodes in the namespace, root included.
func rmdirUsed(t *testing.T, fs *FS) uint64 {
	t.Helper()
	s, err := fs.FSStat(context.Background(), fs.Root())
	if err != nil {
		t.Fatalf("FSStat(root): %v", err)
	}
	return s.TotalFiles - s.FreeFiles
}

// TestRemovedDirectoryHandleIsStale is ADR 0017 §6 L1: after Rmdir of an
// empty directory, every method given its handle fails with vfs.ErrStale
// (the vfs.FS contract above Remove; ADR 0017 §1). Each method gets a fresh
// FS, so that a call which wrongly succeeds cannot affect the next. The
// one-byte Write is deliberate: the contract lets a Write of no bytes
// succeed without looking at the handle.
func TestRemovedDirectoryHandleIsStale(t *testing.T) {
	ctx := context.Background()
	mode := uint32(0o700)
	cases := []struct {
		method string
		keep   bool
		call   func(fs *FS, root, h vfs.Handle) error
	}{
		{method: "GetAttr", call: func(fs *FS, root, h vfs.Handle) error {
			_, err := fs.GetAttr(ctx, h)
			return err
		}},
		{method: "SetAttr", call: func(fs *FS, root, h vfs.Handle) error {
			_, err := fs.SetAttr(ctx, testCaller, h, vfs.SetAttr{Mode: &mode})
			return err
		}},
		{method: "Access", call: func(fs *FS, root, h vfs.Handle) error {
			_, err := fs.Access(ctx, testCaller, h, vfs.AccessRead)
			return err
		}},
		{method: "Lookup", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Lookup(ctx, testCaller, h, "x")
			return err
		}},
		{method: "ReadDir", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, _, err := fs.ReadDir(ctx, testCaller, h, 0, [8]byte{}, 4096, false)
			return err
		}},
		{method: "ReadDir plus", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, _, err := fs.ReadDir(ctx, testCaller, h, 0, [8]byte{}, 4096, true)
			return err
		}},
		{method: "ReadLink", call: func(fs *FS, root, h vfs.Handle) error {
			_, err := fs.ReadLink(ctx, h)
			return err
		}},
		{method: "FSStat", call: func(fs *FS, root, h vfs.Handle) error {
			_, err := fs.FSStat(ctx, h)
			return err
		}},
		{method: "Commit", call: func(fs *FS, root, h vfs.Handle) error {
			return fs.Commit(ctx, h, 0, 0)
		}},
		{method: "Create", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Create(ctx, testCaller, h, "f", vfs.SetAttr{}, true)
			return err
		}},
		{method: "Mkdir", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Mkdir(ctx, testCaller, h, "sub", vfs.SetAttr{})
			return err
		}},
		{method: "Symlink", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Symlink(ctx, testCaller, h, "l", "target", vfs.SetAttr{})
			return err
		}},
		{method: "Remove", call: func(fs *FS, root, h vfs.Handle) error {
			return fs.Remove(ctx, testCaller, h, "x")
		}},
		{method: "Rmdir", call: func(fs *FS, root, h vfs.Handle) error {
			return fs.Rmdir(ctx, testCaller, h, "x")
		}},
		{method: "Rename from the handle", call: func(fs *FS, root, h vfs.Handle) error {
			return fs.Rename(ctx, testCaller, h, "x", root, "y")
		}},
		{method: "Rename into the handle", keep: true, call: func(fs *FS, root, h vfs.Handle) error {
			return fs.Rename(ctx, testCaller, root, "keep.txt", h, "y")
		}},
		{method: "Read", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Read(ctx, testCaller, h, 0, 1)
			return err
		}},
		{method: "Write", call: func(fs *FS, root, h vfs.Handle) error {
			_, _, err := fs.Write(ctx, testCaller, h, 0, []byte{1}, vfs.Unstable)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			fs, _ := rmdirFS(t)
			root := fs.Root()
			h, _, err := fs.Mkdir(ctx, testCaller, root, "gone", vfs.SetAttr{})
			if err != nil {
				t.Fatalf("Mkdir(root, \"gone\"): %v", err)
			}
			if err := fs.Rmdir(ctx, testCaller, root, "gone"); err != nil {
				t.Fatalf("Rmdir(root, \"gone\"): %v", err)
			}
			if tc.keep {
				mustCreate(t, fs, root, "keep.txt")
			}

			if err := tc.call(fs, root, h); !errors.Is(err, vfs.ErrStale) {
				t.Errorf("%s on a removed directory's handle = %v, want ErrStale (vfs.FS contract above Remove; ADR 0017 §1)",
					tc.method, err)
			}

			if tc.keep {
				// ADR 0017 §6 L1: a Rename into the removed directory leaves
				// the name being moved where it was.
				if _, _, err := fs.Lookup(ctx, testCaller, root, "keep.txt"); err != nil {
					t.Errorf("Lookup(root, \"keep.txt\") after a Rename into a removed directory = %v, want it still there (ADR 0017 §6 L1)", err)
				}
			}
		})
	}
}

// TestRenameOntoEmptyDirectoryRemovesIt is ADR 0017 §6 L2: a Rename onto an
// empty directory takes the replaced directory's only name away, so its
// handle goes stale (§1), while the moved directory keeps its handle and its
// FileID (R3), in the same parent and across parents.
func TestRenameOntoEmptyDirectoryRemovesIt(t *testing.T) {
	for _, across := range []bool{false, true} {
		name := "same parent"
		if across {
			name = "across parents"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fs, _ := rmdirFS(t)
			root := fs.Root()
			src, dst := root, root
			srcPath, dstPath := "root/", "root/"
			if across {
				src, _ = rmdirMkdir(t, fs, root, "p")
				dst, _ = rmdirMkdir(t, fs, root, "q")
				srcPath, dstPath = "root/p/", "root/q/"
			}
			aH, aAttr := rmdirMkdir(t, fs, src, "a")
			bH, _ := rmdirMkdir(t, fs, dst, "b")
			op := fmt.Sprintf("Rename %sa onto %sb", srcPath, dstPath)
			if err := fs.Rename(ctx, testCaller, src, "a", dst, "b"); err != nil {
				t.Fatalf("%s: %v", op, err)
			}

			const stale = "want ErrStale (vfs.FS contract above Remove; ADR 0017 §1, §6 L2)"
			if _, err := fs.GetAttr(ctx, bH); !errors.Is(err, vfs.ErrStale) {
				t.Errorf("after %s: GetAttr on b's old handle = %v, %s", op, err, stale)
			}
			if _, _, err := fs.Lookup(ctx, testCaller, bH, "x"); !errors.Is(err, vfs.ErrStale) {
				t.Errorf("after %s: Lookup on b's old handle = %v, %s", op, err, stale)
			}
			if _, _, _, err := fs.ReadDir(ctx, testCaller, bH, 0, [8]byte{}, 4096, false); !errors.Is(err, vfs.ErrStale) {
				t.Errorf("after %s: ReadDir on b's old handle = %v, %s", op, err, stale)
			}
			if _, _, err := fs.Create(ctx, testCaller, bH, "f", vfs.SetAttr{}, true); !errors.Is(err, vfs.ErrStale) {
				t.Errorf("after %s: Create on b's old handle = %v, %s", op, err, stale)
			}

			attr, err := fs.GetAttr(ctx, aH)
			switch {
			case err != nil:
				t.Errorf("after %s: GetAttr on a's handle = %v, want success (R3; vfs.FS contract above Remove; ADR 0017 §6 L2)", op, err)
			case attr.FileID != aAttr.FileID:
				t.Errorf("after %s: GetAttr on a's handle gives FileID %d, want %d from its Mkdir (R3; ADR 0017 §6 L2)", op, attr.FileID, aAttr.FileID)
			}

			_, lAttr, err := fs.Lookup(ctx, testCaller, dst, "b")
			switch {
			case err != nil:
				t.Errorf("after %s: Lookup(%sb) = %v, want the moved directory (ADR 0017 §6 L2)", op, dstPath, err)
			case lAttr.FileID != aAttr.FileID:
				t.Errorf("after %s: Lookup(%sb) gives FileID %d, want a's %d (ADR 0017 §6 L2)", op, dstPath, lAttr.FileID, aAttr.FileID)
			}

			if _, _, err := fs.Lookup(ctx, testCaller, src, "a"); !errors.Is(err, vfs.ErrNoEnt) {
				t.Errorf("after %s: Lookup(%sa) = %v, want ErrNoEnt", op, srcPath, err)
			}
		})
	}
}

// TestDirectoryLinkCounts is ADR 0017 §6 L3: R1 to R3 of §3, checked through
// GetAttr's NLink. It guards against regressions and is expected to pass
// before ADR 0017's change as well as after it.
func TestDirectoryLinkCounts(t *testing.T) {
	ctx := context.Background()
	fs, _ := rmdirFS(t)
	root := fs.Root()
	want := func(h vfs.Handle, what string, n uint32, when string) {
		t.Helper()
		rmdirWantNLink(t, fs, h, what, n, when)
	}

	// R1: a new root is an empty directory.
	want(root, "root", 2, "a new FS (R1)")

	// R2: Mkdir adds 1 to the parent; R1: a new directory has 2.
	a, _ := rmdirMkdir(t, fs, root, "a")
	want(root, "root", 3, "after Mkdir(root, a) (R2)")
	want(a, "root/a", 2, "after Mkdir(root, a) (R1)")

	b, _ := rmdirMkdir(t, fs, a, "b")
	want(a, "root/a", 3, "after Mkdir(a, b) (R2)")
	want(b, "root/a/b", 2, "after Mkdir(a, b) (R1)")

	// R1: a file or symlink has 1; R2: creating one changes no directory's
	// count.
	f := mustCreate(t, fs, a, "f")
	l, _, err := fs.Symlink(ctx, testCaller, a, "l", "target", vfs.SetAttr{})
	if err != nil {
		t.Fatalf("Symlink(a, l): %v", err)
	}
	want(a, "root/a", 3, "after Create(a, f) and Symlink(a, l) (R2)")
	want(f, "root/a/f", 1, "after Create(a, f) (R1)")
	want(l, "root/a/l", 1, "after Symlink(a, l) (R1)")

	// R2: removing a file changes no directory's count.
	if err := fs.Remove(ctx, testCaller, a, "f"); err != nil {
		t.Fatalf("Remove(a, f): %v", err)
	}
	want(a, "root/a", 3, "after Remove(a, f) (R2)")

	// R2: Rmdir takes 1 from the parent.
	if err := fs.Rmdir(ctx, testCaller, a, "b"); err != nil {
		t.Fatalf("Rmdir(a, b): %v", err)
	}
	want(a, "root/a", 2, "after Rmdir(a, b) (R2)")
	want(root, "root", 3, "after Rmdir(a, b) (R2)")

	// R2: renaming a directory between parents takes 1 from the source and
	// adds 1 to the target; R3: the moved directory keeps its own count and
	// its handle. d has a subdirectory so that its own count is not just 2.
	src, _ := rmdirMkdir(t, fs, root, "src")
	dst, _ := rmdirMkdir(t, fs, root, "dst")
	d, dAttr := rmdirMkdir(t, fs, src, "d")
	rmdirMkdir(t, fs, d, "e")
	want(root, "root", 5, "after Mkdir of src and dst (R2)")
	want(src, "root/src", 3, "after Mkdir(src, d) (R2)")
	want(dst, "root/dst", 2, "after Mkdir(root, dst) (R1)")
	want(d, "root/src/d", 3, "after Mkdir(d, e) (R2)")
	if err := fs.Rename(ctx, testCaller, src, "d", dst, "d"); err != nil {
		t.Fatalf("Rename src/d to dst/d: %v", err)
	}
	want(src, "root/src", 2, "after Rename src/d to dst/d (R2: the source loses 1)")
	want(dst, "root/dst", 3, "after Rename src/d to dst/d (R2: the target gains 1)")
	want(d, "the moved directory d", 3, "after Rename src/d to dst/d (R3: it keeps its own count)")
	want(root, "root", 5, "after Rename src/d to dst/d (R2)")
	if attr, err := fs.GetAttr(ctx, d); err != nil {
		t.Errorf("after Rename src/d to dst/d: GetAttr on d's handle = %v, want success (R3)", err)
	} else if attr.FileID != dAttr.FileID {
		t.Errorf("after Rename src/d to dst/d: d's handle gives FileID %d, want %d (R3)", attr.FileID, dAttr.FileID)
	}

	// R2: renaming onto an empty directory across parents. The source loses
	// 1; the target gains 1 for the arrival and loses 1 for the directory it
	// replaces, so it is unchanged.
	s2, _ := rmdirMkdir(t, fs, root, "s2")
	t2, _ := rmdirMkdir(t, fs, root, "t2")
	m, _ := rmdirMkdir(t, fs, s2, "m")
	rmdirMkdir(t, fs, t2, "v")
	want(root, "root", 7, "after Mkdir of s2 and t2 (R2)")
	want(s2, "root/s2", 3, "after Mkdir(s2, m) (R2)")
	want(t2, "root/t2", 3, "after Mkdir(t2, v) (R2)")
	if err := fs.Rename(ctx, testCaller, s2, "m", t2, "v"); err != nil {
		t.Fatalf("Rename s2/m onto the empty t2/v: %v", err)
	}
	want(s2, "root/s2", 2, "after Rename s2/m onto the empty t2/v (R2: the source loses 1)")
	want(t2, "root/t2", 3, "after Rename s2/m onto the empty t2/v (R2: the target is unchanged)")
	want(m, "the moved directory m", 2, "after Rename s2/m onto the empty t2/v (R3)")
	want(root, "root", 7, "after Rename s2/m onto the empty t2/v (R2)")

	// R2: a directory replaced by Rename takes 1 from its parent, here the
	// same parent the moved directory stays in.
	sp, _ := rmdirMkdir(t, fs, root, "sp")
	x, _ := rmdirMkdir(t, fs, sp, "x")
	rmdirMkdir(t, fs, sp, "y")
	want(root, "root", 8, "after Mkdir(root, sp) (R2)")
	want(sp, "root/sp", 4, "after Mkdir of sp/x and sp/y (R2)")
	if err := fs.Rename(ctx, testCaller, sp, "x", sp, "y"); err != nil {
		t.Fatalf("Rename sp/x onto the empty sp/y: %v", err)
	}
	want(sp, "root/sp", 3, "after Rename sp/x onto the empty sp/y (R2: the parent loses 1)")
	want(x, "the moved directory x", 2, "after Rename sp/x onto the empty sp/y (R3)")
	want(root, "root", 8, "after Rename sp/x onto the empty sp/y (R2)")

	// R2: renaming a file between directories changes no count.
	g := mustCreate(t, fs, src, "g")
	want(src, "root/src", 2, "after Create(src, g) (R2)")
	if err := fs.Rename(ctx, testCaller, src, "g", dst, "g"); err != nil {
		t.Fatalf("Rename src/g to dst/g: %v", err)
	}
	want(src, "root/src", 2, "after Rename of the file src/g to dst/g (R2)")
	want(dst, "root/dst", 3, "after Rename of the file src/g to dst/g (R2)")
	want(g, "the moved file g", 1, "after Rename of the file src/g to dst/g (R1, R3)")
}

// TestRemovedDirectoryLeavesNamespace is ADR 0017 §6 L4: FSSTAT's
// TotalFiles - FreeFiles is the number of inodes in the namespace, root
// included (§4), across Mkdir, Rmdir and a rename onto an empty directory,
// and unchanged after Sync and New on the same bucket.
func TestRemovedDirectoryLeavesNamespace(t *testing.T) {
	ctx := context.Background()
	fs, st := rmdirFS(t)
	root := fs.Root()
	check := func(fs *FS, want uint64, when string) {
		t.Helper()
		if got := rmdirUsed(t, fs); got != want {
			t.Errorf("%s: TotalFiles - FreeFiles = %d, want %d (ADR 0017 §4)", when, got, want)
		}
	}

	check(fs, 1, "a new FS")

	rmdirMkdir(t, fs, root, "a")
	check(fs, 2, "after Mkdir(root, a)")
	if err := fs.Rmdir(ctx, testCaller, root, "a"); err != nil {
		t.Fatalf("Rmdir(root, a): %v", err)
	}
	check(fs, 1, "after Rmdir(root, a)")

	rmdirMkdir(t, fs, root, "x")
	rmdirMkdir(t, fs, root, "y")
	check(fs, 3, "after Mkdir of root/x and root/y")
	if err := fs.Rename(ctx, testCaller, root, "x", root, "y"); err != nil {
		t.Fatalf("Rename root/x onto root/y: %v", err)
	}
	check(fs, 2, "after Rename root/x onto the empty root/y")

	if err := fs.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	fs2 := rmdirMount(t, st)
	check(fs2, 2, "after Sync and New on the same bucket")
}

// ---- L5: the model-based test ----

// l5MaxObjects is the model size, root included, at which L5 stops adding
// objects.
const l5MaxObjects = 40

// l5Steps is the number of steps L5 makes for each seed.
const l5Steps = 200

// l5Names is the small set of names L5 draws from, in sorted order.
var l5Names = []string{"a", "b", "c"}

type l5Kind int

const (
	l5Dir l5Kind = iota
	l5File
	l5Link
)

func (k l5Kind) String() string {
	switch k {
	case l5Dir:
		return "directory"
	case l5File:
		return "file"
	default:
		return "symlink"
	}
}

func (k l5Kind) fileType() vfs.FileType {
	switch k {
	case l5Dir:
		return vfs.TypeDir
	case l5File:
		return vfs.TypeReg
	default:
		return vfs.TypeLnk
	}
}

// l5Obj is one object in the model. Since vfs.FS has no hard links, every
// object but the root has exactly one parent and one name. entries is set
// for directories only.
type l5Obj struct {
	kind    l5Kind
	h       vfs.Handle
	id      uint64
	parent  *l5Obj
	name    string
	entries map[string]*l5Obj
}

func (o *l5Obj) path() string {
	if o.parent == nil {
		return "/"
	}
	return l5Join(o.parent, o.name)
}

func l5Join(d *l5Obj, name string) string {
	if d.parent == nil {
		return "/" + name
	}
	return d.path() + "/" + name
}

func (o *l5Obj) subdirs() int {
	n := 0
	for _, e := range o.entries {
		if e.kind == l5Dir {
			n++
		}
	}
	return n
}

// within reports whether o is d or lies in d's subtree.
func (o *l5Obj) within(d *l5Obj) bool {
	for p := o; p != nil; p = p.parent {
		if p == d {
			return true
		}
	}
	return false
}

// l5Gone is the handle of a removed object, with what it was.
type l5Gone struct {
	h    vfs.Handle
	what string
}

// l5Model is the namespace tree as the test expects it. live lists every
// object in the namespace, root included, in a deterministic order.
type l5Model struct {
	root    *l5Obj
	live    []*l5Obj
	removed []l5Gone
}

func l5NewModel(root vfs.Handle, id uint64) *l5Model {
	r := &l5Obj{kind: l5Dir, h: root, id: id, entries: map[string]*l5Obj{}}
	return &l5Model{root: r, live: []*l5Obj{r}}
}

func (m *l5Model) dirs() []*l5Obj {
	var ds []*l5Obj
	for _, o := range m.live {
		if o.kind == l5Dir {
			ds = append(ds, o)
		}
	}
	return ds
}

func (m *l5Model) add(parent *l5Obj, name string, kind l5Kind, h vfs.Handle, id uint64) {
	o := &l5Obj{kind: kind, h: h, id: id, parent: parent, name: name}
	if kind == l5Dir {
		o.entries = map[string]*l5Obj{}
	}
	parent.entries[name] = o
	m.live = append(m.live, o)
}

// drop removes o's only name. o is a non-directory or an empty directory, so
// it takes nothing with it (ADR 0017 §1).
func (m *l5Model) drop(o *l5Obj) {
	m.removed = append(m.removed, l5Gone{h: o.h, what: o.kind.String() + " " + o.path()})
	delete(o.parent.entries, o.name)
	m.live = slices.DeleteFunc(m.live, func(l *l5Obj) bool { return l == o })
}

// move renames o to toDir/toName, removing any entry it replaces
// (RFC 1813 §3.3.14: "the existing target is removed before the rename
// occurs"). o keeps its handle (R3).
func (m *l5Model) move(o, toDir *l5Obj, toName string) {
	if v := toDir.entries[toName]; v != nil {
		m.drop(v)
	}
	delete(o.parent.entries, o.name)
	toDir.entries[toName] = o
	o.parent, o.name = toDir, toName
}

// l5Action is one call L5 can make. For Mkdir, Create, Symlink, Rmdir and
// Remove, dir and name are the call's directory and name; for Rename, src is
// the object moved and dir and name are the target. fail marks a call that
// must fail and change nothing.
type l5Action struct {
	op   string
	fail bool
	dir  *l5Obj
	name string
	src  *l5Obj
}

func (a l5Action) String() string {
	s := a.op + " " + l5Join(a.dir, a.name)
	if a.op == "Rename" {
		s = fmt.Sprintf("Rename %s %s to %s", a.src.kind, a.src.path(), l5Join(a.dir, a.name))
		if e := a.dir.entries[a.name]; e != nil {
			s += fmt.Sprintf(", onto a %s", e.kind)
			if e.kind == l5Dir {
				s += fmt.Sprintf(" with %d entries", len(e.entries))
			}
		}
	}
	if a.fail {
		s += ", which must fail"
	}
	return s
}

// actions lists every call whose outcome the model determines (ADR 0017 §6
// L5), grouped by kind so that a step can pick a kind first and then a call.
// The order is deterministic: it follows m.live and l5Names, never a map.
func (m *l5Model) actions() [][]l5Action {
	var mkdir, create, symlink, rmdir, remove, renameNew, renameOnto []l5Action
	var mkdirExists, symlinkExists, rmdirFull, renameBad []l5Action

	dirs := m.dirs()
	canAdd := len(m.live) < l5MaxObjects
	for _, d := range dirs {
		for _, n := range l5Names {
			e := d.entries[n]
			if e == nil {
				if canAdd {
					mkdir = append(mkdir, l5Action{op: "Mkdir", dir: d, name: n})
					create = append(create, l5Action{op: "Create", dir: d, name: n})
					symlink = append(symlink, l5Action{op: "Symlink", dir: d, name: n})
				}
				continue
			}
			mkdirExists = append(mkdirExists, l5Action{op: "Mkdir", fail: true, dir: d, name: n})
			symlinkExists = append(symlinkExists, l5Action{op: "Symlink", fail: true, dir: d, name: n})
			switch {
			case e.kind != l5Dir:
				remove = append(remove, l5Action{op: "Remove", dir: d, name: n})
			case len(e.entries) == 0:
				rmdir = append(rmdir, l5Action{op: "Rmdir", dir: d, name: n})
			default:
				rmdirFull = append(rmdirFull, l5Action{op: "Rmdir", fail: true, dir: d, name: n})
			}
		}
	}

	for _, s := range m.live {
		if s == m.root {
			continue
		}
		for _, d := range dirs {
			// Never move a directory into itself or its own subtree, whether
			// the call should succeed or fail (ADR 0017 Assumption 9).
			if s.kind == l5Dir && d.within(s) {
				continue
			}
			for _, n := range l5Names {
				e := d.entries[n]
				a := l5Action{op: "Rename", dir: d, name: n, src: s}
				switch {
				case e == nil:
					renameNew = append(renameNew, a)
				case e == s:
					// Never rename an entry onto itself.
				case s.kind != l5Dir && e.kind != l5Dir:
					renameOnto = append(renameOnto, a)
				case s.kind == l5Dir && e.kind == l5Dir && len(e.entries) == 0:
					renameOnto = append(renameOnto, a)
				default:
					// Onto a directory with entries, a directory onto a
					// non-directory, or a non-directory onto a directory.
					a.fail = true
					renameBad = append(renameBad, a)
				}
			}
		}
	}

	return [][]l5Action{
		mkdir, create, symlink, rmdir, remove, renameNew, renameOnto,
		mkdirExists, symlinkExists, rmdirFull, renameBad,
	}
}

// l5Pick picks a kind of call uniformly among those with any candidate, then
// a candidate uniformly. There is always one: the root either lacks a name,
// which Mkdir can add while the model is under its cap, or holds one, which
// Mkdir of an existing name can be tried on; at the cap the root holds at
// least one name.
func l5Pick(rng *rand.Rand, m *l5Model) l5Action {
	var groups [][]l5Action
	for _, g := range m.actions() {
		if len(g) > 0 {
			groups = append(groups, g)
		}
	}
	g := groups[rng.Intn(len(groups))]
	return g[rng.Intn(len(g))]
}

// l5Do makes a's call on fs as testCaller, and returns the new object's
// handle and FileID for a call that makes one.
func l5Do(fs *FS, a l5Action) (vfs.Handle, uint64, error) {
	ctx := context.Background()
	var (
		h    vfs.Handle
		attr vfs.Attr
		err  error
	)
	switch a.op {
	case "Mkdir":
		h, attr, err = fs.Mkdir(ctx, testCaller, a.dir.h, a.name, vfs.SetAttr{})
	case "Create":
		h, attr, err = fs.Create(ctx, testCaller, a.dir.h, a.name, vfs.SetAttr{}, true)
	case "Symlink":
		h, attr, err = fs.Symlink(ctx, testCaller, a.dir.h, a.name, "t", vfs.SetAttr{})
	case "Rmdir":
		err = fs.Rmdir(ctx, testCaller, a.dir.h, a.name)
	case "Remove":
		err = fs.Remove(ctx, testCaller, a.dir.h, a.name)
	case "Rename":
		err = fs.Rename(ctx, testCaller, a.src.parent.h, a.src.name, a.dir.h, a.name)
	default:
		panic("l5: unknown op " + a.op)
	}
	return h, attr.FileID, err
}

// apply updates the model for a call that succeeded.
func (m *l5Model) apply(a l5Action, h vfs.Handle, id uint64) {
	switch a.op {
	case "Mkdir":
		m.add(a.dir, a.name, l5Dir, h, id)
	case "Create":
		m.add(a.dir, a.name, l5File, h, id)
	case "Symlink":
		m.add(a.dir, a.name, l5Link, h, id)
	case "Rmdir", "Remove":
		m.drop(a.dir.entries[a.name])
	case "Rename":
		m.move(a.src, a.dir, a.name)
	}
}

// l5List reads every name in dir, paging to eof and skipping "." and "..",
// and returns them sorted.
func l5List(t *testing.T, fs *FS, dir vfs.Handle, where, path string) []string {
	t.Helper()
	var names []string
	seen := map[string]bool{}
	var cookie uint64
	var verf [8]byte
	for page := 0; ; page++ {
		if page == 100 {
			t.Fatalf("%s: ReadDir(%s) did not reach eof in 100 pages", where, path)
		}
		entries, eof, outVerf, err := fs.ReadDir(context.Background(), testCaller, dir, cookie, verf, 4096, false)
		if err != nil {
			t.Fatalf("%s: ReadDir(%s) at cookie %d: %v", where, path, cookie, err)
		}
		verf = outVerf
		for _, e := range entries {
			cookie = e.Cookie
			if e.Name == "." || e.Name == ".." {
				continue
			}
			if seen[e.Name] {
				t.Fatalf("%s: ReadDir(%s) returned %q twice", where, path, e.Name)
			}
			seen[e.Name] = true
			names = append(names, e.Name)
		}
		if eof {
			break
		}
		if len(entries) == 0 {
			t.Fatalf("%s: ReadDir(%s) made no progress", where, path)
		}
	}
	sort.Strings(names)
	return names
}

// l5Walk walks fs from h, which stands for the model's directory d, with
// ReadDir and Lookup. Every directory must hold exactly the model's names,
// and each Lookup must give that object's FileID and type. It records the
// handle the walk reached each object by.
func l5Walk(t *testing.T, fs *FS, h vfs.Handle, d *l5Obj, where string, out map[*l5Obj]vfs.Handle) {
	t.Helper()
	out[d] = h
	got := l5List(t, fs, h, where, d.path())
	var want []string
	for _, n := range l5Names {
		if d.entries[n] != nil {
			want = append(want, n)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: ReadDir(%s) lists %q, want %q (ADR 0017 §6 L5)", where, d.path(), got, want)
	}
	for _, n := range want {
		o := d.entries[n]
		ch, attr, err := fs.Lookup(context.Background(), testCaller, h, n)
		if err != nil {
			t.Fatalf("%s: Lookup(%s): %v", where, o.path(), err)
		}
		if attr.FileID != o.id {
			t.Fatalf("%s: Lookup(%s) gives FileID %d, want %d (ADR 0017 §6 L5)", where, o.path(), attr.FileID, o.id)
		}
		if attr.Type != o.kind.fileType() {
			t.Fatalf("%s: Lookup(%s) gives type %d, want a %s", where, o.path(), attr.Type, o.kind)
		}
		if o.kind == l5Dir {
			l5Walk(t, fs, ch, o, where, out)
		}
	}
}

// l5Check runs L5's checks of fs against m. sameMount says whether the
// model's handles were issued by fs; after a remount only the walk's handles
// are used, since handles are not pinned across mounts.
func l5Check(t *testing.T, fs *FS, m *l5Model, where string, sameMount bool) {
	t.Helper()
	ctx := context.Background()

	// The tree, read through ReadDir and Lookup, equals the model.
	handles := map[*l5Obj]vfs.Handle{}
	l5Walk(t, fs, fs.Root(), m.root, where, handles)

	// Every count follows ADR 0017 §3 R1: 1 for a file or a symlink, and 2
	// plus its subdirectories for a directory.
	for _, o := range m.live {
		attr, err := fs.GetAttr(ctx, handles[o])
		if err != nil {
			t.Fatalf("%s: GetAttr(%s) on the walk's handle: %v", where, o.path(), err)
		}
		want := uint32(1)
		if o.kind == l5Dir {
			want = 2 + uint32(o.subdirs())
		}
		if attr.NLink != want {
			t.Fatalf("%s: NLink of %s %s = %d, want %d (ADR 0017 §3 R1)", where, o.kind, o.path(), attr.NLink, want)
		}
	}

	// ADR 0017 §4: TotalFiles - FreeFiles is the number of objects, root
	// included.
	s, err := fs.FSStat(ctx, fs.Root())
	if err != nil {
		t.Fatalf("%s: FSStat(root): %v", where, err)
	}
	if used := s.TotalFiles - s.FreeFiles; used != uint64(len(m.live)) {
		t.Fatalf("%s: TotalFiles - FreeFiles = %d, want the model's %d objects (ADR 0017 §4)", where, used, len(m.live))
	}

	if !sameMount {
		return
	}

	// Every removed handle is stale (the vfs.FS contract above Remove;
	// ADR 0017 §1), and every live one answers with its own FileID (R3).
	for _, g := range m.removed {
		if _, err := fs.GetAttr(ctx, g.h); !errors.Is(err, vfs.ErrStale) {
			t.Fatalf("%s: GetAttr on the handle of the removed %s = %v, want ErrStale (vfs.FS contract above Remove; ADR 0017 §1)",
				where, g.what, err)
		}
	}
	for _, o := range m.live {
		attr, err := fs.GetAttr(ctx, o.h)
		if err != nil {
			t.Fatalf("%s: GetAttr on the handle of %s %s = %v, want success (ADR 0017 §3 R3)", where, o.kind, o.path(), err)
		}
		if attr.FileID != o.id {
			t.Fatalf("%s: GetAttr on the handle of %s %s gives FileID %d, want %d (ADR 0017 §3 R3)", where, o.kind, o.path(), attr.FileID, o.id)
		}
	}
}

// TestNamespaceMatchesModel is ADR 0017 §6 L5, the model-based test that
// ADR 0013 §1 gives the namespace tree: seeded random sequences of Mkdir,
// Create, Symlink, Rmdir, Remove and Rename over the names a, b and c, each
// a call whose outcome is determined, checked against a model after every
// step and again after Sync and New on the same bucket.
func TestNamespaceMatchesModel(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			l5Run(t, seed)
		})
	}
}

func l5Run(t *testing.T, seed int64) {
	ctx := context.Background()
	fs, st := rmdirFS(t)
	rootAttr, err := fs.GetAttr(ctx, fs.Root())
	if err != nil {
		t.Fatalf("seed %d: GetAttr(root): %v", seed, err)
	}
	m := l5NewModel(fs.Root(), rootAttr.FileID)
	l5Check(t, fs, m, fmt.Sprintf("seed %d, before step 1", seed), true)

	rng := rand.New(rand.NewSource(seed))
	for step := 1; step <= l5Steps; step++ {
		a := l5Pick(rng, m)
		where := fmt.Sprintf("seed %d, step %d, %s", seed, step, a)
		h, id, err := l5Do(fs, a)
		switch {
		case a.fail && err == nil:
			t.Fatalf("%s: succeeded, want an error and no change (ADR 0017 §6 L5)", where)
		case !a.fail && err != nil:
			t.Fatalf("%s: %v, want success (ADR 0017 §6 L5)", where, err)
		}
		if !a.fail {
			m.apply(a, h, id)
		}
		l5Check(t, fs, m, where, true)
	}

	if err := fs.Sync(ctx); err != nil {
		t.Fatalf("seed %d, after step %d, Sync: %v", seed, l5Steps, err)
	}
	fs2 := rmdirMount(t, st)
	l5Check(t, fs2, m, fmt.Sprintf("seed %d, after step %d, Sync and New on the same bucket", seed, l5Steps), false)
}
