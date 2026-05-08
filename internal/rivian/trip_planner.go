package rivian

import (
	"context"
	"encoding/json"
	"fmt"
)

// PlanTripInput is the request shape for PlanTrip. Mirrors the
// arguments accepted by the Rivian gateway's planTripWithMultiStop
// operation. All required fields are non-pointer; optional fields
// (e.g. TargetArrivalSocPercent, TrailerProfile) are pointers so the
// zero value is distinguishable from "not provided".
//
// Waypoints[0] is the origin, Waypoints[len-1] is the destination.
// Slice 1 of the planner only supports those two; intermediate stops
// aren't part of this schema's planTrip operation. The gateway
// computes charging stops between origin and destination.
type PlanTripInput struct {
	VehicleID               string
	StartingSoC             float64
	StartingRangeMeters     float64 // 0 → derived from SoC by PlanTrip
	OriginBearing           float64 // degrees from true north; pass 0 if unknown
	Waypoints               []PlanTripWaypoint
	TargetArrivalSocPercent *float64
	NetworkPreferences      []NetworkPreference
	// Fields kept on the input for SPA-side compat / future schemas;
	// not forwarded to Rivian's current planTrip schema.
	DriveMode               string
	TrailerProfile          string
	AvoidAdapterRequired    bool
	SupportedConnectorTypes []string
}

// PlanTripWaypoint is one input waypoint. waypointType is one of
// "origin", "destination", or "waypoint" (intermediate stop).
// EntityID is optional and used when the user picked a specific POI
// from a places search; the gateway uses it as a hint.
type PlanTripWaypoint struct {
	Latitude     float64
	Longitude    float64
	WaypointType string
	EntityID     string
}

// NetworkPreference biases the planner toward or away from a charging
// network. Preference is an integer rank: lower = more preferred.
type NetworkPreference struct {
	NetworkID  string
	Preference int
}

// TripPlan is the cleaned-up response. The gateway returns one or
// more candidate routes; first is typically the recommended one.
type TripPlan struct {
	Status                  string
	ChargeStationsAvailable bool
	SoCBelowLimit           bool
	Routes                  []TripRoute
}

// TripRoute is one candidate route. Charging stops are inside
// Waypoints (filtered to entries whose waypointType identifies a
// charger).
type TripRoute struct {
	DestinationReached       bool
	TotalChargingDurationSec int
	ArrivalSoC               float64
	ArrivalReachableMeters   float64
	EnergyConsumptionKWh     float64
	BatteryEmptyToDestMeters float64
	BatteryEmptyLat          float64
	BatteryEmptyLon          float64
	// RouteResponseRaw holds the gateway's geometry payload verbatim
	// so the SPA / map renderer can decode it without a round-trip.
	// Shape is opaque to us (provider-specific encoded polyline +
	// metadata); we forward it as-is.
	RouteResponseRaw any
	Waypoints        []PlannedWaypoint
}

// PlannedWaypoint is one stop on the planned route. Origin and
// destination entries appear with WaypointType="origin"/"destination";
// every other entry is a charger pick.
type PlannedWaypoint struct {
	WaypointType             string
	EntityID                 string
	Name                     string
	Latitude                 float64
	Longitude                float64
	MaxPowerKW               float64
	ChargeDurationSec        int
	ArrivalSoC               float64
	DepartureSoC             float64
	ArrivalReachableMeters   float64
	DepartureReachableMeters float64
}

