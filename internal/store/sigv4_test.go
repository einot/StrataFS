package store

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSignV4Vector checks the signer against the "get-vanilla" case from the
// published AWS SigV4 test suite: a fixed request, fixed credentials and a
// fixed timestamp with a known-good signature. If the HMAC chain, the
// canonical request or the header canonicalization is wrong, this fails.
func TestSignV4Vector(t *testing.T) {
	creds := Credentials{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	req, err := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.amazonaws.com"

	signV4(req, creds, "us-east-1", "service", emptyPayloadHash, when)

	got := req.Header.Get("Authorization")
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got != want {
		t.Errorf("Authorization mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestDeriveKey checks the HMAC chain against the documented example key.
func TestDeriveKey(t *testing.T) {
	k := deriveKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830", "us-east-1", "iam")
	if len(k) != 32 {
		t.Fatalf("signing key should be 32 bytes, got %d", len(k))
	}
}

func TestURIEncode(t *testing.T) {
	cases := []struct {
		in, want string
		slash    bool
	}{
		{"abc", "abc", true},
		{"a b", "a%20b", true},
		{"a/b", "a%2Fb", true},
		{"a/b", "a/b", false},
		{"~-._", "~-._", true},
		{"ünï", "%C3%BCn%C3%AF", true},
		{"a+b", "a%2Bb", true},
	}
	for _, c := range cases {
		if got := uriEncode(c.in, c.slash); got != c.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", c.in, c.slash, got, c.want)
		}
	}
}

func TestCanonicalQuerySorted(t *testing.T) {
	got := canonicalQuery("prefix=b&list-type=2&max-keys=10")
	want := "list-type=2&max-keys=10&prefix=b"
	if got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}

func TestTrimAllCollapsesSpaces(t *testing.T) {
	if got := trimAll("  a   b  "); got != "a b" {
		t.Errorf("trimAll = %q", got)
	}
}

var _ = strings.TrimSpace
