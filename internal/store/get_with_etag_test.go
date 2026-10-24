package store

// Tests of Store.GetWithETag, written without reading the implementation, from
// ADR 0020 (docs/adr/0020-a-mount-loads-the-root-pointer-and-its-etag-in-one-read.md)
// sections 2 and 6, with the safety rules ADR 0011 section 7 and ADR 0012
// section 9 give, as ADR 0020 section 6 restates them.
//
// ADR 0020 section 2: GetWithETag returns the whole object together with the
// ETag of the body it returns, both taken from one response. A missing key is
// ErrNotFound. ReadOnly overrides nothing new, so the method is the embedded
// store's.
//
// Covered, one test per row of section 6's "Tests of the new method":
//   - S1, TestLocalGetWithETagReturnsBodyAndItsETag: on a *Local, the ETag
//     GetWithETag returns is Head's and the lowercase hexadecimal MD5 of the
//     body, a PutIfMatch against it succeeds, and a key never put is
//     ErrNotFound.
//   - S2, TestS3GetWithETagTakesETagFromGetResponse: on an *S3 over an
//     httptest.Server whose GET and HEAD answer different ETags, the ETag comes
//     from the GET, with the quotes and a W/ prefix stripped; a 404 is
//     ErrNotFound; Get still returns the body.
//   - S3, TestReadOnlyPassesGetWithETagThrough: ReadOnly answers GetWithETag
//     as the store it wraps does, and still refuses Put.
//
// Not covered, on purpose: retries (the fake server never answers 5xx or 429,
// which the client would retry after a wait, ADR 0020 section 6), logs, and
// what only review can show (that S3.GetWithETag takes the ETag from the
// response that carried the body, that Local.GetWithETag hashes the bytes it
// returns, and that ReadOnly overrides nothing new).
//
// Safety: every bucket is under t.TempDir(), every fake server is an
// httptest.Server closed by cleanup, every call runs under a context that ends
// after gweBound and is cancelled by cleanup, and there is no sleep.

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// gweBound is how long any one test here lets its store calls take in all.
const gweBound = 10 * time.Second

// gweCtx returns a context that ends after gweBound and is cancelled when the
// test ends.
func gweCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), gweBound)
	t.Cleanup(cancel)
	return ctx
}

