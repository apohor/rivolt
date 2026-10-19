// Package s3 is a minimal S3 client for the telemetry archive: put, get,
// delete and list objects in one bucket, path-style, signed with AWS
// Signature Version 4 over the real payload hash (never UNSIGNED-PAYLOAD).
// It talks to the in-cluster MinIO; nothing here is MinIO-specific.
//
// A handful of verbs didn't justify a client library and its dependency tree
// (DA, N19 A2): the signer is checked against AWS's published examples
// (s3_test.go) and every verb against a real MinIO (integration test).
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// emptySHA256 is the SHA-256 of an empty payload.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// ErrNotFound is a GET of a key that doesn't exist.
var ErrNotFound = errors.New("s3: no such key")

// Client is one bucket on one endpoint.
type Client struct {
	// Endpoint is the service URL, e.g. http://minio.minio.svc:9000.
	Endpoint  string
	Region    string // e.g. us-east-1
	Bucket    string
	AccessKey string
	SecretKey string
	// HTTP defaults to a client with a 2-minute timeout.
	HTTP *http.Client
	// Now is the signing clock (tests pin it).
	Now func() time.Time
	// ListPageSize is ListObjectsV2's max-keys (0: the server's 1000). Tests
	// set it small to exercise continuation.
	ListPageSize int
}

// Object is one ListObjectsV2 entry.
type Object struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Put stores body under key.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	resp, err := c.do(ctx, http.MethodPut, key, nil, h, body)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusErr("put", key, resp)
	}
	return nil
}

// Get returns the object's body; the caller closes it. A missing key is
// ErrNotFound.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	defer drain(resp)
	return nil, statusErr("get", key, resp)
}

// Delete removes key; deleting a missing key is not an error (S3 semantics).
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, nil, nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return statusErr("delete", key, resp)
	}
	return nil
}

// List calls fn for every object under prefix, following ListObjectsV2
// continuation tokens until the listing is complete or fn returns an error.
func (c *Client) List(ctx context.Context, prefix string, fn func(Object) error) error {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if c.ListPageSize > 0 {
			q.Set("max-keys", fmt.Sprint(c.ListPageSize))
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "", q, nil, nil)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			err := statusErr("list", prefix, resp)
			drain(resp)
			return err
		}
		var out struct {
			Contents []struct {
				Key          string
				Size         int64
				ETag         string
				LastModified time.Time
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out)
		drain(resp)
		if err != nil {
			return fmt.Errorf("s3: list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			if err := fn(Object{Key: o.Key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`), LastModified: o.LastModified}); err != nil {
				return err
			}
		}
		if !out.IsTruncated {
			return nil
		}
		if out.NextContinuationToken == "" {
			return fmt.Errorf("s3: list %s: truncated without a continuation token", prefix)
		}
		token = out.NextContinuationToken
	}
}

// CreateBucket creates the client's bucket. For tests and local development:
// in a cluster the buckets are created by the MinIO bootstrap Job, and the
// app's credentials can't create them.
func (c *Client) CreateBucket(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "", nil, nil, nil)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusErr("create bucket", c.Bucket, resp)
	}
	return nil
}

// do signs and sends one request for key (or the bucket itself for "").
func (c *Client) do(ctx context.Context, method, key string, query url.Values, h http.Header, body []byte) (*http.Response, error) {
	base, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("s3: endpoint: %w", err)
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + key
	}
	u := *base
	u.Path = path
	u.RawPath = uriEncode(path, false)
	u.RawQuery = canonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.ContentLength = int64(len(body))
	sign(req, body, c.AccessKey, c.SecretKey, c.Region, "s3", c.now())
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// sign adds x-amz-date, x-amz-content-sha256 and the SigV4 Authorization
// header. The signed headers are host, x-amz-*, and content-type, range,
// content-md5 and date when set.
func sign(req *http.Request, body []byte, accessKey, secretKey, region, service string, now time.Time) {
	payload := emptySHA256
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		payload = hex.EncodeToString(sum[:])
	}
	amzDate := now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	auth, _ := signature(req, payload, accessKey, secretKey, region, service, amzDate)
	req.Header.Set("Authorization", auth)
}

// signature builds the Authorization header value and returns it with the
// canonical request (for tests).
func signature(req *http.Request, payloadHash, accessKey, secretKey, region, service, amzDate string) (string, string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-type" || lk == "range" || lk == "content-md5" || lk == "date" {
			headers[lk] = strings.Join(trimAll(v), ",")
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method,
		path,
		canonicalQuery(req.URL.Query()),
		ch.String(),
		signed,
		payloadHash,
	}, "\n")
	day := amzDate[:8]
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+secretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig, canonical
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func trimAll(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = strings.Join(strings.Fields(s), " ")
	}
	return out
}

// canonicalQuery sorts by key then value and encodes per SigV4 (RFC 3986
// unreserved characters left as they are, everything else %XX; space is %20).
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// uriEncode is SigV4's UriEncode: unreserved characters stay, '/' stays in a
// path (encodeSlash false), every other byte becomes %XX in upper case.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// statusErr reports a non-success status with S3's error code, never the
// request's credentials.
func statusErr(op, key string, resp *http.Response) error {
	var e struct {
		Code    string
		Message string
	}
	_ = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	if e.Code != "" {
		return fmt.Errorf("s3: %s %s: %d %s: %s", op, key, resp.StatusCode, e.Code, e.Message)
	}
	return fmt.Errorf("s3: %s %s: status %d", op, key, resp.StatusCode)
}
