package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// AWS's published Signature Version 4 examples. The S3 ones are from the
// "Authenticating Requests: Using the Authorization Header" examples in the
// Amazon S3 API reference (examplebucket, 2013-05-24); get-vanilla is from
// the SigV4 test suite (service "service", 2015-08-30).
func TestSignatureAWSExamples(t *testing.T) {
	const (
		s3Key    = "AKIAIOSFODNN7EXAMPLE"
		s3Secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		s3Date   = "20130524T000000Z"
	)
	putBody := "Welcome to Amazon S3."
	putSum := sha256.Sum256([]byte(putBody))
	cases := []struct {
		name, method, url                  string
		headers                            map[string]string
		payload                            string
		key, secret, region, service, date string
		want                               string
	}{
		{"s3 get object", "GET", "https://examplebucket.s3.amazonaws.com/test.txt",
			map[string]string{"Range": "bytes=0-9"}, emptySHA256,
			s3Key, s3Secret, "us-east-1", "s3", s3Date,
			"f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"s3 put object", "PUT", "https://examplebucket.s3.amazonaws.com/test$file.text",
			map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"}, hex.EncodeToString(putSum[:]),
			s3Key, s3Secret, "us-east-1", "s3", s3Date,
			"98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"s3 get bucket lifecycle", "GET", "https://examplebucket.s3.amazonaws.com/?lifecycle",
			nil, emptySHA256,
			s3Key, s3Secret, "us-east-1", "s3", s3Date,
			"fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"s3 list objects", "GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			nil, emptySHA256,
			s3Key, s3Secret, "us-east-1", "s3", s3Date,
			"34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, c.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.URL.RawPath = uriEncode(req.URL.Path, false)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("X-Amz-Date", c.date)
			req.Header.Set("X-Amz-Content-Sha256", c.payload)
			auth, canonical := signature(req, c.payload, c.key, c.secret, c.region, c.service, c.date)
			if !strings.HasSuffix(auth, "Signature="+c.want) {
				t.Errorf("authorization %s\nwant signature %s\ncanonical request:\n%s", auth, c.want, canonical)
			}
		})
	}
	// get-vanilla (aws-sig-v4-test-suite): only host and x-amz-date signed.
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	req.Header.Set("X-Amz-Date", "20150830T123600Z")
	auth, canonical := signature(req, emptySHA256, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "service", "20150830T123600Z")
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if auth != want {
		t.Errorf("get-vanilla:\n got %s\nwant %s\ncanonical:\n%s", auth, want, canonical)
	}
}

func TestURIEncode(t *testing.T) {
	for in, want := range map[string]string{
		"prod/v1/u=ab-12/v=x/2026-05.csv.gz.enc": "prod/v1/u%3Dab-12/v%3Dx/2026-05.csv.gz.enc",
		"a b+c~d_e.f":                            "a%20b%2Bc~d_e.f",
		"é":                                      "%C3%A9",
	} {
		if got := uriEncode(in, false); got != want {
			t.Errorf("uriEncode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("slash in a query value: %q", got)
	}
}
