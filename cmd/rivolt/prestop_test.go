package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPrestopSignalsThePIDThenSleeps(t *testing.T) {
	var order []string
	var gotPID int
	var gotSig syscall.Signal
	err := prestop(7, 5*time.Second,
		func(pid int, sig syscall.Signal) error {
			order = append(order, "kill")
			gotPID, gotSig = pid, sig
			return nil
		},
		func(d time.Duration) { order = append(order, "sleep:"+d.String()) })
	if err != nil {
		t.Fatal(err)
	}
	if gotPID != 7 || gotSig != syscall.SIGUSR1 {
		t.Errorf("signalled pid %d with %v, want 7 with SIGUSR1", gotPID, gotSig)
	}
	if strings.Join(order, ",") != "kill,sleep:5s" {
		t.Errorf("order = %v", order)
	}
}

func TestPrestopSleepsEvenWhenTheSignalFails(t *testing.T) {
	slept := false
	err := prestop(1, time.Second,
		func(int, syscall.Signal) error { return errors.New("no such process") },
		func(time.Duration) { slept = true })
	if err == nil || !slept {
		t.Errorf("err=%v slept=%v, want the error and a sleep (the Endpoints removal still needs its time)", err, slept)
	}
}

func TestPrestopBadFlagIsUsageError(t *testing.T) {
	var buf bytes.Buffer
	if code := runPrestop([]string{"--nope"}, &buf); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
}

// SIGUSR1 reaches the drain handler once per signal, and the process survives it.
func TestWatchDrainOnSIGUSR1(t *testing.T) {
	sigs := notifyDrain()
	got := make(chan struct{}, 2)
	go watchDrain(sigs, func() { got <- struct{}{} })
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGUSR1 did not reach the drain handler")
	}
}

// A drain cannot be started over the network: the server opens exactly the listeners it
// opened before (the HTTP address and the metrics address), and the drain code opens none.
func TestNoNewListeners(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	listen := regexp.MustCompile(`net\.Listen\w*\(|\.ListenAndServe(TLS)?\(|\.Serve(TLS)?\(|httptest\.NewServer`)
	var hits []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
				continue
			}
			if listen.MatchString(line) {
				hits = append(hits, f+":"+strings.TrimSpace(line))
			}
		}
	}
	sort.Strings(hits)
	if len(hits) != 2 {
		t.Fatalf("listeners opened by the server = %d, want 2 (HTTP and metrics): %v", len(hits), hits)
	}
	for _, h := range hits {
		if strings.HasPrefix(h, "prestop.go") {
			t.Errorf("the drain code opens a listener: %s", h)
		}
	}
}

func TestUnknownSubcommandIsNotTheServer(t *testing.T) {
	for arg, want := range map[string]bool{"prestop2": true, "serve": true, "": false, "-addr": false, "--addr=:8080": false} {
		if got := isUnknownSubcommand(arg); got != want {
			t.Errorf("isUnknownSubcommand(%q) = %v, want %v", arg, got, want)
		}
	}
}