// PlanTrip runs the gateway's planTripWithMultiStop operation and
// returns a flattened, JSON-friendly TripPlan. Per-user — the
// authenticated session must own the vehicleId.
//
// Schema source: jrgutier/rivian-python-client/src/rivian/schemas/gateway.graphql.
// The operation is `planTrip` (not `planTripMultiStop` — the docs at
// rivian-api.kaedenb.org described a non-existent or broken sibling),
// taking scalar origin+destination CoordinatesInput args (not a
// waypoints array), `bearing` (not `originBearing`), and required
// startingRangeMeters. driveMode/trailerProfile/avoidAdapterRequired/
// supportedConnectorTypes don't exist on this schema at all.
func (c *LiveClient) PlanTrip(ctx context.Context, in PlanTripInput) (*TripPlan, error) {
	if err := c.checkUpstream(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	if in.VehicleID == "" {
		return nil, fmt.Errorf("planTrip: vehicleId required")
	}
	if len(in.Waypoints) < 2 {
		return nil, fmt.Errorf("planTrip: origin + destination waypoints required")
	}
	vars := planTripVars{
		Vehicle: in.VehicleID,
	}
	for _, wp := range in.Waypoints {
		vars.Waypoints = append(vars.Waypoints, planTripWaypointVar{
			Latitude:  wp.Latitude,
			Longitude: wp.Longitude,
		})
	}
	if in.StartingSoC > 0 {
		v := in.StartingSoC
		vars.StartingSoc = &v
	}
	if in.StartingRangeMeters > 0 {
		v := in.StartingRangeMeters
		vars.StartingRangeMeters = &v
	}
	if in.TargetArrivalSocPercent != nil {
		v := *in.TargetArrivalSocPercent
		vars.TargetArrivalSocPercent = &v
	}
	if in.DriveMode != "" {
		v := in.DriveMode
		vars.DriveMode = &v
	}
	if in.TrailerProfile != "" {
		v := in.TrailerProfile
		vars.TrailerProfile = &v
	}
	for _, np := range in.NetworkPreferences {
		vars.NetworkPreferences = append(vars.NetworkPreferences, planTripNetworkPrefVar{
			NetworkID:  np.NetworkID,
			Preference: np.Preference,
		})
	}

	data, err := doGraphQL[planTripData](ctx, c, graphQLRequest{
		OperationName: "planTripWithMultiStopV2",
		Query:         qPlanTripWithMultiStop,
		Variables:     vars,
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("planTrip2: %w", err)
	}

	// Map the v2 response (status / plans[].summary / plans[].waypoints)
	// into the existing TripPlan / TripRoute / PlannedWaypoint shape so
	// the SPA contract stays stable. Each v2 plan becomes one TripRoute;
	// summary fields land on TripRoute, waypoint fields on PlannedWaypoint.
	out := &TripPlan{
		Status: data.PlanTrip2.Status,
		Routes: make([]TripRoute, 0, len(data.PlanTrip2.Plans)),
	}
	// v2 doesn't expose `socBelowLimit` / `chargeStationsAvailable` at
	// the top level — instead it's per-plan. Surface true if any plan
	// flagged it, so the SPA's existing risk display still lights up.
	for _, p := range data.PlanTrip2.Plans {
		if p.Summary.SocBelowLimitAtDestination {
			out.SoCBelowLimit = true
		}
	}
	for _, p := range data.PlanTrip2.Plans {
		route := TripRoute{
			DestinationReached:       p.Summary.DestinationReachable,
			TotalChargingDurationSec: p.Summary.TotalChargeDurationSeconds,
			ArrivalSoC:               p.Summary.ArrivalSOCPercent,
			ArrivalReachableMeters:   p.Summary.ArrivalRangeMeters,
			// EnergyConsumptionKWh on the v1 schema was per-leg; v2's
			// arrivalEnergyKwh is the projected energy *remaining* at
			// destination, not consumed. We don't have an exact
			// "consumed" field; leave 0 unless the SPA computes it
			// from start/arrival deltas.
			EnergyConsumptionKWh: 0,
			Waypoints:            make([]PlannedWaypoint, 0, len(p.Waypoints)),
		}
		for _, w := range p.Waypoints {
			route.Waypoints = append(route.Waypoints, PlannedWaypoint{
				WaypointType:             w.WaypointType,
				Latitude:                 w.Latitude,
				Longitude:                w.Longitude,
				ArrivalSoC:               w.ArrivalSOCPercent,
				DepartureSoC:             w.DepartureSOCPercent,
				ArrivalReachableMeters:   w.ArrivalRangeMeters,
				DepartureReachableMeters: w.DepartureRangeMeters,
			})
		}
		out.Routes = append(out.Routes, route)
	}
	// v2 has no top-level chargeStationsAvailable; infer from the
	// presence of charging waypoints across plans.
	for _, r := range out.Routes {
		for _, w := range r.Waypoints {
			if w.WaypointType != "" && w.WaypointType != "ORIGIN" && w.WaypointType != "DESTINATION" && w.WaypointType != "WAYPOINT" {
				out.ChargeStationsAvailable = true
				break
			}
		}
	}
	return out, nil
}

// --- wire types ----------------------------------------------------

type planTripVars struct {
	Waypoints               []planTripWaypointVar    `json:"waypoints"`
	Vehicle                 string                   `json:"vehicle"`
	StartingSoc             *float64                 `json:"startingSoc,omitempty"`
	StartingRangeMeters     *float64                 `json:"startingRangeMeters,omitempty"`
	TargetArrivalSocPercent *float64                 `json:"targetArrivalSocPercent,omitempty"`
	DriveMode               *string                  `json:"driveMode,omitempty"`
	NetworkPreferences      []planTripNetworkPrefVar `json:"networkPreferences,omitempty"`
	TrailerProfile          *string                  `json:"trailerProfile,omitempty"`
	HasAdapter              *bool                    `json:"hasAdapter,omitempty"`
}

// planTripWaypointVar is the wire shape of one CoordinatesInput.
// Confirmed by /api/admin/trips/plan/diag (v0.17.118): the input
// is *just* {latitude, longitude}. The waypointType + entityId
// fields the rivian-api.kaedenb.org docs listed as inputs are
// actually response-only — the docs page conflated them. Sending
// either field triggers BAD_USER_INPUT (one per waypoint), while a
// bare {lat, lon} payload passes input validation and progresses to
// the planner's runtime stage. Origin vs destination is positional
// (index 0 = origin, last = destination, intermediate = stops).
type planTripWaypointVar struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type planTripNetworkPrefVar struct {
	NetworkID  string `json:"networkId"`
	Preference int    `json:"preference"`
}

type planTripData struct {
	PlanTrip2 struct {
		Status string            `json:"status"`
		Plans  []planTripPlanRow `json:"plans"`
	} `json:"planTrip2"`
}

type planTripPlanRow struct {
	Summary   planTripSummaryRow    `json:"summary"`
	Waypoints []planTripWaypointRow `json:"waypoints"`
}

type planTripSummaryRow struct {
	DestinationReachable        bool    `json:"destinationReachable"`
	SocBelowLimitAtDestination  bool    `json:"socBelowLimitAtDestination"`
	TotalChargeDurationSeconds  int     `json:"totalChargeDurationSeconds"`
	TotalDriveDurationSeconds   int     `json:"totalDriveDurationSeconds"`
	TotalDriveDistanceMeters    float64 `json:"totalDriveDistanceMeters"`
	TotalTripDurationSeconds    int     `json:"totalTripDurationSeconds"`
	ArrivalSOCPercent           float64 `json:"arrivalSOCPercent"`
	ArrivalRangeMeters          float64 `json:"arrivalRangeMeters"`
	ArrivalEnergyKwh            float64 `json:"arrivalEnergyKwh"`
}

type planTripWaypointRow struct {
	WaypointType         string  `json:"waypointType"`
	Latitude             float64 `json:"latitude"`
	Longitude            float64 `json:"longitude"`
	ArrivalSOCPercent    float64 `json:"arrivalSOCPercent"`
	DepartureSOCPercent  float64 `json:"departureSOCPercent"`
	ArrivalRangeMeters   float64 `json:"arrivalRangeMeters"`
	DepartureRangeMeters float64 `json:"departureRangeMeters"`
}

// RawGraphQL posts an arbitrary operation+query+variables to the
// gateway and returns the response data verbatim. Admin-only
// debugging primitive for trying different operation names,
// response selection sets, etc. without round-tripping through
// chart bumps.
func (c *LiveClient) RawGraphQL(ctx context.Context, operationName, query string, variables map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	// Admin debug path: bypass the shared breaker so a fan-out of
	// diagnostic failures can't poison the production hot path.
	data, err := doGraphQL[map[string]any](withBypassBreaker(ctx), c, graphQLRequest{
		OperationName: operationName,
		Query:         query,
		Variables:     variables,
	}, c.authHeaders())
	if err != nil {
		return nil, err
	}
	return data, nil
}

// IntrospectInputType returns the field set of a named GraphQL
// input object via the gateway's __type introspection. Used to
// reverse-engineer the exact shape of inputs like CoordinatesInput
// that the iOS-app reverse-engineered docs got wrong (they conflated
// input/response shapes). Admin-only.
func (c *LiveClient) IntrospectInputType(ctx context.Context, typeName string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	// Admin debug — bypass breaker (see RawGraphQL doc).
	ctx = withBypassBreaker(ctx)
	const q = `query IntrospectInput($name: String!) {
  __type(name: $name) {
    name
    kind
    inputFields {
      name
      description
      type {
        name
        kind
        ofType {
          name
          kind
          ofType {
            name
            kind
          }
        }
      }
      defaultValue
    }
  }
}`
	data, err := doGraphQL[map[string]any](ctx, c, graphQLRequest{
		OperationName: "IntrospectInput",
		Query:         q,
		Variables:     map[string]any{"name": typeName},
	}, c.authHeaders())
	if err != nil {
		return nil, err
	}
	return data, nil
}

// PlanTripRaw posts the planTripWithMultiStop operation with a
// caller-supplied variables payload and returns the gateway's
// response data + errors verbatim. Bypasses the typed PlanTrip path
// so we can reverse-engineer schema/value mismatches without round-
// tripping through chart bumps. Admin-only (the wrapping HTTP
// handler enforces this) — gives the operator full control over
// every field in the request, including ones the typed path
// defaults.
//
// Returns the parsed JSON `data` field on success. On a GraphQL
// `errors` envelope, returns the structured error from
// doGraphQLAt's classification (which preserves extension codes /
// messages) so callers can read the failure reason directly.
func (c *LiveClient) PlanTripRaw(ctx context.Context, variables map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, ErrNotAuthenticated
	}
	// Admin debug — bypass breaker (see RawGraphQL doc).
	ctx = withBypassBreaker(ctx)
	// Marshal/unmarshal so caller-supplied data doesn't accidentally
	// wire any non-JSON-serializable values into the request.
	blob, err := json.Marshal(variables)
	if err != nil {
		return nil, fmt.Errorf("encode variables: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil {
		return nil, fmt.Errorf("decode variables: %w", err)
	}
	data, err := doGraphQL[map[string]any](ctx, c, graphQLRequest{
		OperationName: "planTripWithMultiStop",
		Query:         qPlanTripWithMultiStop,
		Variables:     raw,
	}, c.authHeaders())
	if err != nil {
		return nil, err
	}
	return data, nil
}

// qPlanTripWithMultiStop selects the field set used by the Rivian
// Owner App. Pulled from rivian-api.kaedenb.org's reverse-engineered
// schema; the gateway returns 200 with `errors` when the selection
// includes a removed field, so additions need a probe via
// ChargingFieldProbe / a feature-detect path before going live.
// qPlanTripWithMultiStop targets the v2 planner — `planTrip2` (with
// the "2" suffix) under operation name `planTripWithMultiStopV2`.
// The v1 sibling `planTrip` (and the never-existing
// `planTripMultiStop`) both return INTERNAL_SERVER_ERROR for any
// input today; v2 is what the iOS app actually uses.
//
// Schema source: the actual implementation in
// jrgutier/rivian-python-client/src/rivian/rivian.py at
// plan_trip_with_multi_stop (line 2441+). The gateway.graphql file
// in the same repo was an older snapshot and described the v1
// shape; trust the live client code instead.
//
// Field names that bit us before: `vehicle` (not `vehicleId`!),
// `arrivalSOCPercent` (not `arrivalSOC`), `plans` (not `routes`),
// `summary` block. Drive mode values are CONSERVE / SPORT /
// ALL_PURPOSE (not EVERYDAY).
const qPlanTripWithMultiStop = `query planTripWithMultiStopV2(
  $waypoints: [TripWaypointInput!]!,
  $vehicle: String!,
  $startingSoc: Float,
  $startingRangeMeters: Float,
  $targetArrivalSocPercent: Float,
  $driveMode: String,
  $networkPreferences: [NetworkPreferenceInput!],
  $trailerProfile: String,
  $hasAdapter: Boolean
) {
  planTrip2(
    waypoints: $waypoints,
    vehicle: $vehicle,
    startingSoc: $startingSoc,
    startingRangeMeters: $startingRangeMeters,
    targetArrivalSocPercent: $targetArrivalSocPercent,
    driveMode: $driveMode,
    networkPreferences: $networkPreferences,
    trailerProfile: $trailerProfile,
    hasAdapter: $hasAdapter
  ) {
    status
    plans {
      summary {
        destinationReachable
        socBelowLimitAtDestination
        totalChargeDurationSeconds
        totalDriveDurationSeconds
        totalDriveDistanceMeters
        totalTripDurationSeconds
        arrivalSOCPercent
        arrivalRangeMeters
        arrivalEnergyKwh
      }
      waypoints {
        waypointType
        latitude
        longitude
        arrivalSOCPercent
        departureSOCPercent
        arrivalRangeMeters
        departureRangeMeters
      }
    }
  }
}`
