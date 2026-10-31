package main

import "github.com/apohor/rivolt/internal/fence"

// liveFence is the group-lease fence every consumer built before the lease store exists reads through: the Live Activity source, the
// left-open job and the OTA and receipts syncers. They ask it on every use, so installing the store (Set) reaches them. Before that
// it answers Off, or Lost in lead mode (Require), so none of them acts for a group this pod may not hold during boot.
var liveFence fence.Holder

// mayPushFor says whether this pod may send a live update for the vehicle: only with lead mode off
// (as before) or while it holds the vehicle's group. A pod that has just lost or self-fenced the
// group must not keep pushing figures from state up to 10 minutes old to someone's lock screen.
func mayPushFor(f fence.Source, vehicleID string) bool {
	_, st := f.Fence(vehicleID)
	return st == fence.Off || st == fence.Held
}
