package main

import (
	"log/slog"
	"os"
	"time"
)

// bgShutdownWait bounds how long shutdown waits for fire-and-forget work.
const bgShutdownWait = 10 * time.Second

// exitOnSecondSignal exits at once when a second termination signal arrives while
// a graceful shutdown is under way: an operator who sends SIGTERM twice means it,
// and a stuck drain must not run out the whole grace period.
func exitOnSecondSignal(sigs <-chan os.Signal, exit func(code int), logger *slog.Logger) {
	if _, ok := <-sigs; !ok {
		return
	}
	logger.Warn("second termination signal: exiting now, without finishing the shutdown")
	exit(1)
}
