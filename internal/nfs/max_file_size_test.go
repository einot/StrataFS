package nfs

// Clean-room tests for how ADR 0007, "A file is at most 2^20 chunks long, and
// no call takes it further" (docs/adr/0007-maximum-file-size.md), reaches the
// wire. They are the wire half of the tests for #55 and #66, written from that
// ADR's §5 (the wire, and the encodings it condenses from RFC 1813), §3, §4
// and §7, and from internal/vfs/vfs.go, without reading the implementation.
// They cover:
//
//   - FSINFO's maxfilesize is MaxFileSize() (§5);
//   - a SETATTR, WRITE or CREATE that blobfs refuses is answered NFS3ERR_FBIG,
//     27, with its procedure's usual error body, one wcc_data and nothing
//     after it (§5, including "The end of a reply"), and every reply these
//     tests decode ends at its last field;
//   - a WRITE that crosses the limit is answered NFS3_OK with the count it
//     wrote (§5), and a WRITE whose end runs past 2^64 is refused (#66);
//   - a CREATE whose size is above the limit creates nothing, UNCHECKED and
//     GUARDED alike, and one at the limit is unchanged (§5).
//
// They drive the server over a real TCP socket with startServer, as
// integration_test.go does. startServer's FS has 8192-byte chunks, so
// MaxFileSize is 2^20 × 8192 = 2^33 (§1), and a commit interval of an hour, so
// nothing is flushed unless a test asks; nothing here sends COMMIT. As ADR 0007
// §7 asks, the first over-limit request in each test is the cheapest one, a
// size of MaxFileSize + 1 or a WRITE at MaxFileSize, and a wrong answer to it
// fails the test at once, before anything larger is sent. Each call is bounded
// by the 10 s deadline rpcClient.call sets on the connection.
//
// Not covered, on purpose: what blobfs does with each request beyond the size
// a GETATTR reports, which internal/blobfs/max_file_size_test.go covers, and
// EXCLUSIVE creates, which carry no sattr3 (RFC 1813 §3.3.8, as ADR 0007 §5
// condenses it).

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"strata/internal/xdr"
)

// nmfsMax is the MaxFileSize of startServer's FS, whose chunk size is 8192:
// 2^20 × 8192 = 2^33 (ADR 0007 §1).
const nmfsMax uint64 = 8192 << 20

// SETATTR is procedure 2 (RFC 1813 §3.3.2, as ADR 0007 §5 gives it). It is
// spelled out here rather than taken from the implementation.
const nmfsProcSetAttr uint32 = 2

// The createmode3 values an UNCHECKED and a GUARDED CREATE carry (ADR 0007 §5).
const nmfsUnchecked uint32 = 0

const nmfsGuarded uint32 = 1

// stable_how UNSTABLE (ADR 0007 §5).
const nmfsUnstable uint32 = 0

// The nfsstat3 values these tests expect (ADR 0007 §5; RFC 1813 §2.6).
const nmfsOK uint32 = 0

const nmfsNoEnt uint32 = 2

const nmfsFBig uint32 = 27

// nmfsSattrSize encodes a sattr3 that sets the size alone: set_mode3,
// set_uid3 and set_gid3 not set, set_size3 set to size, and set_atime and
// set_mtime DONT_CHANGE.
func nmfsSattrSize(w *xdr.Writer, size uint64) {
	w.Bool(false)
	w.Bool(false)
	w.Bool(false)
	w.Bool(true)
	w.Uint64(size)
	w.Uint32(dontChange)
	w.Uint32(dontChange)
}

// nmfsFattr decodes an fattr3 and returns its size, the uint64 after the five
// uint32s type to gid (ADR 0007 §5).
func nmfsFattr(r *xdr.Reader) uint64 {
	for i := 0; i < 5; i++ {
		r.Uint32()
	}
	size := r.Uint64()
	// used, rdev, fsid and fileid.
	r.Uint64()
	r.Uint32()
	r.Uint32()
	r.Uint64()
	r.Uint64()
	// atime, mtime and ctime, seconds and nanoseconds each.
	for i := 0; i < 6; i++ {
		r.Uint32()
	}
	return size
}

