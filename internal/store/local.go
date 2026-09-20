package store

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Local is a directory-backed Store. It exists so the filesystem can be
// demonstrated and tested with no credentials and no network, and so the S3
// backend has something to be differentially tested against.
type Local struct {
	root string
	mu   sync.RWMutex // serializes conditional writes
}

func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Local{root: root}, nil
}

func (l *Local) Name() string { return "local:" + l.root }

// path maps an object key to a file path. Keys are escaped so that a key
// containing "/" nests, but ".." and absolute paths cannot escape the root.
func (l *Local) path(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("store: empty key")
	}
	clean := filepath.Clean("/" + key)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("store: invalid key %q", key)
	}
	return filepath.Join(l.root, filepath.FromSlash(clean)), nil
}

func (l *Local) Get(ctx context.Context, key string) ([]byte, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (l *Local) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}

func (l *Local) Head(ctx context.Context, key string) (ObjectInfo, error) {
	p, err := l.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	} else if err != nil {
		return ObjectInfo{}, err
	}
	et, err := l.etag(p)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: st.Size(), ETag: et}, nil
}

func (l *Local) etag(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := md5.Sum(b)
	return hex.EncodeToString(s[:]), nil
}

func (l *Local) Put(ctx context.Context, key string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Write via a temp file and rename so a reader never sees a partial object,
	// matching S3's atomic-object semantics.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (l *Local) Delete(ctx context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *Local) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, err := l.Head(ctx, key)
	switch {
	case errors.Is(err, ErrNotFound):
		if etag != "" {
			return "", ErrPrecondition
		}
	case err != nil:
		return "", err
	default:
		if etag == "" || cur.ETag != etag {
			return "", ErrPrecondition
		}
	}
	if err := l.Put(ctx, key, data); err != nil {
		return "", err
	}
	s := md5.Sum(data)
	return hex.EncodeToString(s[:]), nil
}

func (l *Local) List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error) {
	var out []ObjectInfo
	base := l.root
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) || key <= after {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, ObjectInfo{Key: key, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, nil
}
