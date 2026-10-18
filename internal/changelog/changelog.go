// Package changelog serves Rivolt's release notes: the hand-written notes of
// each release, as published on its GitHub Release, for the /changelog page
// and the Atom feed.
//
// The notes are the release body once a person has written it. The body the
// release starts with is a generated commit list marked AutoNotesMarker, and
// a release with that body is left out until the notes replace it.
package changelog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultURL is the public releases list of the Rivolt repository.
	DefaultURL = "https://api.github.com/repos/apohor/rivolt/releases?per_page=30"
	// AutoNotesMarker starts a generated release body (release-notes.yml).
	AutoNotesMarker = "<!-- rivolt:auto-notes -->"

	defaultTTL     = 15 * time.Minute
	failureBackoff = 2 * time.Minute // no refetch this long after a failed one
	fetchTimeout   = 10 * time.Second
	maxBody        = 2 << 20 // bytes read from GitHub
	userAgent      = "rivolt-changelog (+https://rivolt.dev)"
	siteURL        = "https://rivolt.dev"
	feedID         = "tag:rivolt.dev,2026:changelog"
	feedTitle      = "Rivolt release notes"
	feedSubtitle   = "What's new in Rivolt. Rivolt is an unofficial app, not affiliated with Rivian."
	entryIDPrefix  = "tag:rivolt.dev,2026:"
	maxEntries     = 30
)

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Release is one release's notes.
type Release struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"published_at"`
	Title       string    `json:"title"`
	BodyMD      string    `json:"body_md"`
	URL         string    `json:"url"`
}

// Source fetches the releases from GitHub and keeps them for TTL. A failed
// refresh serves what it had. Safe for concurrent use.
type Source struct {
	URL  string
	HTTP *http.Client
	TTL  time.Duration
	Now  func() time.Time

	mu        sync.Mutex
	etag      string
	releases  []Release
	fetchedAt time.Time
	failedAt  time.Time
	have      bool
	fetching  bool
}

// ErrUnavailable is returned when there is no copy to serve: none was ever
// fetched, and a fetch is running, has just failed, or fails now.
var ErrUnavailable = errors.New("changelog: release notes unavailable")

// New returns a Source for url (DefaultURL when empty).
func New(url string) *Source {
	if url == "" {
		url = DefaultURL
	}
	return &Source{URL: url, HTTP: &http.Client{Timeout: fetchTimeout}, TTL: defaultTTL, Now: time.Now}
}

// Releases returns the published releases with hand-written notes, newest
// first. It refreshes from GitHub when the copy is older than TTL. At most one
// refresh runs at a time, and none for failureBackoff after a failed one;
// meanwhile callers get the copy, or ErrUnavailable when there is none. So
// GitHub sees at most one request per TTL, or per backoff while it is failing,
// however many clients ask.
func (s *Source) Releases(ctx context.Context) ([]Release, error) {
	s.mu.Lock()
	now := s.Now()
	fresh := s.have && now.Sub(s.fetchedAt) < s.TTL
	backingOff := !s.failedAt.IsZero() && now.Sub(s.failedAt) < failureBackoff
	if fresh || s.fetching || backingOff {
		out, have := s.releases, s.have
		s.mu.Unlock()
		if !have {
			return nil, ErrUnavailable
		}
		return out, nil
	}
	s.fetching = true
	etag := s.etag
	s.mu.Unlock()

	rels, newTag, notModified, err := s.fetch(ctx, etag)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetching = false
	switch {
	case err != nil:
		s.failedAt = s.Now()
		if s.have {
			return s.releases, nil // stale beats nothing
		}
		return nil, ErrUnavailable
	case notModified:
		s.fetchedAt, s.failedAt = s.Now(), time.Time{}
	default:
		s.releases, s.etag, s.fetchedAt, s.have, s.failedAt = rels, newTag, s.Now(), true, time.Time{}
	}
	return s.releases, nil
}

// ghRelease is the part of GitHub's release object that is used.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func (s *Source) fetch(ctx context.Context, etag string) ([]Release, string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("changelog: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, "", true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("changelog: GitHub answered %d", resp.StatusCode)
	}
	var raw []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&raw); err != nil {
		return nil, "", false, fmt.Errorf("changelog: decode: %w", err)
	}
	return filter(raw), resp.Header.Get("ETag"), false, nil
}

// filter keeps the published vX.Y.Z releases whose notes have been written,
// newest first, at most maxEntries.
func filter(raw []ghRelease) []Release {
	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		body := strings.TrimSpace(strings.ReplaceAll(r.Body, "\r\n", "\n"))
		if r.Draft || r.Prerelease || !releaseTag.MatchString(r.TagName) || body == "" || strings.Contains(body, AutoNotesMarker) {
			continue
		}
		title := strings.TrimSpace(r.Name)
		if title == "" {
			title = r.TagName
		}
		url := r.HTMLURL
		if !strings.HasPrefix(url, "https://github.com/") {
			url = "" // only ever link to the release page on GitHub
		}
		out = append(out, Release{Tag: r.TagName, PublishedAt: r.PublishedAt.UTC(), Title: title, BodyMD: body, URL: url})
	}
	// GitHub lists newest first; keep that, bounded.
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	return out
}

// Atom renders the releases as an Atom 1.0 feed. Every text goes through
// encoding/xml, so titles, links and notes are escaped; the notes are carried
// as text (the Markdown as written), never as HTML.
func Atom(rels []Release, now time.Time) ([]byte, error) {
	type link struct {
		Rel  string `xml:"rel,attr,omitempty"`
		Type string `xml:"type,attr,omitempty"`
		Href string `xml:"href,attr"`
	}
	type text struct {
		Type string `xml:"type,attr"`
		Body string `xml:",chardata"`
	}
	type entry struct {
		ID        string `xml:"id"`
		Title     text   `xml:"title"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Links     []link `xml:"link"`
		Content   text   `xml:"content"`
	}
	type author struct {
		Name string `xml:"name"`
		URI  string `xml:"uri"`
	}
	type feed struct {
		XMLName  xml.Name `xml:"feed"`
		NS       string   `xml:"xmlns,attr"`
		ID       string   `xml:"id"`
		Title    text     `xml:"title"`
		Subtitle text     `xml:"subtitle"`
		Updated  string   `xml:"updated"`
		Links    []link   `xml:"link"`
		Author   author   `xml:"author"`
		Entries  []entry  `xml:"entry"`
	}
	updated := now.UTC()
	if len(rels) > 0 {
		updated = rels[0].PublishedAt
	}
	f := feed{
		NS: "http://www.w3.org/2005/Atom", ID: feedID,
		Title:    text{Type: "text", Body: feedTitle},
		Subtitle: text{Type: "text", Body: feedSubtitle},
		Updated:  updated.Format(time.RFC3339),
		Links: []link{
			{Rel: "self", Type: "application/atom+xml", Href: siteURL + "/changelog.atom"},
			{Rel: "alternate", Type: "text/html", Href: siteURL + "/changelog"},
		},
		Author: author{Name: "Rivolt", URI: siteURL},
	}
	for _, r := range rels {
		ts := r.PublishedAt.UTC().Format(time.RFC3339)
		e := entry{
			ID: entryIDPrefix + r.Tag, Title: text{Type: "text", Body: r.Title},
			Updated: ts, Published: ts,
			Links:   []link{{Rel: "alternate", Type: "text/html", Href: siteURL + "/changelog#" + r.Tag}},
			Content: text{Type: "text", Body: r.BodyMD},
		}
		if r.URL != "" {
			e.Links = append(e.Links, link{Rel: "related", Type: "text/html", Href: r.URL})
		}
		f.Entries = append(f.Entries, e)
	}
	b, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}