// nmfsWantEnd requires r, a reply whose last field has just been read, to have
// decoded without error and to hold nothing more: Err() is nil and Remaining()
// is 0 (ADR 0007 §5, "The end of a reply"). For a refused SETATTR, WRITE or
// CREATE that is §5's "one wcc_data after its status, and nothing else".
func nmfsWantEnd(t *testing.T, r *xdr.Reader, what string) {
	t.Helper()
	if err := r.Err(); err != nil {
		t.Fatalf("decoding %s: %v (ADR 0007 §5)", what, err)
	}
	if n := r.Remaining(); n != 0 {
		t.Fatalf("%s holds %d more bytes after its last field, want none: a reply carries "+
			"nothing after its last field, and a refused SETATTR, WRITE or CREATE carries one "+
			"wcc_data after its status, and nothing else (ADR 0007 §5)", what, n)
	}
}

// nmfsGetSize sends a GETATTR of fh, which must succeed, and returns the size
// it reports.
func nmfsGetSize(t *testing.T, c *rpcClient, fh []byte, what string) uint64 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) { w.Opaque(fh) })
	if st := r.Uint32(); st != nmfsOK {
		t.Fatalf("GETATTR of %s status = %d, want NFS3_OK", what, st)
	}
	size := nmfsFattr(r)
	if err := r.Err(); err != nil {
		t.Fatalf("decoding the GETATTR reply for %s: %v", what, err)
	}
	return size
}

// nmfsSetSize sends a SETATTR of fh's size alone, with no guard, and returns
// its status. SETATTR3resok and SETATTR3resfail each carry one wcc_data, which
// it decodes (ADR 0007 §5).
func nmfsSetSize(t *testing.T, c *rpcClient, fh []byte, size uint64) uint32 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, nmfsProcSetAttr, func(w *xdr.Writer) {
		w.Opaque(fh)
		nmfsSattrSize(w, size)
		w.Bool(false)
	})
	st := r.Uint32()
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a SETATTR of size %d, status %d, whose "+
		"SETATTR3resok or SETATTR3resfail is one wcc_data", size, st))
	return st
}

// nmfsWriteReply is what nmfsWrite decodes from a WRITE reply.
type nmfsWriteReply struct {
	status    uint32
	count     uint32
	committed uint32
}

// nmfsWrite sends an UNSTABLE WRITE and decodes its reply: WRITE3resfail's
// file_wcc, or WRITE3resok's file_wcc, count, committed and verf (ADR 0007
// §5).
func nmfsWrite(t *testing.T, c *rpcClient, fh []byte, off uint64, data []byte) nmfsWriteReply {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procWrite, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint64(off)
		w.Uint32(uint32(len(data)))
		w.Uint32(nmfsUnstable)
		w.Opaque(data)
	})
	var rep nmfsWriteReply
	rep.status = r.Uint32()
	skipWccData(r)
	if rep.status == nmfsOK {
		rep.count = r.Uint32()
		rep.committed = r.Uint32()
		r.Fixed(8)
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a WRITE of %d bytes at %d, status %d",
		len(data), off, rep.status))
	return rep
}

// nmfsCreateReply is what nmfsCreate decodes from a CREATE reply.
type nmfsCreateReply struct {
	status  uint32
	fh      []byte
	hasAttr bool
	size    uint64
}

// nmfsCreate sends a CREATE of name in dir with the given createmode3, which
// must be UNCHECKED or GUARDED, and a sattr3 that sets the size alone. It
// decodes CREATE3resfail's dir_wcc, or CREATE3resok's obj, obj_attributes and
// dir_wcc (ADR 0007 §5).
func nmfsCreate(t *testing.T, c *rpcClient, dir []byte, name string, mode uint32, size uint64) nmfsCreateReply {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procCreate, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
		w.Uint32(mode)
		nmfsSattrSize(w, size)
	})
	var rep nmfsCreateReply
	rep.status = r.Uint32()
	if rep.status == nmfsOK {
		if r.Bool() {
			rep.fh = r.Opaque()
		}
		if r.Bool() {
			rep.hasAttr = true
			rep.size = nmfsFattr(r)
		}
	}
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a CREATE of %s with size %d, status %d",
		name, size, rep.status))
	return rep
}

