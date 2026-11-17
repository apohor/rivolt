package main

import (
	"log/slog"
	"os"
	"time"
)

// The shutdown budget. The pod gets a 75 s grace period (terminationGracePeriodSeconds)
// and the chart's preStop hook spends 5 s of it before our signal arrives; every
// step below is bounded, and TestShutdownBudgetFitsTheGracePeriod holds their sum
// under the rest. Change one and the test says what has to give.
//
// The HTTP drain is 35 s so a request on the 30 s timeout group (a trip plan) that was in flight when the pod was told to stop still
// finishes. It runs alongside the flush and the lease release, not after them: waiting for a slow request before releasing the vehicles
// would stretch the telemetry gap of every vehicle on the pod by as much. (The sum below counts it as a step anyway: the cheap bound.)
const (
	gracePeriod       = 75 * time.Second
	preStopSleep      = 5 * time.Second // charts: lifecycle.preStop (`rivolt prestop --sleep`)
	drainFlushTimeout = 2 * time.Second // write the in-progress sessions the throttle held back
	// leaseReleaseTimeout covers stopping every subscription and saving the final snapshots (4 s)
	// and then releasing every group and vehicle row (2 s).
	leaseReleaseTimeout = 6 * time.Second
	httpDrainTimeout    = 35 * time.Second
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

// ingestDriveWaitCap is how long a draining pod waits for the drives on its groups to end before it moves them (P2b, RIVOLT_INGEST_DRIVE_WAIT_CAP,
// a Go duration). 0, the default, waits for none: a drive moves at shutdown, as it did before. Set it together with the pod's
// terminationGracePeriodSeconds (the cap plus the budget above): a cap longer than the grace period would be cut by SIGKILL with the
// groups still held.
func ingestDriveWaitCap() time.Duration {
	d, err := time.ParseDuration(os.Getenv("RIVOLT_INGEST_DRIVE_WAIT_CAP"))
	if err != nil || d < 0 {
		return 0
	}
	return d
}
