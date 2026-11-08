package forward

import (
	"fmt"
	"regexp"
)

// DefaultPrefix is the key prefix. The Valkey user `rivolt` may touch `~rivolt:*` only, so every forwarder key starts with it.
const DefaultPrefix = "rivolt"

// Reply and request lifetimes (seconds).
const (
	RequestListTTL = 45 // the longest kind timeout plus slack; refreshed on every push
	ReplyTTL       = 5  // the requester is already blocked reading when the owner answers
	UserSetTTL     = 60 // the per-user set of reply ids, for purge on account deletion
	ReadyTTL       = 15 // the owner's readiness marker; refreshed every ReadyEvery
	ReadyEvery     = 5
)

// MaxQueue is the owner list length above which a requester refuses to push (503, Retry-After: 5).
const MaxQueue = 100

var podRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidPod reports whether s can be a pod id inside a key.
func ValidPod(s string) bool { return podRE.MatchString(s) }

// Keys builds the Redis key names. The zero value uses DefaultPrefix.
type Keys struct{ Prefix string }

func (k Keys) p() string {
	if k.Prefix == "" {
		return DefaultPrefix
	}
	return k.Prefix
}

// Queue is the owner's request list.
func (k Keys) Queue(pod string) string { return fmt.Sprintf("%s:fwd:%s", k.p(), pod) }

// Ready is the owner's readiness marker.
func (k Keys) Ready(pod string) string { return fmt.Sprintf("%s:fwd:ready:%s", k.p(), pod) }

// Reply is the one-shot reply list of request id. The id must have passed ValidID.
func (k Keys) Reply(id string) string { return fmt.Sprintf("%s:fwd:reply:%s", k.p(), id) }

// UserSet is the set of in-flight reply ids of a user (purged with the account).
func (k Keys) UserSet(user string) string { return fmt.Sprintf("%s:fwd:u:%s", k.p(), user) }