// nmfsLookup sends a LOOKUP of name in dir and returns its status and, on
// success, the handle.
func nmfsLookup(t *testing.T, c *rpcClient, dir []byte, name string) (uint32, []byte) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procLookup, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
	})
	st := r.Uint32()
	var fh []byte
	if st == nmfsOK {
		fh = r.Opaque()
	}
	if err := r.Err(); err != nil {
		t.Fatalf("decoding the reply to a LOOKUP of %s, status %d: %v", name, st, err)
	}
	return st, fh
}

// TestNFSMaxFileSizeFSInfo pins ADR 0007 §5, "FSINFO's maxfilesize is
// MaxFileSize()", and §7, "FSINFO's maxfilesize is MaxFileSize()". It decodes
// the whole of FSINFO3resok as §5 gives it: obj_attributes, the seven uint32s
// rtmax to dtpref, maxfilesize, time_delta and properties.
func TestNFSMaxFileSizeFSInfo(t *testing.T) {
	addr, _, fs := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)

	r := c.call(ProgramNFS, VersionNFS, procFSInfo, func(w *xdr.Writer) { w.Opaque(root) })
	if st := r.Uint32(); st != nmfsOK {
		t.Fatalf("FSINFO status = %d, want NFS3_OK", st)
	}
	skipPostOpAttr(r)
	var words [7]uint32
	for i := range words {
		words[i] = r.Uint32()
	}
	maxFileSize := r.Uint64()
	deltaSec, deltaNsec := r.Uint32(), r.Uint32()
	properties := r.Uint32()
	nmfsWantEnd(t, r, "the FSINFO reply, whose last field is FSINFO3resok's properties")
	t.Logf("FSINFO: rtmax %d, rtpref %d, rtmult %d, wtmax %d, wtpref %d, wtmult %d, dtpref %d, "+
		"maxfilesize %d, time_delta %d.%09d, properties %#x", words[0], words[1], words[2], words[3],
		words[4], words[5], words[6], maxFileSize, deltaSec, deltaNsec, properties)

	if want := fs.MaxFileSize(); maxFileSize != want {
		t.Errorf("FSINFO's maxfilesize = %d, want the FS's MaxFileSize(), %d (ADR 0007 §5)",
			maxFileSize, want)
	}
	if maxFileSize != nmfsMax {
		t.Errorf("FSINFO's maxfilesize = %d, want 2^20 × 8192 = 2^33 = %d for startServer's "+
			"8192-byte chunks (ADR 0007 §1, §5)", maxFileSize, nmfsMax)
	}
}

// TestNFSMaxFileSizeSetAttr pins ADR 0007 §5: a SETATTR that blobfs refuses is
// answered NFS3ERR_FBIG, "which each procedure answers with its usual error
// body", SETATTR3resfail's obj_wcc; with §3's rule that a size above
// MaxFileSize is refused and exactly MaxFileSize is not (§1: the limit is
// inclusive).
func TestNFSMaxFileSizeSetAttr(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)
	fh := nfsCreate(t, c, root, "s.bin")

	if st := nmfsSetSize(t, c, fh, nmfsMax+1); st != nmfsFBig {
		t.Fatalf("SETATTR of s.bin to size 2^33 + 1 status = %d, want NFS3ERR_FBIG, %d (ADR 0007 §3, "+
			"§5)", st, nmfsFBig)
	}
	if got := nmfsGetSize(t, c, fh, "s.bin after the refused SETATTR"); got != 0 {
		t.Errorf("s.bin's size after a refused SETATTR to 2^33 + 1 = %d, want 0: none of sa is "+
			"applied (ADR 0007 §3)", got)
	}
	if st := nmfsSetSize(t, c, fh, math.MaxUint64); st != nmfsFBig {
		t.Errorf("SETATTR of s.bin to size 2^64 − 1 status = %d, want NFS3ERR_FBIG, %d (ADR 0007 §3, "+
			"§5)", st, nmfsFBig)
	}
	if got := nmfsGetSize(t, c, fh, "s.bin after the refused SETATTRs"); got != 0 {
		t.Errorf("s.bin's size after refused SETATTRs = %d, want 0 (ADR 0007 §3)", got)
	}
	if st := nmfsSetSize(t, c, fh, nmfsMax); st != nmfsOK {
		t.Errorf("SETATTR of s.bin to size 2^33, exactly the limit, status = %d, want NFS3_OK (ADR "+
			"0007 §1: the limit is inclusive)", st)
	}
	if got := nmfsGetSize(t, c, fh, "s.bin after the SETATTR to the limit"); got != nmfsMax {
		t.Errorf("s.bin's size after a SETATTR to 2^33 = %d, want %d", got, nmfsMax)
	}
}

