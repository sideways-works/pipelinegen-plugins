package s3

import (
	"net/http"
	"testing"
	"time"
)

// AWS's published example of "Signature Calculations for the Authorization Header": GET Object,
// with the documentation's example credentials.
func TestSigningMatchesAWSsGetObjectExample(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"} //nolint:gosec // AWS's documented example key
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Range", "bytes=0-9")

	sign(req, "examplebucket.s3.amazonaws.com", "/test.txt", "", emptyHash, "us-east-1", creds, at)

	const want = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"

	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization:\n got %s\nwant %s", got, want)
	}

	canonical, _ := canonicalRequest(req.Method, "examplebucket.s3.amazonaws.com", "/test.txt", "", req.Header, emptyHash)
	if got := hashHex([]byte(canonical)); got != "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972" {
		t.Errorf("the canonical request's hash is %s:\n%s", got, canonical)
	}
}

func TestEncodingKeepsUnreservedAndSlashesInPaths(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"a/b c+d~e", "path"}:  "a/b%20c%2Bd~e",
		{"a/b c+d~e", "query"}: "a%2Fb%20c%2Bd~e",
		{"ü=&", "query"}:       "%C3%BC%3D%26",
	} {
		if got := encode(in[0], in[1] == "query"); got != want {
			t.Errorf("encode(%q, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
