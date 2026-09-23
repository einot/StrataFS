package nfs

import (
	"context"
	"log/slog"

	"strata/internal/sunrpc"
	"strata/internal/vfs"
	"strata/internal/xdr"
)

// Transfer sizes advertised to clients. They bound both the reply size and the
// amount of data buffered per request.
const (
	maxReadSize  = 1 << 20
	maxWriteSize = 1 << 20
	dirPrefSize  = 64 << 10
)

// Server implements the NFSv3 program over a vfs.FS.
type Server struct {
	fs   vfs.FS
	log  *slog.Logger
	verf [8]byte
}

// NewServer builds an NFSv3 program handler. verf is the write verifier
// reported in WRITE and COMMIT replies; it must change when the server
// restarts so clients know to replay unstable writes.
func NewServer(fs vfs.FS, verf [8]byte, log *slog.Logger) *Server {
	return &Server{fs: fs, log: log, verf: verf}
}

func (s *Server) HasProc(proc uint32) bool { return proc < procCount }

// Idempotent is an allow-list so that a procedure added later is protected by
// the duplicate request cache until someone deliberately says otherwise.
// SETATTR is excluded because its guarded form fails with NFS3ERR_NOT_SYNC on
// replay, and the RPC layer cannot see the guard. WRITE and COMMIT are
// included: a replayed write puts the same bytes at the same offset, and the
// write verifier is stable for the life of the process.
func (s *Server) Idempotent(proc uint32) bool {
	switch proc {
	case procNull, procGetAttr, procLookup, procAccess, procReadlink,
		procRead, procWrite, procReaddir, procReaddirPlus, procFSStat,
		procFSInfo, procPathconf, procCommit:
		return true
	default:
		return false
	}
}

func callerFrom(c sunrpc.Cred) vfs.Caller {
	return vfs.Caller{UID: c.UID, GID: c.GID, GIDs: c.GIDs}
}

// attrOf fetches attributes for a post_op_attr, returning nil if unavailable.
// A missing post_op_attr is always legal, so errors here are not fatal.
func (s *Server) attrOf(ctx context.Context, h vfs.Handle) *vfs.Attr {
	if h == nil {
		return nil
	}
	a, err := s.fs.GetAttr(ctx, h)
	if err != nil {
		return nil
	}
	return &a
}

// Call dispatches one NFSv3 procedure.
func (s *Server) Call(ctx context.Context, proc uint32, cred sunrpc.Cred, r *xdr.Reader, w *xdr.Writer) error {
	c := callerFrom(cred)
	switch proc {
	case procNull:
		return nil
	case procGetAttr:
		return s.getattr(ctx, c, r, w)
	case procSetAttr:
		return s.setattr(ctx, c, r, w)
	case procLookup:
		return s.lookup(ctx, c, r, w)
	case procAccess:
		return s.access(ctx, c, r, w)
	case procReadlink:
		return s.readlink(ctx, c, r, w)
	case procRead:
		return s.read(ctx, c, r, w)
	case procWrite:
		return s.write(ctx, c, r, w)
	case procCreate:
		return s.create(ctx, c, r, w)
	case procMkdir:
		return s.mkdir(ctx, c, r, w)
	case procSymlink:
		return s.symlink(ctx, c, r, w)
	case procMknod:
		return s.unsupportedCreate(ctx, r, w)
	case procRemove:
		return s.remove(ctx, c, r, w, false)
	case procRmdir:
		return s.remove(ctx, c, r, w, true)
	case procRename:
		return s.rename(ctx, c, r, w)
	case procLink:
		return s.link(ctx, r, w)
	case procReaddir:
		return s.readdir(ctx, c, r, w, false)
	case procReaddirPlus:
		return s.readdir(ctx, c, r, w, true)
	case procFSStat:
		return s.fsstat(ctx, r, w)
	case procFSInfo:
		return s.fsinfo(ctx, r, w)
	case procPathconf:
		return s.pathconf(ctx, r, w)
	case procCommit:
		return s.commit(ctx, r, w)
	}
	return sunrpc.ErrGarbageArgs
}

func (s *Server) getattr(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	a, err := s.fs.GetAttr(ctx, h)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putFattr(w, a)
	return nil
}

func (s *Server) setattr(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	sa, err := getSattr(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	// guard: the client may ask us to apply the change only if ctime is
	// unchanged, which makes a retried SETATTR safe.
	var guard *vfs.Attr
	if r.Bool() {
		guardCtime := getTime(r)
		cur, gerr := s.fs.GetAttr(ctx, h)
		if gerr == nil {
			guard = &cur
			if !cur.CTime.Equal(guardCtime) {
				w.Uint32(uint32(vfs.ErrNotSync))
				putWccData(w, wccFrom(guard), guard)
				return nil
			}
		}
	}
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}

	before := wccFrom(s.attrOf(ctx, h))
	a, err := s.fs.SetAttr(ctx, c, h, sa)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, h))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putWccData(w, before, &a)
	return nil
}