// TestNFSMaxFileSizeWrite pins ADR 0007 §5: a WRITE that blobfs refuses is
// answered NFS3ERR_FBIG with WRITE3resfail's file_wcc, and "a short WRITE is
// answered NFS3_OK with the count it wrote" (§7). A WRITE at 2^33 and one of 20
// bytes at 2^64 − 10, whose end runs past 2^64 (#66), are refused and leave
// the file empty; one of 20 bytes at 2^33 − 10 writes 10, makes the file 2^33
// long, and a READ at 2^33 − 10 returns those 10 bytes with eof. Nothing
// sends COMMIT.
func TestNFSMaxFileSizeWrite(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)
	fh := nfsCreate(t, c, root, "w.bin")

	if rep := nmfsWrite(t, c, fh, nmfsMax, []byte{1}); rep.status != nmfsFBig {
		t.Fatalf("WRITE of 1 byte at 2^33 status = %d, want NFS3ERR_FBIG, %d (ADR 0007 §3, §5)",
			rep.status, nmfsFBig)
	}
	if rep := nmfsWrite(t, c, fh, 1<<64-10, bytes.Repeat([]byte{2}, 20)); rep.status != nmfsFBig {
		t.Errorf("WRITE of 20 bytes at 2^64 − 10, whose end runs past 2^64, status = %d, want "+
			"NFS3ERR_FBIG, %d (ADR 0007 §3, §4, §5; #66)", rep.status, nmfsFBig)
	}
	if got := nmfsGetSize(t, c, fh, "w.bin after the refused WRITEs"); got != 0 {
		t.Errorf("w.bin's size after refused WRITEs = %d, want 0: a refused write changes nothing "+
			"(ADR 0007 §3)", got)
	}

	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(0x40 + i)
	}
	rep := nmfsWrite(t, c, fh, nmfsMax-10, data)
	if rep.status != nmfsOK || rep.count != 10 {
		t.Errorf("WRITE of 20 bytes at 2^33 − 10 = status %d, count %d, want NFS3_OK with count 10: "+
			"a WRITE that crosses the limit is short (ADR 0007 §3, §5, §7)", rep.status, rep.count)
	}
	if got := nmfsGetSize(t, c, fh, "w.bin after the short WRITE"); got != nmfsMax {
		t.Errorf("w.bin's size after the short WRITE = %d, want 2^33 = %d (ADR 0007 §7)", got, nmfsMax)
	}

	r := c.call(ProgramNFS, VersionNFS, procRead, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint64(nmfsMax - 10)
		w.Uint32(20)
	})
	if st := r.Uint32(); st != nmfsOK {
		t.Fatalf("READ of 20 bytes at 2^33 − 10 status = %d, want NFS3_OK", st)
	}
	skipPostOpAttr(r)
	count := r.Uint32()
	eof := r.Bool()
	got := r.Opaque()
	if err := r.Err(); err != nil {
		t.Fatalf("decoding READ3resok: %v", err)
	}
	if count != 10 || !eof || !bytes.Equal(got, data[:10]) {
		t.Errorf("READ of 20 bytes at 2^33 − 10 = count %d, eof %v, data %x, want count 10, eof "+
			"true, data %x: the first 10 bytes of the short WRITE, which end the file", count, eof,
			got, data[:10])
	}
}

