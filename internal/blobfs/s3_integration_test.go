package blobfs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// TestFilesystemOverS3 runs the whole filesystem against a live S3-compatible
// server: write, commit, remount from the buckets alone, and verify that the
// data bucket still contains nothing but content-addressed chunks.
//
// Runs only when STRATA_S3_ENDPOINT is set. See the store package's
// conformance test for the environment variables.
func TestFilesystemOverS3(t *testing.T) {
	endpoint := os.Getenv("STRATA_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATA_S3_ENDPOINT to run the S3 filesystem test")
	}

	mk := func(bucket string) store.Store {
		s, err := store.NewS3(store.S3Config{
			Endpoint: endpoint,
			Bucket:   bucket,
			Region:   "us-east-1",
			Creds: store.Credentials{
				AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
				SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			},
			PathStyle: true,
			Timeout:   20 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// This test EMPTIES both buckets before running, so the defaults are names
	// nothing would be mounted on, and any override must opt in explicitly.
	// Pointing it at a bucket a live mount is using destroys that filesystem.
	dataBucket := envDefault("STRATA_S3_DATA_BUCKET", "strata-test-data")
	metaBucket := envDefault("STRATA_S3_META_BUCKET", "strata-test-meta")
	if !strings.Contains(dataBucket, "test") || !strings.Contains(metaBucket, "test") {
		t.Fatalf("refusing to wipe buckets %q and %q: this test empties both, so "+
			"their names must contain \"test\". Set STRATA_S3_DATA_BUCKET and "+
			"STRATA_S3_META_BUCKET to dedicated buckets.", dataBucket, metaBucket)
	}
	data, meta := mk(dataBucket), mk(metaBucket)
	ctx := context.Background()

	// Each run starts from a clean namespace so the test is repeatable.
	clearBucket(t, ctx, meta)
	clearBucket(t, ctx, data)

	fs, err := New(ctx, Config{
		Data: data, Meta: meta,
		ChunkSize: 64 * 1024,
		OwnerUID:  501, OwnerGID: 20,
		Log: quietLog(),
	})
	if err != nil {
		t.Fatalf("create filesystem on S3: %v", err)
	}

	// A tree with nested directories and a multi-chunk file.
	docs, _, err := fs.Mkdir(ctx, testCaller, fs.Root(), "documents", vfs.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	// Pseudorandom so that every chunk is distinct; a repeating payload would
	// dedup down to a single chunk and make the count below meaningless.
	payload := pseudorandom(640*1024, 1)
	h := mustCreate(t, fs, docs, "large.txt")
	mustWrite(t, fs, h, 0, payload)

	// A second file with identical content, to confirm dedup over real S3.
	h2 := mustCreate(t, fs, docs, "duplicate.txt")
	mustWrite(t, fs, h2, 0, payload)

	if err := fs.Sync(ctx); err != nil {
		t.Fatalf("commit to S3: %v", err)
	}

	// Remount using nothing but the two buckets.
	fs2, err := New(ctx, Config{Data: mk(dataBucket), Meta: mk(metaBucket), Log: quietLog()})
	if err != nil {
		t.Fatalf("remount from S3: %v", err)
	}
	docs2, _, err := fs2.Lookup(ctx, testCaller, fs2.Root(), "documents")
	if err != nil {
		t.Fatalf("lookup directory after remount: %v", err)
	}
	f2, attr, err := fs2.Lookup(ctx, testCaller, docs2, "large.txt")
	if err != nil {
		t.Fatalf("lookup file after remount: %v", err)
	}
	if attr.Size != uint64(len(payload)) {
		t.Errorf("size after remount = %d, want %d", attr.Size, len(payload))
	}
	if got := readAll(t, fs2, f2, len(payload)); !bytes.Equal(got, payload) {
		t.Errorf("contents differ after remount from S3 (%d vs %d bytes)", len(got), len(payload))
	}

	// Dedup: two identical files must share their chunks.
	objs, err := data.List(ctx, chunkPrefix, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	wantChunks := (len(payload) + 64*1024 - 1) / (64 * 1024)
	if len(objs) != wantChunks {
		t.Errorf("data bucket holds %d chunks, want %d (two identical files should share)", len(objs), wantChunks)
	}

	// Writing a third copy of the same bytes must add no objects at all.
	h3 := mustCreate(t, fs, docs, "triplicate.txt")
	mustWrite(t, fs, h3, 0, payload)
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := data.List(ctx, chunkPrefix, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(objs) {
		t.Errorf("a third identical file added %d chunks, want 0", len(after)-len(objs))
	}

	// The data bucket must still be free of namespace detail.
	all, err := data.List(ctx, "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		if !strings.HasPrefix(o.Key, chunkPrefix) || len(strings.TrimPrefix(o.Key, chunkPrefix)) != 64 {
			t.Errorf("unexpected object in data bucket: %q", o.Key)
		}
	}
	t.Logf("wrote %d KB as %d chunks across two files; namespace in a separate bucket",
		len(payload)/1024, len(objs))
}

// pseudorandom builds deterministic incompressible bytes.
func pseudorandom(n int, seed uint64) []byte {
	out := make([]byte, n)
	x := seed*6364136223846793005 + 1442695040888963407
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}

func clearBucket(t *testing.T, ctx context.Context, s store.Store) {
	t.Helper()
	for {
		objs, err := s.List(ctx, "", "", 1000)
		if err != nil {
			t.Fatalf("list for cleanup: %v", err)
		}
		if len(objs) == 0 {
			return
		}
		for _, o := range objs {
			if err := s.Delete(ctx, o.Key); err != nil {
				t.Fatalf("delete %s: %v", o.Key, err)
			}
		}
		if len(objs) < 1000 {
			return
		}
	}
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var _ = fmt.Sprintf
