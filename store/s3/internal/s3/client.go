package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Client reaches one bucket. It began as pipelinegen's plans client and adds what a store needs:
// reads that return an object's ETag, writes guarded by one, and listings with each key's.
//
// Client reaches one bucket. Endpoint is the service's base URL (https://s3.<region>.amazonaws.com
// for AWS); PathStyle puts the bucket in the path instead of the host, as SeaweedFS and MinIO
// usually want.
type Client struct {
	Endpoint  *url.URL
	Region    string
	Bucket    string
	PathStyle bool
	Creds     Credentials
	// HTTP sends the requests; nil is a client with a 5-minute timeout, for a record's largest version.
	HTTP *http.Client
	// Now is when a request is signed; nil is time.Now.
	Now func() time.Time
}

// ErrStatus is an answer other than the one a request expects; Error carries S3's error code and
// message only, never the request it echoes.
var ErrStatus = errors.New("s3")

// ErrPrecondition is a guarded write the object no longer allowed: S3's 412.
var ErrPrecondition = errors.New("s3: the object changed under the write")

// Error is a refused request: its status, and S3's code and message when the answer had them.
type Error struct {
	Op      string
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s: %d %s", e.Op, e.Status, http.StatusText(e.Status))
	if e.Code != "" {
		msg += ": " + e.Code
	}

	if e.Message != "" {
		msg += ": " + e.Message
	}

	return msg
}

func (e *Error) Unwrap() error { return ErrStatus }

// Put writes body under key with the given headers (Content-Type, Content-Encoding) and user
// metadata, each sent as x-amz-meta-<name>.
func (c *Client) Put(ctx context.Context, key string, body []byte, header map[string]string, meta map[string]string) error {
	h := http.Header{}
	for k, v := range header {
		h.Set(k, v)
	}

	for k, v := range meta {
		h.Set("X-Amz-Meta-"+k, v)
	}

	_, _, err := c.do(ctx, "put "+key, http.MethodPut, key, nil, h, body, http.StatusOK)

	return err
}

// PutIf writes body under key only when the object's ETag is ifMatch, or, with ifMatch empty, when
// there is no object; another object fails with ErrPrecondition. It returns the new ETag.
func (c *Client) PutIf(ctx context.Context, key string, body []byte, ifMatch string) (string, error) {
	h := http.Header{}
	if ifMatch == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", ifMatch)
	}

	_, got, err := c.do(ctx, "put "+key, http.MethodPut, key, nil, h, body, http.StatusOK)

	var e *Error
	if errors.As(err, &e) && (e.Status == http.StatusPreconditionFailed || e.Status == http.StatusConflict) {
		return "", fmt.Errorf("%w: %s", ErrPrecondition, key)
	}

	if err != nil {
		return "", err
	}

	return got.Get("ETag"), nil
}

// Get reads key and its ETag; a missing object is no error and no object.
func (c *Client) Get(ctx context.Context, key string) ([]byte, string, bool, error) {
	data, got, err := c.do(ctx, "get "+key, http.MethodGet, key, nil, nil, nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return nil, "", false, err
	}

	if got.Get(statusHeader) == strconv.Itoa(http.StatusNotFound) {
		return nil, "", false, nil
	}

	return data, got.Get("ETag"), true, nil
}

// Object is a listed key and its ETag.
type Object struct {
	Key  string
	ETag string
}

