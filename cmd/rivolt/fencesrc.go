package main

import (
	"sync/atomic"

	"github.com/apohor/rivolt/internal/fence"
)

// liveFence is the group-lease fence the Live Activity source asks. The source starts before the
// lease store exists, so it reads the store through this holder: Off until Set.
var liveFence atomicFence

type atomicFence struct{ p atomic.Pointer[fence.Source] }

// Set installs the fence source (the lease store).
func (a *atomicFence) Set(s fence.Source) { a.p.Store(&s) }

// Fence answers from the installed source, Off before there is one (lead mode off, or still booting).
func (a *atomicFence) Fence(vehicleID string) (fence.Token, fence.State) {
	if p := a.p.Load(); p != nil {
		return (*p).Fence(vehicleID)
	}
	return fence.Token{}, fence.Off
}

// mayPushFor says whether this pod may send a live update for the vehicle: only with lead mode off
// (as before) or while it holds the vehicle's group. A pod that has just lost or self-fenced the
// group must not keep pushing figures from state up to 10 minutes old to someone's lock screen.
func mayPushFor(f fence.Source, vehicleID string) bool {
	_, st := f.Fence(vehicleID)
	return st == fence.Off || st == fence.Held
}
