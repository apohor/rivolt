package privacy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Rivian vehicle id ("01-" and nine or ten digits) in the tree must be a placeholder: real ids belong to
// real trucks and their owners. Fixtures, tests, comments and examples use 01-0000000NN (or a non-Rivian
// string such as "veh-fixture").
var (
	rivianID    = regexp.MustCompile(`\b01-[0-9]{9,10}\b`)
	placeholder = regexp.MustCompile(`^01-0000000[0-9]{2}$`)
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "dist": true, "build": true, "DerivedData": true, ".build": true}

func TestNoRealVehicleIDsInTheTree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil || info.Size() > 4<<20 {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.IndexByte(string(b[:min(len(b), 8000)]), 0) >= 0 {
			return nil // binary
		}
		for _, id := range rivianID.FindAllString(string(b), -1) {
			if !placeholder.MatchString(id) {
				t.Errorf("%s: a Rivian vehicle id that isn't a placeholder; use 01-0000000NN", rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
