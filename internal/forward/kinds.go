package forward

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/apohor/rivolt/internal/settings"
)

// Kind is a forwarded operation. The allowlist is closed: an unknown kind is dropped and counted rejected.
type Kind string

const (
	KindPlanLegs     Kind = "plan_legs"     // one planTrip call (the requester keeps the cache and the quota)
	KindFreshState   Kind = "fresh_state"   // one vehicle-state fetch
	KindAdopt        Kind = "adopt"         // best-effort nudge after a login: user and group only, never credentials
	KindReceiptsSync Kind = "receipts_sync" // one receipts summaries pass after a user opts in: user and group only, no reply (owner runs Sync, the 30 min claim caps it)
	KindOTANotes     Kind = "ota_notes"     // one OTA release-notes URL lookup (not stored, not cached by the requester)
	KindLiveSession  Kind = "live_session"  // one read of the owner's live charging session for a vehicle (no Rivian call: the owner's monitor answers)
)

// Spec is what the allowlist holds for a kind.
type Spec struct {
	// Timeout bounds the owner's work and the requester's wait. It stays at least 2 s under the handler deadline of every route that
	// forwards the kind (the API's 30 s group deadline; see TestForwardKindTimeoutsFitTheirRoutes), or the requester would be cut
	// off before it could answer or retry.
	Timeout time.Duration
	// Retry says whether a timeout or a `moved` reply may be retried once (reads and version-CAS: yes; adopt: never).
	Retry bool
	// Validate strictly decodes the args (unknown fields rejected) and checks types, lengths and ranges. It returns the decoded
	// value on success.
	Validate func(args json.RawMessage) (any, error)
	// NoReply says no requester waits for an answer, so the owner writes none (adopt, receipts_sync).
	NoReply bool
	// DrainWait caps how long a call of this kind waits for a draining owner's successor (0: the requester's own ceiling). A kind whose
	// caller polls soon again keeps it short so the waits do not stack.
	DrainWait time.Duration
}

// Kinds is the allowlist.
var Kinds = map[Kind]Spec{
	KindPlanLegs:     {Timeout: 28 * time.Second, Retry: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[PlanLegsArgs](a) }},
	KindFreshState:   {Timeout: 10 * time.Second, Retry: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[FreshStateArgs](a) }},
	KindAdopt:        {Timeout: 5 * time.Second, Retry: false, NoReply: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[AdoptArgs](a) }},
	KindReceiptsSync: {Timeout: 25 * time.Second, Retry: false, NoReply: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[ReceiptsSyncArgs](a) }},
	KindOTANotes:     {Timeout: 15 * time.Second, Retry: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[OTANotesArgs](a) }},
	KindLiveSession:  {Timeout: 4 * time.Second, DrainWait: 4 * time.Second, Retry: true, Validate: func(a json.RawMessage) (any, error) { return decodeArgs[LiveSessionArgs](a) }},
}

// ErrBadArgs: args that fail the kind's schema.
var ErrBadArgs = errors.New("forward: bad args")

// Validatable is implemented by every args type.
type Validatable interface{ validate() error }

func decodeArgs[T any, P interface {
	*T
	Validatable
}](raw json.RawMessage) (*T, error) {
	var v T
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil || d.More() {
		return nil, fmt.Errorf("%w: not the kind's shape", ErrBadArgs)
	}
	if err := P(&v).validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadArgs, err)
	}
	return &v, nil
}

var vehicleIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func checkVehicle(id string) error {
	if !vehicleIDRE.MatchString(id) {
		return errors.New("vehicle id")
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func inRange(f, lo, hi float64) bool { return finite(f) && f >= lo && f <= hi }

// Limits for plan_legs, mirroring what the planner accepts (rivian.MaxPlanWaypoints is 10 including origin and destination).
const (
	maxWaypoints   = 10
	maxNetworkPref = 64
	maxShortString = 128
)

// PlanLegsArgs mirrors rivian.PlanTripInput (the fields the planner call uses).
type PlanLegsArgs struct {
	VehicleID               string        `json:"vehicle_id"`
	StartingSoC             float64       `json:"starting_soc"`
	StartingRangeMeters     float64       `json:"starting_range_meters"`
	OriginBearing           float64       `json:"origin_bearing"`
	Waypoints               []Waypoint    `json:"waypoints"`
	TargetArrivalSocPercent *float64      `json:"target_arrival_soc_percent,omitempty"`
	NetworkPreferences      []NetworkPref `json:"network_preferences,omitempty"`
	DriveMode               string        `json:"drive_mode,omitempty"`
	HasAdapter              *bool         `json:"has_adapter,omitempty"`
	RoutePreferences        *RoutePrefs   `json:"route_preferences,omitempty"`
	DepartureTimeMS         int64         `json:"departure_time_ms,omitempty"`
}

// Waypoint is one stop.
type Waypoint struct {
	Latitude     float64 `json:"lat"`
	Longitude    float64 `json:"lon"`
	WaypointType string  `json:"type,omitempty"`
	EntityID     string  `json:"entity_id,omitempty"`
}

// NetworkPref is a charging-network preference.
type NetworkPref struct {
	NetworkID  string `json:"network_id"`
	Preference int    `json:"preference"`
}

// RoutePrefs are the route-avoidance flags.
type RoutePrefs struct {
	AvoidTolls    bool `json:"avoid_tolls"`
	AvoidFerries  bool `json:"avoid_ferries"`
	AvoidHighways bool `json:"avoid_highways"`
}

// driveModes are the drive modes a planner request may carry: built from settings.DriveModes, the list the trip handlers let through (they drop
// anything unknown to ""), so the two cannot differ. (This was once a copy of Rivian's older enum, CONSERVE/SPORT/ALL_PURPOSE, which rejected
// EVERYDAY and DISTANCE, the web's defaults: the cause of the Nov 8 preview rollback.)
var driveModes = func() map[string]bool {
	m := map[string]bool{"": true}
	for _, d := range settings.DriveModes {
		m[d] = true
	}
	return m
}()

// ValidDriveMode reports whether a forwarded plan may carry mode.
func ValidDriveMode(mode string) bool { return driveModes[mode] }

func (a *PlanLegsArgs) validate() error {
	if err := checkVehicle(a.VehicleID); err != nil {
		return err
	}
	if !inRange(a.StartingSoC, 0, 100) {
		return errors.New("starting soc")
	}
	if !inRange(a.StartingRangeMeters, 0, 2_000_000) || !inRange(a.OriginBearing, 0, 360) {
		return errors.New("range or bearing")
	}
	if len(a.Waypoints) < 2 || len(a.Waypoints) > maxWaypoints {
		return errors.New("waypoint count")
	}
	for _, w := range a.Waypoints {
		if !inRange(w.Latitude, -90, 90) || !inRange(w.Longitude, -180, 180) {
			return errors.New("waypoint coordinates")
		}
		if len(w.WaypointType) > maxShortString || len(w.EntityID) > maxShortString || !utf8.ValidString(w.WaypointType) || !utf8.ValidString(w.EntityID) {
			return errors.New("waypoint text")
		}
	}
	if a.TargetArrivalSocPercent != nil && !inRange(*a.TargetArrivalSocPercent, 0, 100) {
		return errors.New("target soc")
	}
	if len(a.NetworkPreferences) > maxNetworkPref {
		return errors.New("network preferences")
	}
	for _, n := range a.NetworkPreferences {
		if len(n.NetworkID) == 0 || len(n.NetworkID) > maxShortString || !utf8.ValidString(n.NetworkID) || n.Preference < -1000 || n.Preference > 1000 {
			return errors.New("network preference")
		}
	}
	if !driveModes[a.DriveMode] {
		return errors.New("drive mode")
	}
	if a.DepartureTimeMS < 0 {
		return errors.New("departure time")
	}
	return nil
}

// FreshStateArgs: one vehicle-state fetch. MaxAgeMS lets the owner serve its cache when it is fresh enough.
type FreshStateArgs struct {
	VehicleID string `json:"vehicle_id"`
	MaxAgeMS  int64  `json:"max_age_ms"`
}

func (a *FreshStateArgs) validate() error {
	if err := checkVehicle(a.VehicleID); err != nil {
		return err
	}
	if a.MaxAgeMS < 0 || a.MaxAgeMS > 10*60*1000 {
		return errors.New("max age")
	}
	return nil
}

// AdoptArgs: nothing. The message's user and group are the whole content, and no credential, token or cookie ever rides it.
type AdoptArgs struct{}

func (a *AdoptArgs) validate() error { return nil }

// ReceiptsSyncArgs: nothing. The message's user and group are the whole content; no credential ever rides it.
type ReceiptsSyncArgs struct{}

func (a *ReceiptsSyncArgs) validate() error { return nil }

// OTANotesArgs: one release-notes URL lookup.
type OTANotesArgs struct {
	VehicleID string `json:"vehicle_id"`
	ReleaseID string `json:"release_id,omitempty"`
}

func (a *OTANotesArgs) validate() error {
	if err := checkVehicle(a.VehicleID); err != nil {
		return err
	}
	if len(a.ReleaseID) > maxShortString || !utf8.ValidString(a.ReleaseID) {
		return errors.New("release id")
	}
	return nil
}

// LiveSessionArgs: one live-session read.
type LiveSessionArgs struct {
	VehicleID string `json:"vehicle_id"`
}

func (a *LiveSessionArgs) validate() error { return checkVehicle(a.VehicleID) }

// Check is the owner's whole admission test for a decoded request, in order: the signature, then the kind, then the args. It
// returns the decoded args on success. Nothing before the signature touches the message's content.
func Check(s *Signer, r *Request) (any, error) {
	if err := s.VerifyRequest(r); err != nil {
		return nil, err
	}
	spec, ok := Kinds[r.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: unknown kind", ErrBadMessage)
	}
	return spec.Validate(r.Args)
}

// ExpiresMS is the Redis-clock expiry of a request of kind: Redis TIME in ms plus the kind's timeout.
func ExpiresMS(redisNowMS int64, k Kind) int64 {
	return redisNowMS + Kinds[k].Timeout.Milliseconds()
}

// Expired reports whether a request's expiry has passed on the Redis clock. Pods never compare wall clocks.
func Expired(redisNowMS, expiresMS int64) bool { return redisNowMS >= expiresMS }
