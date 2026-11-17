package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Arguments are refused before any database is opened, so these need none.
func TestLiveDestinationUsersArgs(t *testing.T) {
	const id = "3f2b8c1e-5a4d-4e6f-9a0b-1c2d3e4f5a6b"
	for _, c := range []struct {
		name string
		args []string
	}{
		{"nothing", nil},
		{"all", []string{"--add", "all"}},
		{"a name", []string{"--add", "alice"}},
		{"remove a name", []string{"--remove", "alice"}},
		{"two modes", []string{"--add", id, "--remove", id}},
		{"show and add", []string{"--show", "--add", id}},
		{"stray argument", []string{"--show", "extra"}},
		{"unknown flag", []string{"--all"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := runLiveDestinationUsers(c.args, &out, &errb); code != 2 {
				t.Fatalf("exit %d (%s), want 2", code, strings.TrimSpace(errb.String()))
			}
			if out.Len() != 0 {
				t.Fatalf("printed %q on a refused call", out.String())
			}
		})
	}
}

// The subcommand is in the dispatch switch: an unknown word must never start the server, a known one must not be unknown.
func TestLiveDestinationUsersIsASubcommand(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `case "live-destination-users":`) {
		t.Fatal("live-destination-users is not in main's subcommand switch")
	}
}
