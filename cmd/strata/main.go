// Command strata mounts an S3-backed filesystem by serving NFSv3 on loopback.
//
// The whole filesystem lives in one bucket. See the blobfs package for the
// object layout and the commit protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strata/internal/blobfs"
	"strata/internal/nfs"
	"strata/internal/store"
	"strata/internal/sunrpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "strata: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		bucketSpec = flag.String("bucket", "", "bucket holding the filesystem: s3://BUCKET or a local directory path")
		endpoint   = flag.String("endpoint", "", "S3 endpoint URL (default: AWS for the given region)")
		region     = flag.String("region", "us-east-1", "S3 region")
		pathStyle  = flag.Bool("path-style", false, "use path-style S3 addressing (required by MinIO and most S3-compatible servers)")

		readOnly = flag.Bool("read-only", false, "refuse all modifications")
		check    = flag.Bool("check", false, "probe the endpoint for the S3 behaviour strata needs, then exit")

		listen    = flag.String("listen", "127.0.0.1:20490", "address to serve NFS on")
		export    = flag.String("export", "/", "exported path clients mount")
		chunkKiB  = flag.Int("chunk-size", 1024, "chunk size in KiB")
		cacheMiB  = flag.Int("cache", 256, "chunk cache size in MiB")
		interval  = flag.Duration("commit-interval", 5*time.Second, "how often to commit the namespace")
		retention = flag.Int("snapshot-retention", 10, "superseded namespace snapshots to keep (-1 keeps all)")
		maxDirty  = flag.Int("max-dirty", 256, "maximum buffered write data in MiB before writes are held for a commit (0 or less: no limit)")

		verifyChunks = flag.Bool("verify-chunks", true, "verify each chunk fetched from the bucket against the hash that names it")

		uidFlag = flag.Int("uid", -1, "owner uid for a new filesystem (default: current user)")
		gidFlag = flag.Int("gid", -1, "owner gid for a new filesystem (default: current user)")

		verbose = flag.Bool("v", false, "verbose logging")
	)
	flag.Usage = usage
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *bucketSpec == "" {
		flag.Usage()
		return errors.New("-bucket is required")
	}

	creds := store.Credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}

	bucket, err := openStore(*bucketSpec, *endpoint, *region, creds, *pathStyle)
	if err != nil {
		return fmt.Errorf("bucket: %w", err)
	}

	if *check {
		return runCheck(context.Background(), bucket, os.Stdout)
	}

	uid, gid := currentIDs()
	if *uidFlag >= 0 {
		uid = uint32(*uidFlag)
	}
	if *gidFlag >= 0 {
		gid = uint32(*gidFlag)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs, err := blobfs.New(ctx, blobfs.Config{
		Store:             bucket,
		ChunkSize:         uint32(*chunkKiB) * 1024,
		CacheBytes:        int64(*cacheMiB) << 20,
		CommitInterval:    *interval,
		OwnerUID:          uid,
		OwnerGID:          gid,
		ReadOnly:          *readOnly,
		SnapshotRetention: *retention,
		MaxDirtyBytes:     maxDirtyBytes(*maxDirty),

		SkipChunkVerification: !*verifyChunks,
		Log:                   log,
	})
	if err != nil {
		return err
	}

	rpc := sunrpc.NewServer(log)
	rpc.Register(nfs.ProgramNFS, nfs.VersionNFS, nfs.NewServer(fs, fs.WriteVerf(), log))
	rpc.Register(nfs.ProgramMount, nfs.VersionMount, nfs.NewMountServer(fs, *export, log))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *listen, err)
	}
	defer ln.Close()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	printMountInstructions(host, port, *export, *readOnly)

	go fs.Run(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- rpc.Serve(ctx, ln) }()

	select {
	case <-ctx.Done():
		log.Info("shutting down, committing")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
	}

	// The signal context is already cancelled, so the final commit needs its own.
	shutCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := fs.Sync(shutCtx); err != nil {
		return fmt.Errorf("final commit: %w", err)
	}
	epoch, inodes, hits, misses, _ := fs.Stats()
	log.Info("clean shutdown", "epoch", epoch, "inodes", inodes, "cache_hits", hits, "cache_misses", misses)
	return nil
}