func (s *Server) lookup(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	h, a, err := s.fs.Lookup(ctx, c, args.dir, args.name)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, s.attrOf(ctx, args.dir))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putHandle(w, h)
	putPostOpAttr(w, &a)
	putPostOpAttr(w, s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) access(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	want := r.Uint32()
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}
	granted, err := s.fs.Access(ctx, c, h, want)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, nil)
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.Uint32(granted)
	return nil
}

func (s *Server) readlink(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	target, err := s.fs.ReadLink(ctx, h)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, s.attrOf(ctx, h))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.String(target)
	return nil
}

func (s *Server) read(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	off := r.Uint64()
	count := r.Uint32()
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}
	if count > maxReadSize {
		count = maxReadSize
	}

	data, eof, err := s.fs.Read(ctx, c, h, off, count)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, s.attrOf(ctx, h))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.Uint32(uint32(len(data)))
	w.Bool(eof)
	w.Opaque(data)
	return nil
}

func (s *Server) write(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	off := r.Uint64()
	count := r.Uint32()
	how := vfs.Stability(r.Uint32())
	data := r.Opaque()
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}
	// The count field and the actual data length must agree; trust the data.
	if uint32(len(data)) < count {
		count = uint32(len(data))
	}
	if count > maxWriteSize {
		count = maxWriteSize
	}
	data = data[:count]

	before := wccFrom(s.attrOf(ctx, h))
	n, committed, err := s.fs.Write(ctx, c, h, off, data, how)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, h))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putWccData(w, before, s.attrOf(ctx, h))
	w.Uint32(n)
	w.Uint32(uint32(committed))
	w.Fixed(s.verf[:])
	return nil
}

// createhow3 modes.
const (
	createUnchecked = 0
	createGuarded   = 1
	createExclusive = 2
)

func (s *Server) create(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	mode := r.Uint32()
	var sa vfs.SetAttr
	excl := false
	switch mode {
	case createUnchecked, createGuarded:
		sa, err = getSattr(r)
		if err != nil {
			return sunrpc.ErrGarbageArgs
		}
		excl = mode == createGuarded
	case createExclusive:
		// The client supplies a verifier so a retried create is idempotent.
		// Without persistent verifier storage the closest correct behaviour is
		// a guarded create, which at least never silently truncates.
		r.Fixed(8)
		excl = true
	default:
		return sunrpc.ErrGarbageArgs
	}
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}

	before := wccFrom(s.attrOf(ctx, args.dir))
	h, a, err := s.fs.Create(ctx, c, args.dir, args.name, sa, excl)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, args.dir))
		return nil
	}
	// An UNCHECKED create on an existing file carries a size in its attributes
	// to truncate it; apply it now that the file exists.
	if mode == createUnchecked && sa.Size != nil {
		if na, serr := s.fs.SetAttr(ctx, c, h, vfs.SetAttr{Size: sa.Size}); serr == nil {
			a = na
		}
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpHandle(w, h)
	putPostOpAttr(w, &a)
	putWccData(w, before, s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) mkdir(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	sa, err := getSattr(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	before := wccFrom(s.attrOf(ctx, args.dir))
	h, a, err := s.fs.Mkdir(ctx, c, args.dir, args.name, sa)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, args.dir))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpHandle(w, h)
	putPostOpAttr(w, &a)
	putWccData(w, before, s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) symlink(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	sa, err := getSattr(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	target := r.String()
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}

	before := wccFrom(s.attrOf(ctx, args.dir))
	h, a, err := s.fs.Symlink(ctx, c, args.dir, args.name, target, sa)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, args.dir))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpHandle(w, h)
	putPostOpAttr(w, &a)
	putWccData(w, before, s.attrOf(ctx, args.dir))
	return nil
}

