package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Credentials are the AWS keys used to sign requests.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// emptyPayloadHash is Hex(SHA256("")), required by S3 on bodyless requests.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signV4 signs req in place with AWS Signature Version 4, following
// the canonical-request / string-to-sign / derived-key procedure.
//
// payloadHash must be the lowercase hex SHA-256 of the body (or the literal
// UNSIGNED-PAYLOAD). We always sign the real hash: chunks are content-addressed
// by SHA-256 anyway, so the digest is already computed.
func signV4(req *http.Request, creds Credentials, region, service, payloadHash string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	// x-amz-content-sha256 is an S3 requirement, not a general SigV4 one, so
	// it is only set for S3. Other services must not have it in the signature.
	if service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	if req.Host != "" {
		req.Header.Set("Host", req.Host)
	} else {
		req.Header.Set("Host", req.URL.Host)
	}

	canonHeaders, signedHeaders := canonicalHeaders(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.EscapedPath()),
		canonicalQuery(req.URL.RawQuery),
		canonHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	crHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(crHash[:]),
	}, "\n")

	signingKey := deriveKey(creds.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+creds.AccessKeyID+"/"+scope+
			", SignedHeaders="+signedHeaders+
			", Signature="+signature)
}

// deriveKey performs the HMAC chain that turns the secret key into a key
// scoped to one day, region and service.
func deriveKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalHeaders builds the sorted lowercase header block and the
// semicolon-separated list of signed header names.
//
// We sign host, content-type when present, and every x-amz-* header. Volatile
// hop-by-hop headers are deliberately excluded, as the spec warns.
func canonicalHeaders(req *http.Request) (block, signed string) {
	type hv struct{ name, value string }
	var hs []hv

	add := func(name, value string) {
		hs = append(hs, hv{strings.ToLower(name), trimAll(value)})
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	add("host", host)

	for name, vals := range req.Header {
		l := strings.ToLower(name)
		if l == "host" {
			continue
		}
		if l == "content-type" || strings.HasPrefix(l, "x-amz-") {
			add(l, strings.Join(vals, ","))
		}
	}

	sort.Slice(hs, func(i, j int) bool { return hs[i].name < hs[j].name })

	var b strings.Builder
	names := make([]string, 0, len(hs))
	for _, h := range hs {
		b.WriteString(h.name)
		b.WriteByte(':')
		b.WriteString(h.value)
		b.WriteByte('\n')
		names = append(names, h.name)
	}
	return b.String(), strings.Join(names, ";")
}

// trimAll trims surrounding space and collapses internal runs of spaces to one,
// as the canonical-header rules require.
func trimAll(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// canonicalURI returns the already-escaped path, or "/" when empty.
//
// The path is passed through as escaped by net/url, which encodes exactly the
// characters SigV4 wants and leaves "/" separators alone. S3 does not
// normalize paths, so we must not collapse anything here either.
func canonicalURI(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	return escapedPath
}

// canonicalQuery re-encodes and sorts query parameters. Sorting happens after
// encoding, per the spec.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	var pairs []string
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		// Decode then re-encode so the result is canonical regardless of how
		// the caller wrote it.
		pairs = append(pairs, uriEncode(unescape(k), true)+"="+uriEncode(unescape(v), true))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

func unescape(s string) string {
	// Minimal percent-decoder: query components we generate are already simple,
	// but a caller may pass an encoded key.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := fromHex(s[i+1])
			lo, ok2 := fromHex(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func fromHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

const upperhex = "0123456789ABCDEF"

// uriEncode percent-encodes every byte except the RFC 3986 unreserved set.
// Go's net/url helpers do not match these rules exactly, which is why the spec
// recommends writing this by hand.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}
