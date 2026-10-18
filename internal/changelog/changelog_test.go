package changelog

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const releasesJSON = `[
 {"tag_name":"v0.37.0-dev.3","name":"dev","body":"x","html_url":"https://github.com/apohor/rivolt/releases/tag/v0.37.0-dev.3","published_at":"2026-10-19T10:00:00Z"},
 {"tag_name":"v0.36.22","name":"v0.36.22","body":"<!-- rivolt:auto-notes -->\n## Changes since v0.36.21\n- x (abc)","html_url":"https://github.com/apohor/rivolt/releases/tag/v0.36.22","published_at":"2026-10-19T09:00:00Z"},
 {"tag_name":"v0.36.21","name":"v0.36.21","body":"## What's new\r\n\r\n- Safer <script>alert(1)</script> sign-in & more","html_url":"https://github.com/apohor/rivolt/releases/tag/v0.36.21","published_at":"2026-10-18T20:56:28Z"},
 {"tag_name":"v0.36.20","name":"","body":"## What's new\n\n- Clearer emails","html_url":"javascript:alert(1)","published_at":"2026-10-18T20:29:34Z"},
 {"tag_name":"v0.36.19","name":"draft","body":"notes","draft":true,"html_url":"https://github.com/x","published_at":"2026-10-18T17:24:26Z"},
 {"tag_name":"v0.36.18","name":"pre","body":"notes","prerelease":true,"html_url":"https://github.com/x","published_at":"2026-10-18T05:59:48Z"},
 {"tag_name":"v0.36.17","name":"empty","body":"  ","html_url":"https://github.com/x","published_at":"2026-10-17T05:59:48Z"}
]`

func fakeGitHub(t *testing.T, calls *int32, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("GitHub requires a User-Agent")
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("no token may be sent")
		}
		if fail != nil && fail.Load() {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(releasesJSON))
	}))
}

func TestReleasesKeepsOnlyWrittenNotes(t *testing.T) {
	var calls int32
	gh := fakeGitHub(t, &calls, nil)
	defer gh.Close()
	s := New(gh.URL)
	rels, err := s.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || rels[0].Tag != "v0.36.21" || rels[1].Tag != "v0.36.20" {
		t.Fatalf("got %+v", rels)
	}
	if strings.Contains(rels[0].BodyMD, "\r") {
		t.Errorf("CRLF kept: %q", rels[0].BodyMD)
	}
	if rels[1].Title != "v0.36.20" || rels[1].URL != "" {
		t.Errorf("an empty name falls back to the tag, and a non-GitHub URL is dropped: %+v", rels[1])
	}
}

func TestReleasesCachesRevalidatesAndServesStale(t *testing.T) {
	var calls int32
	var fail atomic.Bool
	gh := fakeGitHub(t, &calls, &fail)
	defer gh.Close()
	now := time.Date(2026, 10, 18, 21, 0, 0, 0, time.UTC)
	s := New(gh.URL)
	s.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := s.Releases(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _ = s.Releases(ctx); calls != 1 {
		t.Fatalf("within the TTL there is no refetch, calls=%d", calls)
	}
	now = now.Add(16 * time.Minute)
	rels, err := s.Releases(ctx) // 304
	if err != nil || len(rels) != 2 || calls != 2 {
		t.Fatalf("revalidation: %d releases, err %v, calls %d", len(rels), err, calls)
	}
	now = now.Add(16 * time.Minute)
	fail.Store(true)
	rels, err = s.Releases(ctx) // GitHub down: the stale copy
	if err != nil || len(rels) != 2 {
		t.Fatalf("stale on error: %d releases, err %v", len(rels), err)
	}

	cold := New(gh.URL)
	if _, err := cold.Releases(ctx); err == nil {
		t.Error("with no copy at all, an error must surface")
	}
}

func TestAtomIsValidAndEscaped(t *testing.T) {
	rels := filter(mustRaw(t))
	b, err := Atom(rels, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "<script>") {
		t.Fatalf("raw <script> in the feed:\n%s", b)
	}
	var f struct {
		XMLName  xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		ID       string   `xml:"id"`
		Subtitle string   `xml:"subtitle"`
		Updated  string   `xml:"updated"`
		Entries  []struct {
			ID      string `xml:"id"`
			Content struct {
				Type string `xml:"type,attr"`
				Body string `xml:",chardata"`
			} `xml:"content"`
			Links []struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(b, &f); err != nil {
		t.Fatalf("not valid XML: %v\n%s", err, b)
	}
	if len(f.Entries) != 2 || f.Entries[0].ID != "tag:rivolt.dev,2026:v0.36.21" || f.Updated != "2026-10-18T20:56:28Z" {
		t.Fatalf("feed: %+v", f)
	}
	e := f.Entries[0]
	if e.Content.Type != "text" || !strings.Contains(e.Content.Body, "<script>alert(1)</script>") {
		t.Errorf("the notes must round-trip as text: %+v", e.Content)
	}
	if e.Links[0].Href != "https://rivolt.dev/changelog#v0.36.21" {
		t.Errorf("entry link: %+v", e.Links)
	}
	if len(f.Entries[1].Links) != 1 {
		t.Errorf("a dropped release URL must not be linked: %+v", f.Entries[1].Links)
	}
	if !strings.Contains(f.Subtitle, "not affiliated with Rivian") {
		t.Errorf("subtitle: %q", f.Subtitle)
	}
}

func mustRaw(t *testing.T) []ghRelease {
	t.Helper()
	var calls int32
	gh := fakeGitHub(t, &calls, nil)
	defer gh.Close()
	s := New(gh.URL)
	rels, _, _, err := s.fetch(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// fetch already filters; turn the result back into raw releases.
	out := make([]ghRelease, len(rels))
	for i, r := range rels {
		out[i] = ghRelease{TagName: r.Tag, Name: r.Title, Body: r.BodyMD, HTMLURL: r.URL, PublishedAt: r.PublishedAt}
	}
	return out
}