// unsupportedCreate answers MKNOD. Device and socket nodes have no meaning in
// an object store, so the honest answer is NOTSUPP rather than a fake success.
func (s *Server) unsupportedCreate(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	w.Uint32(uint32(vfs.ErrNotSupp))
	putWccData(w, wccFrom(s.attrOf(ctx, args.dir)), s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) remove(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer, isDir bool) error {
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	before := wccFrom(s.attrOf(ctx, args.dir))
	if isDir {
		err = s.fs.Rmdir(ctx, c, args.dir, args.name)
	} else {
		err = s.fs.Remove(ctx, c, args.dir, args.name)
	}
	w.Uint32(uint32(statusOf(err)))
	putWccData(w, before, s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) rename(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer) error {
	from, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	to, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	beforeFrom := wccFrom(s.attrOf(ctx, from.dir))
	beforeTo := wccFrom(s.attrOf(ctx, to.dir))

	err = s.fs.Rename(ctx, c, from.dir, from.name, to.dir, to.name)
	w.Uint32(uint32(statusOf(err)))
	putWccData(w, beforeFrom, s.attrOf(ctx, from.dir))
	putWccData(w, beforeTo, s.attrOf(ctx, to.dir))
	return nil
}

// link answers LINK. Hard links would require reference counting across the
// namespace snapshot; the filesystem does not implement them, and FSINFO
// advertises their absence so clients do not try.
func (s *Server) link(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	args, err := getDirOpArgs(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	w.Uint32(uint32(vfs.ErrNotSupp))
	putPostOpAttr(w, s.attrOf(ctx, h))
	putWccData(w, wccFrom(s.attrOf(ctx, args.dir)), s.attrOf(ctx, args.dir))
	return nil
}

func (s *Server) readdir(ctx context.Context, c vfs.Caller, r *xdr.Reader, w *xdr.Writer, plus bool) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	cookie := r.Uint64()
	verfBytes := r.Fixed(8)
	var verf [8]byte
	copy(verf[:], verfBytes)

	var max uint32
	if plus {
		r.Uint32() // dircount: the name-only budget, subsumed by maxcount here
		max = r.Uint32()
	} else {
		max = r.Uint32()
	}
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}
	if max > maxReadSize {
		max = maxReadSize
	}

	entries, eof, outVerf, err := s.fs.ReadDir(ctx, c, h, cookie, verf, max, plus)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, s.attrOf(ctx, h))
		return nil
	}

	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.Fixed(outVerf[:])
	for _, e := range entries {
		w.Bool(true) // another entry follows
		w.Uint64(e.FileID)
		w.String(e.Name)
		w.Uint64(e.Cookie)
		if plus {
			putPostOpAttr(w, e.Attr)
			putPostOpHandle(w, e.Handle)
		}
	}
	w.Bool(false) // end of list
	w.Bool(eof)
	return nil
}

func (s *Server) fsstat(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	st, err := s.fs.FSStat(ctx, h)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putPostOpAttr(w, nil)
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.Uint64(st.TotalBytes)
	w.Uint64(st.FreeBytes)
	w.Uint64(st.AvailBytes)
	w.Uint64(st.TotalFiles)
	w.Uint64(st.FreeFiles)
	w.Uint64(st.FreeFiles)
	w.Uint32(0) // invarsec: we make no promise that these numbers are stable
	return nil
}

// FSINFO property bits.
const (
	fsfLink        = 0x0001
	fsfSymlink     = 0x0002
	fsfHomogeneous = 0x0008
	fsfCanSetTime  = 0x0010
)

func (s *Server) fsinfo(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))

	w.Uint32(maxReadSize)  // rtmax
	w.Uint32(maxReadSize)  // rtpref
	w.Uint32(4096)         // rtmult: preferred read size multiple
	w.Uint32(maxWriteSize) // wtmax
	w.Uint32(maxWriteSize) // wtpref
	w.Uint32(4096)         // wtmult
	w.Uint32(dirPrefSize)  // dtpref: preferred READDIR size
	w.Uint64(1 << 62)      // maxfilesize
	w.Uint32(1)            // time_delta seconds
	w.Uint32(0)            // time_delta nanoseconds
	// Hard links are deliberately absent from the property set.
	w.Uint32(fsfSymlink | fsfHomogeneous | fsfCanSetTime)
	return nil
}

func (s *Server) pathconf(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	w.Uint32(uint32(vfs.OK))
	putPostOpAttr(w, s.attrOf(ctx, h))
	w.Uint32(1)   // linkmax: no hard links beyond the entry itself
	w.Uint32(255) // name_max
	w.Bool(true)  // no_trunc: over-long names are rejected, not silently cut
	w.Bool(true)  // chown_restricted
	w.Bool(false) // case_insensitive
	w.Bool(true)  // case_preserving
	return nil
}

func (s *Server) commit(ctx context.Context, r *xdr.Reader, w *xdr.Writer) error {
	h, err := getHandle(r)
	if err != nil {
		return sunrpc.ErrGarbageArgs
	}
	off := r.Uint64()
	count := r.Uint32()
	if r.Err() != nil {
		return sunrpc.ErrGarbageArgs
	}

	before := wccFrom(s.attrOf(ctx, h))
	err = s.fs.Commit(ctx, h, off, count)
	if err != nil {
		w.Uint32(uint32(statusOf(err)))
		putWccData(w, before, s.attrOf(ctx, h))
		return nil
	}
	w.Uint32(uint32(vfs.OK))
	putWccData(w, before, s.attrOf(ctx, h))
	w.Fixed(s.verf[:])
	return nil
}