// gweMD5 is *Local's ETag rule (ADR 0020 section 6, first fact): the
// lowercase hexadecimal MD5 of the body.
func gweMD5(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// gweLocal returns a *Local over a fresh directory under t.TempDir().
func gweLocal(t *testing.T) *Local {
	t.Helper()
	l, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return l
}

// S1 (ADR 0020 section 6).
func TestLocalGetWithETagReturnsBodyAndItsETag(t *testing.T) {
	ctx := gweCtx(t)
	l := gweLocal(t)

	const key = "root"
	body := []byte(`{"version":1,"epoch":1,"snapshot":"snapshots/first"}`)
	if err := l.Put(ctx, key, body); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}

	data, etag, err := l.GetWithETag(ctx, key)
	if err != nil {
		t.Fatalf("GetWithETag(%q) after Put: %v", key, err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("GetWithETag(%q) body = %q, want the body put, %q", key, data, body)
	}
	info, err := l.Head(ctx, key)
	if err != nil {
		t.Fatalf("Head(%q): %v", key, err)
	}
	if etag != info.ETag {
		t.Errorf("GetWithETag(%q) ETag = %q, want Head's ETag %q", key, etag, info.ETag)
	}
	if want := gweMD5(body); etag != want {
		t.Errorf("GetWithETag(%q) ETag = %q, want the lowercase hexadecimal MD5 of the body, %q", key, etag, want)
	}

	newBody := []byte(`{"version":1,"epoch":2,"snapshot":"snapshots/second"}`)
	if _, err := l.PutIfMatch(ctx, key, newBody, etag); err != nil {
		t.Fatalf("PutIfMatch(%q) with the ETag GetWithETag returned, %q: %v", key, etag, err)
	}
	data, etag, err = l.GetWithETag(ctx, key)
	if err != nil {
		t.Fatalf("GetWithETag(%q) after PutIfMatch: %v", key, err)
	}
	if !bytes.Equal(data, newBody) {
		t.Errorf("GetWithETag(%q) after PutIfMatch: body = %q, want the new body, %q", key, data, newBody)
	}
	if want := gweMD5(newBody); etag != want {
		t.Errorf("GetWithETag(%q) after PutIfMatch: ETag = %q, want the MD5 of the new body, %q", key, etag, want)
	}

	const missing = "never-put"
	if _, _, err := l.GetWithETag(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetWithETag(%q) of a key never put: err = %v, want ErrNotFound", missing, err)
	}
}

// gweRootBody is the body the fake S3 server answers GET /b/root with.
var gweRootBody = []byte(`{"version":1,"epoch":7,"snapshot":"snapshots/from-get"}`)

// gweS3 starts an httptest.Server standing for S3, as ADR 0020 section 6's S2
// specifies, closed by cleanup, and returns an *S3 over it with bucket "b" and
// path-style addressing. The server answers:
//   - GET /b/root: 200, header ETag: getETag, body gweRootBody;
//   - HEAD /b/root: 200, Content-Length of gweRootBody, ETag: "etag-from-head";
//   - GET /b/missing: 404, body <Error><Code>NoSuchKey</Code></Error>;
//   - anything else: 400, so that nothing is answered 5xx or 429, which the
//     client would retry after a wait.
func gweS3(t *testing.T, getETag string) *S3 {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/b/root":
			w.Header().Set("ETag", getETag)
			w.Header().Set("Content-Length", strconv.Itoa(len(gweRootBody)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(gweRootBody)
		case r.Method == http.MethodHead && r.URL.Path == "/b/root":
			w.Header().Set("ETag", `"etag-from-head"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(gweRootBody)))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/b/missing":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
		default:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<Error><Code>InvalidRequest</Code></Error>`))
		}
	}))
	t.Cleanup(srv.Close)

	s, err := NewS3(S3Config{
		Endpoint:  srv.URL,
		Bucket:    "b",
		Region:    "us-east-1",
		Creds:     Credentials{AccessKeyID: "k", SecretAccessKey: "s"},
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return s
}

// S2 (ADR 0020 section 6).
func TestS3GetWithETagTakesETagFromGetResponse(t *testing.T) {
	t.Run("strong", func(t *testing.T) {
		ctx := gweCtx(t)
		s := gweS3(t, `"etag-from-get"`)

		data, etag, err := s.GetWithETag(ctx, "root")
		if err != nil {
			t.Fatalf(`GetWithETag("root"): %v`, err)
		}
		if !bytes.Equal(data, gweRootBody) {
			t.Errorf(`GetWithETag("root") body = %q, want the GET response's body, %q`, data, gweRootBody)
		}
		if etag != "etag-from-get" {
			t.Errorf(`GetWithETag("root") ETag = %q, want "etag-from-get", the GET response's ETag with the quotes stripped (HEAD answers "etag-from-head")`, etag)
		}

		if _, _, err := s.GetWithETag(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf(`GetWithETag("missing") on a 404: err = %v, want ErrNotFound`, err)
		}

		got, err := s.Get(ctx, "root")
		if err != nil {
			t.Fatalf(`Get("root"): %v`, err)
		}
		if !bytes.Equal(got, gweRootBody) {
			t.Errorf(`Get("root") = %q, want the body, %q`, got, gweRootBody)
		}
	})

	t.Run("weak", func(t *testing.T) {
		ctx := gweCtx(t)
		s := gweS3(t, `W/"weak"`)

		data, etag, err := s.GetWithETag(ctx, "root")
		if err != nil {
			t.Fatalf(`GetWithETag("root"): %v`, err)
		}
		if !bytes.Equal(data, gweRootBody) {
			t.Errorf(`GetWithETag("root") body = %q, want the GET response's body, %q`, data, gweRootBody)
		}
		if etag != "weak" {
			t.Errorf(`GetWithETag("root") with ETag: W/"weak": ETag = %q, want "weak"`, etag)
		}
	})
}

// S3 (ADR 0020 section 6).
func TestReadOnlyPassesGetWithETagThrough(t *testing.T) {
	ctx := gweCtx(t)
	local := gweLocal(t)

	const key = "root"
	body := []byte(`{"version":1,"epoch":3,"snapshot":"snapshots/ro"}`)
	if err := local.Put(ctx, key, body); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	ro := ReadOnly{Store: local}

	t.Run("present", func(t *testing.T) {
		wantData, wantETag, wantErr := local.GetWithETag(ctx, key)
		if wantErr != nil {
			t.Fatalf("local.GetWithETag(%q): %v", key, wantErr)
		}
		data, etag, err := ro.GetWithETag(ctx, key)
		if err != nil {
			t.Fatalf("ReadOnly.GetWithETag(%q): %v, want nil as the wrapped store answers", key, err)
		}
		if !bytes.Equal(data, wantData) {
			t.Errorf("ReadOnly.GetWithETag(%q) body = %q, want the wrapped store's, %q", key, data, wantData)
		}
		if etag != wantETag {
			t.Errorf("ReadOnly.GetWithETag(%q) ETag = %q, want the wrapped store's, %q", key, etag, wantETag)
		}
	})

	t.Run("missing", func(t *testing.T) {
		const missing = "never-put"
		wantData, wantETag, wantErr := local.GetWithETag(ctx, missing)
		if !errors.Is(wantErr, ErrNotFound) {
			t.Fatalf("local.GetWithETag(%q) of a key never put: err = %v, want ErrNotFound", missing, wantErr)
		}
		data, etag, err := ro.GetWithETag(ctx, missing)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ReadOnly.GetWithETag(%q): err = %v, want ErrNotFound as the wrapped store answers", missing, err)
		}
		if !bytes.Equal(data, wantData) {
			t.Errorf("ReadOnly.GetWithETag(%q) body = %q, want the wrapped store's, %q", missing, data, wantData)
		}
		if etag != wantETag {
			t.Errorf("ReadOnly.GetWithETag(%q) ETag = %q, want the wrapped store's, %q", missing, etag, wantETag)
		}
	})

	if err := ro.Put(ctx, "other", []byte("x")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ReadOnly.Put: err = %v, want ErrUnsupported", err)
	}
}
