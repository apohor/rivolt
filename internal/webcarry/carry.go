// Package webcarry layers the hashed web assets of other builds under the embedded bundle. It is separate from internal/web so that packages that
// only need the overlay (and their tests) do not import the //go:embed package, which needs a built dist directory.
package webcarry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// The carry directory holds the hashed /assets/ files of other builds, so a browser that loaded index.html from one version of the app can still
// fetch its chunks from a pod running another during a rolling update (and a tab left open across a deploy can fetch a chunk it had not loaded
// yet). The image build fills it (scripts/web-carry.sh: the last 48 hours of main builds plus the last two final releases, taken from their
// registry images), next to a MANIFEST.json. The server reads it only for a file the embedded build lacks, only for a hashed asset name, and
// never lists it: nothing here can change what an existing path serves or reveal what is in the directory.
const DefaultCarryDir = "/app/web-carry"

// hashedAsset is the shape of Vite's content-hashed output, assets/<name>-<8 chars>.<ext> (the same rule the SPA handler caches by).
var hashedAsset = regexp.MustCompile(`^assets/[^/]+-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

// CarryInfo is what the build recorded about the carry directory (its MANIFEST.json); zero when there is none.
type CarryInfo struct {
	// Files is how many assets the manifest lists.
	Files int
	// Missing names the sources the build could not copy (a failed pull, or an image from before the carry existed); the build stays green and
	// flags it. rivolt_web_carry_missing_sources exports its length.
	Missing []string
}

type manifest struct {
	Files   map[string]int64 `json:"files"`
	Missing []string         `json:"missing"`
}

// WithCarry layers dir under base: a file base has is base's; a hashed asset base lacks is read from dir if it is a regular file there. When dir
// does not exist (a self-hosted build, a dev run) base is returned as it is.
func WithCarry(base fs.FS, dir string) (fs.FS, CarryInfo) {
	if base == nil {
		return base, CarryInfo{}
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return base, CarryInfo{}
	}
	var info CarryInfo
	if b, err := os.ReadFile(filepath.Join(dir, "MANIFEST.json")); err == nil {
		var m manifest
		if json.Unmarshal(b, &m) == nil {
			info = CarryInfo{Files: len(m.Files), Missing: m.Missing}
		}
	}
	return carryFS{base: base, dir: dir}, info
}

type carryFS struct {
	base fs.FS
	dir  string
}

func (c carryFS) Open(name string) (fs.File, error) {
	f, err := c.base.Open(name)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	// Only a hashed asset, by its exact name: no directory, no manifest, no other file in the directory is reachable.
	if !fs.ValidPath(name) || !hashedAsset.MatchString(name) {
		return nil, err
	}
	p := filepath.Join(c.dir, filepath.FromSlash(name))
	st, serr := os.Lstat(p)
	if serr != nil || !st.Mode().IsRegular() {
		return nil, err
	}
	cf, oerr := os.Open(p)
	if oerr != nil {
		return nil, err
	}
	return cf, nil
}
