package main

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// grant-pro is a registered subcommand, and a misspelling of it is an unknown command, never the server: main() starts the server for a
// flag or no argument only.
func TestGrantProIsRegisteredAndMisspellingsAreNotTheServer(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`case "grant-pro":\s+os\.Exit\(runGrantPro\(os\.Args\[2:\]\)\)`).Match(src) {
		t.Error(`main() has no case "grant-pro" calling runGrantPro`)
	}
	for _, word := range []string{"grant-pro", "grant_pro", "grantpro", "grant-Pro", "grant-pro ", "grant", "grant-prp", "revoke-pro"} {
		if !isUnknownSubcommand(word) {
			t.Errorf("isUnknownSubcommand(%q) = false: that word would start the server", word)
		}
	}
}

// The real main(), in a child process: a misspelt subcommand exits 2 with "unknown command" and starts nothing. A server would sit there
// until the deadline kills it, which fails the test.
func TestMisspeltGrantProExitsWithoutStartingAServer(t *testing.T) {
	if os.Getenv("RIVOLT_TEST_CHILD_ARGS") != "" {
		os.Args = append([]string{"rivolt"}, strings.Fields(os.Getenv("RIVOLT_TEST_CHILD_ARGS"))...)
		main()
		return
	}
	for _, args := range []string{"grant_pro --email a@b.c --until 2027-01-01 --reason x", "grantpro", "grant-prp --dry-run"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMisspeltGrantProExitsWithoutStartingAServer$")
		cmd.Env = append(os.Environ(), "RIVOLT_TEST_CHILD_ARGS="+args, "DATABASE_URL=", "DB_HOST=")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 2 || !strings.Contains(stderr.String(), "unknown command") {
				t.Errorf("%q: exit %v, stderr %q; want exit 2 and unknown command", args, err, stderr.String())
			}
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("%q: still running after 20 s: the server was started", args)
		}
	}
}

// Flag mistakes end the run with the usage code before any database is opened (no DSN is set here, so a run that got past the flags would
// fail with exit 1 on the connection instead).
func TestGrantProFlagMistakesAreUsageErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "")
	for name, args := range map[string][]string{
		"no flags":             {},
		"no email":             {"--until", "2099-01-01", "--reason", "x"},
		"no reason":            {"--email", "a@b.c", "--until", "2099-01-01"},
		"no until":             {"--email", "a@b.c", "--reason", "x"},
		"an unknown flag":      {"--email", "a@b.c", "--until", "2099-01-01", "--reason", "x", "--apply"},
		"a stray argument":     {"--email", "a@b.c", "--until", "2099-01-01", "--reason", "x", "more"},
		"revoke with an end":   {"--revoke", "--email", "a@b.c", "--until", "2099-01-01", "--reason", "x"},
		"an address in reason": {"--email", "a@b.c", "--until", "2099-01-01", "--reason", "for a@b.c"},
		"two addresses":        {"--email", "a@b.c,d@e.f", "--until", "2099-01-01", "--reason", "x"},
	} {
		if code := runGrantPro(args); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}
