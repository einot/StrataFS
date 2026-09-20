package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestS3Conformance exercises the S3 client against a real S3-compatible
// server. It is the authoritative check on the SigV4 signer: a server rejects
// any request whose signature it cannot reproduce, so a passing run means the
// canonical request, the header canonicalization and the HMAC chain all match
// an independent implementation.
//
// It runs only when STRATA_S3_ENDPOINT is set, for example against MinIO:
//
//	STRATA_S3_ENDPOINT=http://127.0.0.1:9000 \
//	STRATA_S3_BUCKET=strata-test \
//	AWS_ACCESS_KEY_ID=minioadmin \
//	AWS_SECRET_ACCESS_KEY=minioadmin \
//	go test ./internal/store -run Conformance -v
func TestS3Conformance(t *testing.T) {
	endpoint := os.Getenv("STRATA_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set STRATA_S3_ENDPOINT to run S3 conformance tests")
	}
	bucket := os.Getenv("STRATA_S3_BUCKET")
	if bucket == "" {
		bucket = "strata-test"
	}

	s, err := NewS3(S3Config{
		Endpoint: endpoint,
		Bucket:   bucket,
		Region:   envOr("AWS_REGION", "us-east-1"),
		Creds: Credentials{
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		},
		PathStyle: os.Getenv("STRATA_S3_VIRTUAL_HOST") == "",
		Timeout:   20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prefix := fmt.Sprintf("conformance/%d/", time.Now().UnixNano())

	t.Run("PutGet", func(t *testing.T) {
		key := prefix + "hello.txt"
		want := []byte("signed, sealed, delivered")
		if err := s.Put(ctx, key, want); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Get(ctx, key)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("round trip got %q, want %q", got, want)
		}
	})

	t.Run("KeysWithAwkwardCharacters", func(t *testing.T) {
		// Percent-encoding in the canonical URI is the most common source of
		// signature mismatches, so exercise characters that need escaping.
		for _, name := range []string{
			"spaces and more.txt",
			"plus+sign.txt",
			"tilde~dash-dot.txt",
			"unicode-ünïcödé.txt",
			"nested/deep/path.bin",
			"parens(1).txt",
		} {
			key := prefix + name
			body := []byte("content of " + name)
			if err := s.Put(ctx, key, body); err != nil {
				t.Errorf("put %q: %v", name, err)
				continue
			}
			got, err := s.Get(ctx, key)
			if err != nil {
				t.Errorf("get %q: %v", name, err)
				continue
			}
			if !bytes.Equal(got, body) {
				t.Errorf("round trip %q mismatched", name)
			}
		}
	})

	t.Run("Range", func(t *testing.T) {
		key := prefix + "range.bin"
		body := []byte("0123456789abcdefghij")
		if err := s.Put(ctx, key, body); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRange(ctx, key, 5, 5)
		if err != nil {
			t.Fatalf("get range: %v", err)
		}
		if string(got) != "56789" {
			t.Errorf("range read = %q, want %q", got, "56789")
		}
	})

	t.Run("HeadAndMissing", func(t *testing.T) {
		key := prefix + "head.txt"
		body := []byte("abcdef")
		if err := s.Put(ctx, key, body); err != nil {
			t.Fatal(err)
		}
		info, err := s.Head(ctx, key)
		if err != nil {
			t.Fatalf("head: %v", err)
		}
		if info.Size != int64(len(body)) {
			t.Errorf("head size = %d, want %d", info.Size, len(body))
		}
		if info.ETag == "" {
			t.Error("head returned an empty ETag")
		}
		if _, err := s.Head(ctx, prefix+"definitely-not-here"); !errors.Is(err, ErrNotFound) {
			t.Errorf("head on missing key = %v, want ErrNotFound", err)
		}
		if _, err := s.Get(ctx, prefix+"definitely-not-here"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get on missing key = %v, want ErrNotFound", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		lp := prefix + "list/"
		for _, n := range []string{"a", "b", "c"} {
			if err := s.Put(ctx, lp+n, []byte(n)); err != nil {
				t.Fatal(err)
			}
		}
		objs, err := s.List(ctx, lp, "", 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(objs) != 3 {
			t.Errorf("list returned %d objects, want 3", len(objs))
		}
	})

	t.Run("ConditionalWrite", func(t *testing.T) {
		// This is what protects the root pointer from two writers.
		key := prefix + "cas.json"

		et, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":1}`), "")
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrPrecondition) {
			t.Skipf("server does not support conditional writes: %v", err)
		}
		if err != nil {
			t.Fatalf("initial conditional put: %v", err)
		}

		// Creating again with If-None-Match must now fail.
		if _, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":2}`), ""); !errors.Is(err, ErrPrecondition) {
			t.Errorf("create-if-absent on existing key = %v, want ErrPrecondition", err)
		}

		// Updating with the right ETag must succeed.
		if et == "" {
			info, herr := s.Head(ctx, key)
			if herr != nil {
				t.Fatal(herr)
			}
			et = info.ETag
		}
		next, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":2}`), et)
		if err != nil {
			t.Fatalf("conditional update with correct ETag: %v", err)
		}

		// Updating with a stale ETag must fail: this is the divergence check.
		if _, err := s.PutIfMatch(ctx, key, []byte(`{"epoch":3}`), et); !errors.Is(err, ErrPrecondition) {
			t.Errorf("conditional update with stale ETag = %v, want ErrPrecondition", err)
		}
		_ = next
	})

	t.Run("Delete", func(t *testing.T) {
		key := prefix + "doomed.txt"
		if err := s.Put(ctx, key, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("get after delete = %v, want ErrNotFound", err)
		}
		// Deleting a missing key is not an error.
		if err := s.Delete(ctx, key); err != nil {
			t.Errorf("delete of missing key = %v, want nil", err)
		}
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
