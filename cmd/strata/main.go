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
	"runtime"
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

	// Judged before the store is opened, so that a refused -chunk-size,
	// -commit-interval, -uid or -gid creates and contacts nothing, -check and
	// -read-only included, even for a bucket whose filesystem the owner flags
	// would never touch (ADR 0008 §5, ADR 0009 §3, ADR 0010 §5).
	chunkSize, err := chunkSizeBytes(*chunkKiB)
	if err != nil {
		return err
	}
	commitEvery, err := commitInterval(*interval)
	if err != nil {
		return err
	}
	uid, gid := currentIDs()
	uid, err = ownerID("-uid", *uidFlag, uid)
	if err != nil {
		return err
	}
	gid, err = ownerID("-gid", *gidFlag, gid)
	if err != nil {
		return err
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs, err := blobfs.New(ctx, blobfs.Config{
		Store:             bucket,
		ChunkSize:         chunkSize,
		CacheBytes:        cacheBytes(*cacheMiB),
		CommitInterval:    commitEvery,
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

// chunkSizeBytes converts -chunk-size, in KiB, to Config.ChunkSize.
//
// A count no uint32 can hold in bytes, or a negative one, is refused rather
// than clamped or wrapped: a new filesystem keeps its chunk size for good, so
// any substitute would be one the operator did not ask for. Zero passes
// through for New to take as the default, and 1 to 3 KiB are left to New,
// which owns the least size it accepts. Converting before comparing keeps the
// constant representable where int is 32 bits (ADR 0008 §2, §5).
func chunkSizeBytes(kib int) (uint32, error) {
	if kib < 0 || int64(kib) > math.MaxUint32>>10 {
		return 0, fmt.Errorf("-chunk-size %d is out of range: it cannot be negative or more than 4194303 KiB", kib)
	}
	return uint32(kib) * 1024, nil
}

// cacheBytes converts -cache, in MiB, to Config.CacheBytes.
//
// Zero or less is 0, which Config takes as the default, so no negative count
// wraps into a positive bound. A count too large to express in bytes is a
// bound no host reaches, so it maps to math.MaxInt64 rather than wrapping;
// Config has no other spelling for an unbounded cache. Converting before
// comparing keeps the constant representable where int is 32 bits (ADR 0008
// §3).
func cacheBytes(mib int) int64 {
	if mib <= 0 {
		return 0
	}
	if int64(mib) > math.MaxInt64>>20 {
		return math.MaxInt64
	}
	return int64(mib) << 20
}

// commitInterval checks -commit-interval for Config.CommitInterval.
//
// A negative interval is refused rather than clamped or taken as the default:
// no ticker runs at one, and any substitute would be a schedule the operator
// did not ask for. Zero passes through for New to take as the default, and a
// positive interval is used as given, however small, with no floor (ADR 0009
// §3, §4).
func commitInterval(d time.Duration) (time.Duration, error) {
	if d < 0 {
		return 0, fmt.Errorf("-commit-interval %v is negative: give a positive duration, or 0 for the default", d)
	}
	return d, nil
}

// ownerID resolves -uid or -gid to Config.OwnerUID or Config.OwnerGID.
//
// An id is refused rather than wrapped, since 2^32 wrapped is root, and
// 4294967295 is refused too: it is (uid_t)-1, which nothing can give a file
// and a Linux client rejects in any attributes. -1, the default, is the id
// strata runs as, and is refused where the platform reports none, which
// currentIDs spells as 4294967295. Every other negative value has no meaning
// and is refused. Converting before comparing keeps the bound representable
// where int is 32 bits (ADR 0010 §2, §3).
func ownerID(flagName string, given int, current uint32) (uint32, error) {
	// Typed, so that passing it to Errorf does not overflow a 32-bit int.
	const maxID int64 = math.MaxUint32 - 1
	switch g := int64(given); {
	case g >= 0 && g <= maxID:
		return uint32(g), nil
	case g == -1 && current != math.MaxUint32:
		return current, nil
	case g == -1:
		return 0, fmt.Errorf("%s -1 asks for the %s strata runs as, which this platform does not report: give %s an id from 0 to %d", flagName, strings.TrimPrefix(flagName, "-"), flagName, maxID)
	default:
		return 0, fmt.Errorf("%s %d is out of range: give an id from 0 to %d, or -1 for the id strata runs as", flagName, g, maxID)
	}
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

// currentIDs returns the real uid and gid that strata runs as.
//
// It asks the kernel and nothing else: os/user fails under a uid the user
// database does not list, or without cgo and $USER or $HOME, and every such
// failure used to make root the owner. getuid and getgid cannot fail on Linux
// or macOS. The conversion to uint32 restores an id of 2^31 or more where int
// is 32 bits, and turns Windows's -1 into 4294967295, which ownerID reads as
// no id (ADR 0010 §4).
func currentIDs() (uid, gid uint32) {
	return uint32(os.Getuid()), uint32(os.Getgid())
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
