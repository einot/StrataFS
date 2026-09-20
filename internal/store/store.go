// Package store abstracts the object storage that holds the filesystem. Two
// backends exist: a real S3 client and a local directory backend used for
// tests and for running the demo without cloud credentials.
package store

import (
	"context"
	"errors"
)

var (
	// ErrNotFound means the key does not exist.
	ErrNotFound = errors.New("store: object not found")
	// ErrPrecondition means a conditional write lost the race: the object was
	// modified by someone else. This is what makes the root-pointer swap safe.
	ErrPrecondition = errors.New("store: precondition failed")
	// ErrUnsupported means the backend cannot do conditional writes at all.
	ErrUnsupported = errors.New("store: operation not supported by backend")
)

// ObjectInfo describes one listed object.
type ObjectInfo struct {
	Key  string
	Size int64
	ETag string
}

// Store is a minimal object store: the five verbs strata actually needs.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// Get returns the whole object.
	Get(ctx context.Context, key string) ([]byte, error)
	// GetRange returns bytes [off, off+n). Backends map this to an HTTP Range
	// request so reading one chunk never transfers the whole object.
	GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
	// Head returns size and ETag without transferring the body.
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// Put writes an object unconditionally.
	Put(ctx context.Context, key string, data []byte) error
	// Delete removes an object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns up to max objects under prefix, starting after the given
	// key. An empty returned slice means the listing is exhausted.
	List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error)

	// PutIfMatch writes only if the object's current ETag equals etag. An empty
	// etag means "only if the object does not exist" (If-None-Match: *).
	// Returns ErrPrecondition if the condition fails.
	PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error)

	// Name identifies the backend in logs and errors.
	Name() string
}

// ReadOnly wraps a Store and rejects every mutation. A filesystem opened
// through this can be inspected or recovered with no risk of modifying the
// bucket, because the refusal sits below the filesystem rather than relying on
// every write path remembering to check a flag.
type ReadOnly struct{ Store }

func (r ReadOnly) Put(ctx context.Context, key string, data []byte) error {
	return ErrUnsupported
}
func (r ReadOnly) Delete(ctx context.Context, key string) error { return ErrUnsupported }
func (r ReadOnly) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	return "", ErrUnsupported
}
func (r ReadOnly) Name() string { return r.Store.Name() + " (read-only)" }
