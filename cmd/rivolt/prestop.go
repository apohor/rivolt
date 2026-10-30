package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// `rivolt prestop` is the chart's preStop hook (an exec, not an HTTP endpoint: the kubelet runs
// httpGet hooks against the pod IP, so anything that could reach the port could force a drain).
// It tells the server (PID 1 in the container) to start draining with SIGUSR1, then sleeps the
// time the old sleep hook spent letting the pod drop out of the Service's Endpoints. The server
// opens no listener for this.

// serverPID is the server's PID inside the container: the image's ENTRYPOINT runs it directly.
const serverPID = 1

// runPrestop parses the hook's arguments and runs it. Returns the exit code.
func runPrestop(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("prestop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sleep := fs.Duration("sleep", preStopSleep, "how long to wait after signalling, for the Endpoints removal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := prestop(serverPID, *sleep, syscall.Kill, time.Sleep); err != nil {
		fmt.Fprintln(stderr, "prestop:", err)
		// The pod is terminating either way; still wait so the Endpoints removal has time.
		time.Sleep(*sleep)
		return 0
	}
	return 0
}

// prestop signals pid with SIGUSR1 and sleeps. kill and sleep are injected for the test.
func prestop(pid int, sleep time.Duration, kill func(pid int, sig syscall.Signal) error, sleepFn func(time.Duration)) error {
	err := kill(pid, syscall.SIGUSR1)
	sleepFn(sleep)
	return err
}

// watchDrain calls drain once for every SIGUSR1 on sigs. The signal must be registered before the
// server can be sent one: an unhandled SIGUSR1 ends a Go process.
func watchDrain(sigs <-chan os.Signal, drain func()) {
	for range sigs {
		drain()
	}
}

func notifyDrain() chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	return ch
}