// maxDirtyBytes converts -max-dirty, in MiB, to Config.MaxDirtyBytes.
//
// Zero or less means no limit, which Config spells as a negative value, since
// its zero selects the default. A count too large to express in bytes is a
// limit no workload could reach, so it maps to no limit too rather than
// wrapping into a small one. Converting before comparing keeps the constant
// representable where int is 32 bits (ADR 0003 §1, Assumptions 16 and 17).
func maxDirtyBytes(mib int) int64 {
	if mib <= 0 || int64(mib) > math.MaxInt64>>20 {
		return -1
	}
	return int64(mib) << 20
}

// openStore turns a bucket spec into a Store. A spec starting with s3:// is an
// S3 bucket; anything else is a local directory.
func openStore(spec, endpoint, region string, creds store.Credentials, pathStyle bool) (store.Store, error) {
	if rest, ok := strings.CutPrefix(spec, "s3://"); ok {
		bucket, _, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return nil, fmt.Errorf("no bucket name in %q", spec)
		}
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, errors.New("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set to use an S3 bucket")
		}
		return store.NewS3(store.S3Config{
			Endpoint:  endpoint,
			Bucket:    bucket,
			Region:    region,
			Creds:     creds,
			PathStyle: pathStyle,
		})
	}
	return store.NewLocal(strings.TrimPrefix(spec, "file://"))
}

func currentIDs() (uint32, uint32) {
	u, err := user.Current()
	if err != nil {
		return 0, 0
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uint32(uid), uint32(gid)
}

func printMountInstructions(host, port, export string, ro bool) {
	opts := []string{"vers=3", "tcp", "port=" + port, "mountport=" + port, "noresvport", "hard"}
	// We serve NFS but not the separate NLM lock protocol, so the client must
	// be told to keep locking local rather than trying to reach a lock manager
	// that is not there. The option is spelled differently on each platform.
	if runtime.GOOS == "darwin" {
		opts = append(opts, "nolocks", "locallocks")
	} else {
		opts = append(opts, "nolock")
	}
	if ro {
		opts = append(opts, "ro")
	}
	fmt.Fprintf(os.Stderr, `
strata is serving NFSv3 on %s:%s

  mkdir -p /tmp/strata-mnt
  sudo mount -t nfs -o %s %s:%s /tmp/strata-mnt

To unmount:

  sudo umount /tmp/strata-mnt

Locking is kept local: strata serves NFS but not the separate NLM protocol.

`, host, port, strings.Join(opts, ","), host, export)
}

func usage() {
	fmt.Fprint(os.Stderr, `strata - an S3-backed filesystem served over loopback NFSv3

The whole filesystem lives in one bucket. File contents are stored as chunks
named by the SHA-256 of their bytes, so identical data is stored once and a
modified chunk lands at a new key instead of overwriting the old one. Exactly
one object is ever mutated: a small root pointer, swapped with a conditional
write, which advances the filesystem atomically from one consistent state to
the next.

Usage:
  strata -bucket <bucket> [options]

Examples:
  # A local directory, no credentials needed.
  strata -bucket /tmp/strata

  # AWS S3.
  export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
  strata -bucket s3://my-filesystem -region eu-west-1

  # An on-premise S3-compatible cluster.
  strata -bucket s3://my-filesystem -endpoint https://objectscale.example.net -path-style

  # Check that an endpoint supports what strata needs, without mounting.
  strata -bucket s3://my-filesystem -endpoint https://objectscale.example.net -path-style -check

Options:
`)
	flag.PrintDefaults()
}
