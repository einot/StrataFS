package nfs

// Tests for the idempotency classification the RPC layer asks this package
// for, written against docs/adr/0002-duplicate-request-cache.md §3 (the
// Handler method) and §4 (the classification itself).

import (
	"io"
	"log/slog"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testIdempotent calls fn(proc) and fails rather than letting a panic escape.
// §3 makes Idempotent a question about a procedure number, and §1 says
// internal/nfs contributes exactly one fact to the cache: whether a given
// procedure number is idempotent. Answering it must therefore not depend on
// the filesystem behind the server.
func testIdempotent(t *testing.T, what string, proc uint32, fn func(uint32) bool) (got bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			got = false
			t.Errorf("%s.Idempotent(%d) panicked with %v: ADR 0002 §1 says this package contributes exactly one fact to the cache — whether a procedure number is idempotent — so the answer is a property of the procedure number alone and must not reach for the filesystem", what, proc, r)
		}
	}()
	return fn(proc)
}

// TestServerIdempotentNeedsNoFilesystem pins that the classification is
// answerable on a server with no filesystem behind it (ADR 0002 §1, §3).
func TestServerIdempotentNeedsNoFilesystem(t *testing.T) {
	s := NewServer(nil, [8]byte{}, testLogger())
	for proc := uint32(0); proc <= 21; proc++ {
		testIdempotent(t, "nfs.Server", proc, s.Idempotent)
	}
}

// TestMountServerIdempotentNeedsNoFilesystem is the same requirement for the
// MOUNT program (ADR 0002 §1, §3).
func TestMountServerIdempotentNeedsNoFilesystem(t *testing.T) {
	s := NewMountServer(nil, "/", testLogger())
	for proc := uint32(0); proc <= 5; proc++ {
		testIdempotent(t, "nfs.MountServer", proc, s.Idempotent)
	}
}

// TestServerIdempotentClassification covers every NFSv3 procedure number
// against ADR 0002 §4's list. The "why" column records the reason §4 gives,
// because a wrong answer here is either a needless cache entry or a wrongly
// re-executed operation.
func TestServerIdempotentClassification(t *testing.T) {
	tests := []struct {
		proc uint32
		name string
		want bool
		why  string
	}{
		{0, "NULL", true, "§4 lists NULL as idempotent"},
		{1, "GETATTR", true, "§4 lists GETATTR as idempotent"},
		{2, "SETATTR", false, "§4: SETATTR is cached because a guarded SETATTR fails with NFS3ERR_NOT_SYNC on replay, and procedure granularity is all the RPC layer has"},
		{3, "LOOKUP", true, "§4 lists LOOKUP as idempotent"},
		{4, "ACCESS", true, "§4 lists ACCESS as idempotent"},
		{5, "READLINK", true, "§4 lists READLINK as idempotent"},
		{6, "READ", true, "§4 lists READ as idempotent"},
		{7, "WRITE", true, "§4: WRITE is idempotent by offset — a replayed write puts the same bytes at the same place — and it is the hottest mutating path, so caching it would be pure overhead"},
		{8, "CREATE", false, "§4 and the Context table: re-executing a GUARDED CREATE that succeeded returns NFS3ERR_EXIST"},
		{9, "MKDIR", false, "§4 and the Context table: re-executing MKDIR returns NFS3ERR_EXIST"},
		{10, "SYMLINK", false, "§4 and the Context table: re-executing SYMLINK returns NFS3ERR_EXIST"},
		{11, "MKNOD", false, "§4: MKNOD answers NFS3ERR_NOTSUPP today, but it is a namespace mutation in RFC 1813's model and the classification should not have to be revisited if it is implemented"},
		{12, "REMOVE", false, "§4 and the Context table: re-executing REMOVE returns NFS3ERR_NOENT for a delete that succeeded"},
		{13, "RMDIR", false, "§4 and the Context table: re-executing RMDIR returns NFS3ERR_NOENT for a delete that succeeded"},
		{14, "RENAME", false, "§4 and the Context table: re-executing RENAME returns NFS3ERR_NOENT, or worse if a new file took the old name"},
		{15, "LINK", false, "§4: LINK answers NFS3ERR_NOTSUPP today, but it is a namespace mutation in RFC 1813's model"},
		{16, "READDIR", true, "§4 lists READDIR as idempotent"},
		{17, "READDIRPLUS", true, "§4 lists READDIRPLUS as idempotent"},
		{18, "FSSTAT", true, "§4 lists FSSTAT as idempotent"},
		{19, "FSINFO", true, "§4 lists FSINFO as idempotent"},
		{20, "PATHCONF", true, "§4 lists PATHCONF as idempotent"},
		{21, "COMMIT", true, "§4: COMMIT is idempotent; committing twice is a no-op and returns the same verifier"},
	}

	s := NewServer(nil, [8]byte{}, testLogger())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := testIdempotent(t, "nfs.Server", tc.proc, s.Idempotent)
			if got != tc.want {
				t.Errorf("Idempotent(%d) for %s = %t, want %t: %s", tc.proc, tc.name, got, tc.want, tc.why)
			}
		})
	}
}

// TestServerIdempotentDefaultsToFalse covers §4's last bullet: "the default
// for an unlisted procedure is not idempotent, so a procedure added later is
// protected until someone deliberately says otherwise."
func TestServerIdempotentDefaultsToFalse(t *testing.T) {
	s := NewServer(nil, [8]byte{}, testLogger())
	for _, proc := range []uint32{22, 99, 1 << 16, ^uint32(0)} {
		if got := testIdempotent(t, "nfs.Server", proc, s.Idempotent); got {
			t.Errorf("Idempotent(%d) = true for a procedure number NFSv3 does not define, want false: ADR 0002 §4 makes the default for an unlisted procedure not idempotent, so a procedure added later is protected until someone deliberately says otherwise", proc)
		}
	}
}

// TestMountServerIdempotentIsAlwaysTrue covers §4's last paragraph and
// Assumption 5: MOUNT keeps only an informational client list, so replaying
// MNT, UMNT or UMNTALL changes nothing a client can observe. The asymmetry
// with nfs.Server is deliberate.
func TestMountServerIdempotentIsAlwaysTrue(t *testing.T) {
	names := map[uint32]string{
		0: "NULL",
		1: "MNT",
		2: "DUMP",
		3: "UMNT",
		4: "UMNTALL",
		5: "EXPORT",
	}

	s := NewMountServer(nil, "/", testLogger())
	for _, proc := range []uint32{0, 1, 2, 3, 4, 5, 6} {
		name, ok := names[proc]
		if !ok {
			name = "an undefined procedure"
		}
		if got := testIdempotent(t, "nfs.MountServer", proc, s.Idempotent); !got {
			t.Errorf("MountServer.Idempotent(%d) for %s = false, want true: ADR 0002 §4 says MOUNT is idempotent for every procedure, because the mount table is informational (Assumption 5)", proc, name)
		}
	}
}
