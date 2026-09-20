package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// S3 is a dependency-free S3 client covering the five verbs strata needs.
// It works against AWS S3 and against S3-compatible servers such as MinIO.
type S3 struct {
	client    *http.Client
	endpoint  *url.URL
	bucket    string
	region    string
	creds     Credentials
	pathStyle bool

	// maxRetries bounds retries of transient failures (5xx, 429, connection
	// errors). Conditional writes are never retried: a retry could turn a lost
	// race into a silent overwrite.
	maxRetries int
}

// S3Config configures an S3 backend.
type S3Config struct {
	Endpoint  string // e.g. https://s3.us-east-1.amazonaws.com, or http://127.0.0.1:9000
	Bucket    string
	Region    string
	Creds     Credentials
	PathStyle bool
	Timeout   time.Duration
}

func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("store: bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("store: bad endpoint %q: %w", cfg.Endpoint, err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &S3{
		client:     &http.Client{Timeout: cfg.Timeout},
		endpoint:   u,
		bucket:     cfg.Bucket,
		region:     cfg.Region,
		creds:      cfg.Creds,
		pathStyle:  cfg.PathStyle,
		maxRetries: 3,
	}, nil
}

func (s *S3) Name() string { return "s3://" + s.bucket }

// url builds the request URL for a key, setting RawPath so that the escaping
// we sign is byte-for-byte the escaping we send.
func (s *S3) url(key string, query url.Values) *url.URL {
	u := *s.endpoint
	if s.pathStyle {
		u.Path = "/" + s.bucket + "/" + key
		u.RawPath = "/" + uriEncode(s.bucket, true) + "/" + uriEncode(key, false)
	} else {
		u.Host = s.bucket + "." + u.Host
		u.Path = "/" + key
		u.RawPath = "/" + uriEncode(key, false)
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return &u
}

// do signs and sends a request, retrying transient failures. body may be nil.
// The caller owns closing the returned response body.
func (s *S3) do(ctx context.Context, method string, u *url.URL, body []byte, headers map[string]string, retry bool) (*http.Response, error) {
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	if body == nil {
		payloadHash = emptyPayloadHash
	}

	attempts := 1
	if retry {
		attempts = s.maxRetries + 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff, capped. Keeps a throttled bucket from being
			// hammered while a mounted filesystem is under load.
			delay := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.ContentLength = int64(len(body))
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		signV4(req, s.creds, s.region, "s3", payloadHash, time.Now())

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if retry && (resp.StatusCode >= 500 || resp.StatusCode == 429) {
			drain(resp)
			lastErr = fmt.Errorf("store: %s %s: HTTP %d", method, u.Path, resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// s3Error is the XML error document S3 returns on failure.
type s3Error struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// errorFor turns a non-2xx response into a typed error.
func errorFor(method, key string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()

	var e s3Error
	xml.Unmarshal(body, &e)

	switch {
	case resp.StatusCode == http.StatusNotFound,
		e.Code == "NoSuchKey", e.Code == "NoSuchBucket":
		return ErrNotFound
	case resp.StatusCode == http.StatusPreconditionFailed,
		resp.StatusCode == http.StatusConflict,
		e.Code == "PreconditionFailed", e.Code == "ConditionalRequestConflict":
		return ErrPrecondition
	case resp.StatusCode == http.StatusNotImplemented:
		return ErrUnsupported
	}
	msg := e.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("store: %s %s: HTTP %d %s: %s", method, key, resp.StatusCode, e.Code, msg)
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, s.url(key, nil), nil, nil, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFor("GET", key, resp)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (s *S3) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	h := map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", off, off+n-1),
	}
	resp, err := s.do(ctx, http.MethodGet, s.url(key, nil), nil, h, true)
	if err != nil {
		return nil, err
	}
	// 206 is the expected answer; 200 means the server ignored Range and sent
	// the whole object, which we then trim ourselves.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, errorFor("GET", key, resp)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		if off >= int64(len(data)) {
			return nil, nil
		}
		end := off + n
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		data = data[off:end]
	}
	return data, nil
}

func (s *S3) Head(ctx context.Context, key string) (ObjectInfo, error) {
	resp, err := s.do(ctx, http.MethodHead, s.url(key, nil), nil, nil, true)
	if err != nil {
		return ObjectInfo{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// HEAD carries no body, so errorFor can only use the status code.
		return ObjectInfo{}, errorFor("HEAD", key, resp)
	}
	defer resp.Body.Close()
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return ObjectInfo{Key: key, Size: size, ETag: cleanETag(resp.Header.Get("ETag"))}, nil
}

func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	resp, err := s.do(ctx, http.MethodPut, s.url(key, nil), data, nil, true)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errorFor("PUT", key, resp)
	}
	drain(resp)
	return nil
}

func (s *S3) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	if data == nil {
		data = []byte{}
	}
	h := map[string]string{}
	if etag == "" {
		h["If-None-Match"] = "*"
	} else {
		h["If-Match"] = `"` + etag + `"`
	}
	// Never retried: a retried conditional PUT can succeed on the second try
	// against state the first try already changed.
	resp, err := s.do(ctx, http.MethodPut, s.url(key, nil), data, h, false)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errorFor("PUT", key, resp)
	}
	et := cleanETag(resp.Header.Get("ETag"))
	drain(resp)
	return et, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, s.url(key, nil), nil, nil, true)
	if err != nil {
		return err
	}
	// S3 answers 204 for a successful delete and also for a missing key.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			drain(resp)
			return nil
		}
		return errorFor("DELETE", key, resp)
	}
	drain(resp)
	return nil
}

// listBucketResult is the ListObjectsV2 response document.
type listBucketResult struct {
	IsTruncated bool `xml:"IsTruncated"`
	Contents    []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
		ETag string `xml:"ETag"`
	} `xml:"Contents"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func (s *S3) List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if after != "" {
		q.Set("start-after", after)
	}
	if max > 0 {
		q.Set("max-keys", strconv.Itoa(max))
	}

	u := *s.endpoint
	if s.pathStyle {
		u.Path = "/" + s.bucket
		u.RawPath = "/" + uriEncode(s.bucket, true)
	} else {
		u.Host = s.bucket + "." + u.Host
		u.Path = "/"
		u.RawPath = "/"
	}
	u.RawQuery = q.Encode()

	resp, err := s.do(ctx, http.MethodGet, &u, nil, nil, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFor("GET", "?list-type=2", resp)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var res listBucketResult
	if err := xml.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("store: parse listing: %w", err)
	}
	out := make([]ObjectInfo, 0, len(res.Contents))
	for _, c := range res.Contents {
		out = append(out, ObjectInfo{Key: c.Key, Size: c.Size, ETag: cleanETag(c.ETag)})
	}
	return out, nil
}

// cleanETag strips the quotes S3 wraps ETags in.
func cleanETag(s string) string {
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}
