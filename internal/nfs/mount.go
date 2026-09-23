package nfs

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"strata/internal/sunrpc"
	"strata/internal/vfs"
	"strata/internal/xdr"
)

// MOUNT version 3 procedure numbers.
const (
	mountProcNull = iota
	mountProcMnt
	mountProcDump
	mountProcUmnt
	mountProcUmntAll
	mountProcExport
	mountProcCount
)

// mountstat3 values.
const (
	mnt3OK          = 0
	mnt3ErrNoEnt    = 2
	mnt3ErrAccess   = 13
	mnt3ErrNotDir   = 20
	mnt3ErrInval    = 22
	mnt3ErrNameLong = 63
	mnt3ErrNotSupp  = 10004
)

// Auth flavors we advertise as acceptable.
const (
	authFlavorNone = 0
	authFlavorSys  = 1
)

// MountServer implements the MOUNT version 3 program, which exists only to
// hand a client the root file handle for an exported path.
type MountServer struct {
	fs     vfs.FS
	export string
	log    *slog.Logger

	// mounts records active clients so DUMP can answer and so the operator can
	// see who is attached. It is informational: NFS has no real session state.
	mu     sync.Mutex
	mounts map[string]string // client address -> exported path
}

func NewMountServer(fs vfs.FS, export string, log *slog.Logger) *MountServer {
	if export == "" {
		export = "/"
	}
	return &MountServer{
		fs:     fs,
		export: export,
		log:    log,
		mounts: make(map[string]string),
	}
}

func (m *MountServer) HasProc(proc uint32) bool { return proc < mountProcCount }

// Idempotent is true for every procedure: the mount table is informational,
// so replaying MNT, UMNT or UMNTALL changes nothing a client can observe.
func (m *MountServer) Idempotent(proc uint32) bool { return true }

func (m *MountServer) Call(ctx context.Context, proc uint32, cred sunrpc.Cred, r *xdr.Reader, w *xdr.Writer) error {
	switch proc {
	case mountProcNull:
		return nil

	case mountProcMnt:
		path := r.String()
		if r.Err() != nil {
			return sunrpc.ErrGarbageArgs
		}
		if !m.accepts(path) {
			m.log.Warn("mount refused for unknown export", "requested", path, "export", m.export)
			w.Uint32(mnt3ErrNoEnt)
			return nil
		}
		m.mu.Lock()
		m.mounts[cred.Machine] = path
		m.mu.Unlock()

		m.log.Info("mounted", "path", path, "client", cred.Machine, "uid", cred.UID)
		w.Uint32(mnt3OK)
		w.Opaque(m.fs.Root())
		// Acceptable auth flavors, most preferred first.
		w.Uint32(2)
		w.Uint32(authFlavorSys)
		w.Uint32(authFlavorNone)
		return nil

	case mountProcUmnt:
		path := r.String()
		if r.Err() != nil {
			return sunrpc.ErrGarbageArgs
		}
		m.mu.Lock()
		delete(m.mounts, cred.Machine)
		m.mu.Unlock()
		m.log.Info("unmounted", "path", path, "client", cred.Machine)
		return nil

	case mountProcUmntAll:
		m.mu.Lock()
		m.mounts = make(map[string]string)
		m.mu.Unlock()
		return nil

	case mountProcDump:
		m.mu.Lock()
		defer m.mu.Unlock()
		for client, path := range m.mounts {
			w.Bool(true)
			w.String(client)
			w.String(path)
		}
		w.Bool(false)
		return nil

	case mountProcExport:
		// One export, offered to everyone that can reach the socket. Access
		// control is the loopback binding, not an export list.
		w.Bool(true)
		w.String(m.export)
		w.Bool(false) // no group restrictions
		w.Bool(false) // end of export list
		return nil
	}
	return sunrpc.ErrGarbageArgs
}

// accepts reports whether a requested mount path matches the export. Clients
// vary in whether they send a trailing slash.
func (m *MountServer) accepts(path string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(s, "/")
		if s == "" {
			s = "/"
		}
		return s
	}
	return norm(path) == norm(m.export)
}
