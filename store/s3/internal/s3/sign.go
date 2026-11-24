package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Credentials sign requests; a session token, when given, goes as x-amz-security-token.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

const (
	algorithm  = "AWS4-HMAC-SHA256"
	service    = "s3"
	amzDate    = "20060102T150405Z"
	shortDate  = "20060102"
	emptyHash  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	hdrDate    = "X-Amz-Date"
	hdrSHA256  = "X-Amz-Content-Sha256"
	hdrToken   = "X-Amz-Security-Token" //nolint:gosec // a header's name, not a credential
	hdrAuth    = "Authorization"
	scopeTail  = "aws4_request"
	headerHost = "host"
)

// sign adds the date, the payload's hash and the Authorization header of AWS Signature Version 4
// to req, signing the host and every header req holds then. host is the Host header as sent,
// path and query the request's, each already encoded as the canonical request takes it.
func sign(req *http.Request, host, path, query, payloadHash, region string, creds Credentials, now time.Time) {
	now = now.UTC()

	req.Header.Set(hdrDate, now.Format(amzDate))
	req.Header.Set(hdrSHA256, payloadHash)

	if creds.SessionToken != "" {
		req.Header.Set(hdrToken, creds.SessionToken)
	}

	canonical, signed := canonicalRequest(req.Method, host, path, query, req.Header, payloadHash)

	scope := now.Format(shortDate) + "/" + region + "/" + service + "/" + scopeTail
	toSign := algorithm + "\n" + now.Format(amzDate) + "\n" + scope + "\n" + hashHex([]byte(canonical))

	key := hmacOf([]byte("AWS4"+creds.SecretAccessKey), now.Format(shortDate))
	key = hmacOf(key, region)
	key = hmacOf(key, service)
	key = hmacOf(key, scopeTail)

	req.Header.Set(hdrAuth, algorithm+" Credential="+creds.AccessKeyID+"/"+scope+
		",SignedHeaders="+signed+",Signature="+hex.EncodeToString(hmacOf(key, toSign)))
}

// canonicalRequest is the request as SigV4 hashes it, and the names of the headers it signs: the
// host and every header given, lowercased and sorted, their values trimmed.
func canonicalRequest(method, host, path, query string, header http.Header, payloadHash string) (canonical, signed string) {
	values := map[string]string{headerHost: host}

	for name, vs := range header {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}

		trimmed := make([]string, 0, len(vs))
		for _, v := range vs {
			trimmed = append(trimmed, strings.Join(strings.Fields(v), " "))
		}

		values[lower] = strings.Join(trimmed, ",")
	}

	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}

	sort.Strings(names)

	var b strings.Builder

	b.WriteString(method + "\n" + path + "\n" + query + "\n")

	for _, n := range names {
		b.WriteString(n + ":" + values[n] + "\n")
	}

	signed = strings.Join(names, ";")
	b.WriteString("\n" + signed + "\n" + payloadHash)

	return b.String(), signed
}

// canonicalQuery sorts the parameters by name and encodes each name and value as SigV4 does.
func canonicalQuery(params map[string]string) string {
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, encode(n, true)+"="+encode(params[n], true))
	}

	return strings.Join(parts, "&")
}

// encode is RFC 3986's percent-encoding of everything but the unreserved characters, as SigV4
// takes it: a slash stays in a path and is encoded in a query.
func encode(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"

	var b strings.Builder

	for i := range len(s) {
		c := s[i]

		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}

	return b.String()
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func hmacOf(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))

	return h.Sum(nil)
}