// TestNFSMaxFileSizeCreate pins ADR 0007 §5: a CREATE that blobfs refuses is
// answered NFS3ERR_FBIG with CREATE3resfail's dir_wcc, and "A CREATE with mode
// UNCHECKED whose size is at or below the limit is unchanged"; with §3,
// "Create fails with ErrFBig before it creates or changes anything, whether or
// not name exists". An UNCHECKED and a GUARDED create of a new name with size
// 2^33 + 1 leave no name behind; an UNCHECKED create of an existing empty
// file with size 2^64 − 1 leaves it empty; and an UNCHECKED create with size
// 2^33 makes a file of that size.
func TestNFSMaxFileSizeCreate(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)

	if rep := nmfsCreate(t, c, root, "u.bin", nmfsUnchecked, nmfsMax+1); rep.status != nmfsFBig {
		t.Fatalf("UNCHECKED CREATE of u.bin with size 2^33 + 1 status = %d, want NFS3ERR_FBIG, %d "+
			"(ADR 0007 §3, §5)", rep.status, nmfsFBig)
	}
	if st, _ := nmfsLookup(t, c, root, "u.bin"); st != nmfsNoEnt {
		t.Errorf("LOOKUP of u.bin after its refused CREATE status = %d, want NFS3ERR_NOENT, %d: a "+
			"refused Create creates nothing (ADR 0007 §3, §4)", st, nmfsNoEnt)
	}

	if rep := nmfsCreate(t, c, root, "g.bin", nmfsGuarded, nmfsMax+1); rep.status != nmfsFBig {
		t.Errorf("GUARDED CREATE of g.bin with size 2^33 + 1 status = %d, want NFS3ERR_FBIG, %d "+
			"(ADR 0007 §3, §5)", rep.status, nmfsFBig)
	}
	if st, _ := nmfsLookup(t, c, root, "g.bin"); st != nmfsNoEnt {
		t.Errorf("LOOKUP of g.bin after its refused CREATE status = %d, want NFS3ERR_NOENT, %d "+
			"(ADR 0007 §3, §4)", st, nmfsNoEnt)
	}

	e := nfsCreate(t, c, root, "e.bin")
	if rep := nmfsCreate(t, c, root, "e.bin", nmfsUnchecked, math.MaxUint64); rep.status != nmfsFBig {
		t.Errorf("UNCHECKED CREATE of the existing e.bin with size 2^64 − 1 status = %d, want "+
			"NFS3ERR_FBIG, %d: Create fails whether or not name exists (ADR 0007 §3, §5)",
			rep.status, nmfsFBig)
	}
	if got := nmfsGetSize(t, c, e, "e.bin after the refused CREATE over it"); got != 0 {
		t.Errorf("e.bin's size after a refused UNCHECKED CREATE over it = %d, want 0 (ADR 0007 §3)", got)
	}

	rep := nmfsCreate(t, c, root, "m.bin", nmfsUnchecked, nmfsMax)
	if rep.status != nmfsOK {
		t.Fatalf("UNCHECKED CREATE of m.bin with size 2^33, exactly the limit, status = %d, want "+
			"NFS3_OK (ADR 0007 §1, §5)", rep.status)
	}
	if rep.hasAttr && rep.size != nmfsMax {
		t.Errorf("UNCHECKED CREATE of m.bin with size 2^33: obj_attributes report size %d, want "+
			"%d: the server applies an UNCHECKED create's size once Create has returned (ADR 0007 "+
			"§5)", rep.size, nmfsMax)
	}
	fh := rep.fh
	if fh == nil {
		var st uint32
		if st, fh = nmfsLookup(t, c, root, "m.bin"); st != nmfsOK {
			t.Fatalf("LOOKUP of m.bin after its CREATE status = %d, want NFS3_OK", st)
		}
	}
	if got := nmfsGetSize(t, c, fh, "m.bin after its UNCHECKED CREATE with size 2^33"); got != nmfsMax {
		t.Errorf("m.bin's size after an UNCHECKED CREATE with size 2^33 = %d, want %d (ADR 0007 §5)",
			got, nmfsMax)
	}
}