// Objects lists every key under prefix with its ETag, page by page.
func (c *Client) Objects(ctx context.Context, prefix string) ([]Object, error) {
	var (
		out   []Object
		token string
	)

	for {
		q := map[string]string{"list-type": "2", "prefix": prefix}
		if token != "" {
			q["continuation-token"] = token
		}

		data, _, err := c.do(ctx, "list "+prefix, http.MethodGet, "", q, nil, nil, http.StatusOK)
		if err != nil {
			return nil, err
		}

		var page struct {
			Contents []struct {
				Key  string `xml:"Key"`
				ETag string `xml:"ETag"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}

		if err := xml.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("%w: list %s: %w", ErrStatus, prefix, err)
		}

		for _, k := range page.Contents {
			out = append(out, Object{Key: k.Key, ETag: k.ETag})
		}

		if !page.IsTruncated {
			return out, nil
		}

		if page.NextContinuationToken == "" || page.NextContinuationToken == token {
			return nil, fmt.Errorf("%w: list %s: a truncated page without a new continuation token", ErrStatus, prefix)
		}

		token = page.NextContinuationToken
	}
}

// statusHeader carries an answer's status beside its headers, for a caller that took more than one.
const statusHeader = "X-Pipelinegen-Status"

// Delete removes key; a key already gone is no error, so two deletes of it both succeed.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, _, err := c.do(ctx, "delete "+key, http.MethodDelete, key, nil, nil, nil, http.StatusNoContent, http.StatusOK, http.StatusNotFound)

	return err
}

// Listing is one level of a bucket under a prefix: the keys at it, and with a delimiter the
// common prefixes below it, each ending in the delimiter.
type Listing struct {
	Keys     []string
	Prefixes []string
}

// List lists every key under prefix with ListObjectsV2, page by page; with a delimiter the keys
// past it are folded into Prefixes.
func (c *Client) List(ctx context.Context, prefix, delimiter string) (Listing, error) {
	var (
		out   Listing
		token string
	)

	for {
		q := map[string]string{"list-type": "2", "prefix": prefix}
		if delimiter != "" {
			q["delimiter"] = delimiter
		}

		if token != "" {
			q["continuation-token"] = token
		}

		data, _, err := c.do(ctx, "list "+prefix, http.MethodGet, "", q, nil, nil, http.StatusOK)
		if err != nil {
			return Listing{}, err
		}

		var page struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			CommonPrefixes []struct {
				Prefix string `xml:"Prefix"`
			} `xml:"CommonPrefixes"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}

		if err := xml.Unmarshal(data, &page); err != nil {
			return Listing{}, fmt.Errorf("%w: list %s: %w", ErrStatus, prefix, err)
		}

		for _, k := range page.Contents {
			out.Keys = append(out.Keys, k.Key)
		}

		for _, p := range page.CommonPrefixes {
			out.Prefixes = append(out.Prefixes, p.Prefix)
		}

		if !page.IsTruncated {
			return out, nil
		}

		if page.NextContinuationToken == "" || page.NextContinuationToken == token {
			return Listing{}, fmt.Errorf("%w: list %s: a truncated page without a new continuation token", ErrStatus, prefix)
		}

		token = page.NextContinuationToken
	}
}

// maxAnswer bounds what is read of an answer: a record's version with its failure files is well
// under it, and one past it fails rather than coming back cut.
const maxAnswer = 512 << 20

// do sends a request and returns the answer's body and headers when its status is one of want; the
// status itself is in the headers as statusHeader.
func (c *Client) do(ctx context.Context, op, method, key string, query map[string]string, header http.Header, body []byte, want ...int) ([]byte, http.Header, error) {
	req, err := c.request(ctx, method, key, query, header, body)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", ErrStatus, op, err)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", ErrStatus, op, scrub(err))
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", ErrStatus, op, err)
	}

	if len(data) > maxAnswer {
		return nil, nil, fmt.Errorf("%w: %s: the answer is over %d MB", ErrStatus, op, maxAnswer>>20)
	}

	if slices.Contains(want, resp.StatusCode) {
		resp.Header.Set(statusHeader, strconv.Itoa(resp.StatusCode))

		return data, resp.Header, nil
	}

	e := &Error{Op: op, Status: resp.StatusCode}

	var answer struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}

	if xml.Unmarshal(data, &answer) == nil {
		e.Code, e.Message = printable(answer.Code), printable(answer.Message)
	}

	return nil, nil, e
}

// request is the signed request for key (the bucket itself when empty) in the client's style.
func (c *Client) request(ctx context.Context, method, key string, query map[string]string, header http.Header, body []byte) (*http.Request, error) {
	host, plain := c.Endpoint.Host, "/"

	switch {
	case c.PathStyle:
		plain += c.Bucket
		if key != "" {
			plain += "/" + key
		}
	default:
		host = c.Bucket + "." + host
		plain += key
	}

	// the path goes as it is signed: each character but the unreserved ones and slashes encoded once
	path, rawQuery := encode(plain, false), canonicalQuery(query)
	u := &url.URL{Scheme: c.Endpoint.Scheme, Host: host, Path: plain, RawPath: path, RawQuery: rawQuery}

	// the body is made again on each call, so a request sent again sends all of it
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err //nolint:wrapcheck // do names the operation
	}

	req.Host = host
	req.ContentLength = int64(len(body))

	if len(body) == 0 {
		req.Body = http.NoBody
	}

	maps.Copy(req.Header, header)

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	sign(req, host, path, rawQuery, hashHex(body), c.Region, c.Creds, now())

	return req, nil
}

// scrub keeps the cause of a request that got no answer and drops the request it names, whose
// query may carry a continuation token: only the operation names what was asked.
func scrub(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}

	return err
}

// printable cuts an answer's text to one short line of printable ASCII.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return ' '
		}

		return r
	}, s)

	const most = 200
	if len(s) > most {
		s = s[:most] + "..." + strconv.Itoa(len(s)-most) + " more"
	}

	return strings.TrimSpace(s)
}
