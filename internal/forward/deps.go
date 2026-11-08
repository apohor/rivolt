package forward

import (
	"context"
	"encoding/json"
	"time"
)

// LeaseOwner is what a requester learns about a group's owner from the lease row.
type LeaseOwner struct {
	Found     bool
	Pod       string
	Epoch     int64
	ExpiresAt time.Time
}

// LeaseView is how the forwarder reads the lease. It only reads: no acquire in M3 (C2). The implementation reads the lease row on
// the SYSTEM pool (C3: row-level security on the user pool could hide the row and read as "no owner"), cached for 2 s, and asks
// the local lease store about this pod.
type LeaseView interface {
	// Owner is the current owner of the group, from the lease row.
	Owner(ctx context.Context, group string) (LeaseOwner, error)
	// Held reports whether this pod holds the group, and at which epoch.
	Held(group string) (epoch int64, ok bool)
	// PastDeadline reports whether this pod's local lease deadline has passed (the pod must stop acting).
	PastDeadline() bool
}

// Result is what a Runner returns for a kind: the HTTP-ish status, the error class the handlers already map (empty on success)
// and the kind-specific body.
type Result struct {
	Status int
	Class  string
	Body   json.RawMessage
}

// Runner executes a kind for a user on this pod: the same code the handler runs today. args is what the kind's Validate returned.
type Runner interface {
	Run(ctx context.Context, kind Kind, user, group string, args any) (Result, error)
}

// Clock is Redis's clock. Pods never compare wall clocks (design §4.1): the requester stamps `expires_ms` from it and the owner
// compares against it at dequeue.
type Clock interface {
	RedisTimeMS(ctx context.Context) (int64, error)
}
