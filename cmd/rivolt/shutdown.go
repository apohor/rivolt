package main

import (
	"log/slog"
	"os"
	"time"
)

// The shutdown budget. The pod gets a 45 s grace period (terminationGracePeriod)
// and the chart's preStop hook spends 5 s of it before our signal arrives; every
// step below is bounded, and TestShutdownBudgetFitsTheGracePeriod holds their sum
// under the rest. Change one and the test says what has to give.
const (
	gracePeriod       = 45 * time.Second
	preStopSleep      = 5 * time.Second // charts: lifecycle.preStop (`rivolt prestop --sleep`)
	drainFlushTimeout = 2 * time.Second // write the in-progress sessions the throttle held back
	// leaseReleaseTimeout covers stopping every subscription and saving the final snapshots (4 s)
	// and then releasing every group and vehicle row (2 s).
	leaseReleaseTimeout = 6 * time.Second
	httpDrainTimeout    = 10 * time.Second
	// bgShutdownWait bounds how long shutdown waits for fire-and-forget work.
	bgShutdownWait = 8 * time.Second
)

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
