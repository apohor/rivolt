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
	// startingRangeMeters is non-null on the schema. If the caller
	// didn't pass one, fall back to a SoC-derived estimate using a
	// conservative R1S range table (so the request validates even
	// without an explicit value). Caller can override.
	rangeMeters := in.StartingRangeMeters
	if rangeMeters <= 0 {
		// 5 m/% per kWh @ ~3 mi/kWh -> ~5000m per percent SoC for an
		// R1S Adventure. Coarse but well within the planner's
		// tolerance; refines automatically once the SPA can read the
		// vehicle's reported range.
		rangeMeters = in.StartingSoC * 5000
	}

	vars := planTripVars{
		Origin:              planTripWaypointVar{Latitude: in.Waypoints[0].Latitude, Longitude: in.Waypoints[0].Longitude},
		Destination:         planTripWaypointVar{Latitude: in.Waypoints[len(in.Waypoints)-1].Latitude, Longitude: in.Waypoints[len(in.Waypoints)-1].Longitude},
		Bearing:             in.OriginBearing,
		VehicleID:           in.VehicleID,
		StartingSoc:         in.StartingSoC,
		StartingRangeMeters: rangeMeters,
	}
	if in.TargetArrivalSocPercent != nil {
		v := *in.TargetArrivalSocPercent
		vars.TargetArrivalSocPercent = &v
	}
	for _, np := range in.NetworkPreferences {
		vars.NetworkPreferences = append(vars.NetworkPreferences, planTripNetworkPrefVar{
			NetworkID:  np.NetworkID,
			Preference: np.Preference,
		})
	}

	data, err := doGraphQL[planTripData](ctx, c, graphQLRequest{
		OperationName: "planTrip",
		Query:         qPlanTripWithMultiStop,
		Variables:     vars,
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("planTrip: %w", err)
	}

	out := &TripPlan{
		Status:                  data.PlanTrip.TripPlanStatus,
		ChargeStationsAvailable: data.PlanTrip.ChargeStationsAvailable,
		SoCBelowLimit:           data.PlanTrip.SocBelowLimit,
		Routes:                  make([]TripRoute, 0, len(data.PlanTrip.Routes)),
	}
	for _, r := range data.PlanTrip.Routes {
		route := TripRoute{
			DestinationReached:       r.DestinationReached,
			TotalChargingDurationSec: r.TotalChargingDuration,
			ArrivalSoC:               r.ArrivalSOC,
			ArrivalReachableMeters:   r.ArrivalReachableDistance,
			EnergyConsumptionKWh:     r.EnergyConsumptionOnLeg,
			BatteryEmptyToDestMeters: r.BatteryEmptyToDestinationDistance,
			BatteryEmptyLat:          r.BatteryEmptyLocationLatitude,
			BatteryEmptyLon:          r.BatteryEmptyLocationLongitude,
			RouteResponseRaw:         r.RouteResponse,
			Waypoints:                make([]PlannedWaypoint, 0, len(r.Waypoints)),
		}
		for _, w := range r.Waypoints {
			route.Waypoints = append(route.Waypoints, PlannedWaypoint{
				WaypointType:             w.WaypointType,
				EntityID:                 w.EntityID,
				Name:                     w.Name,
				Latitude:                 w.Latitude,
				Longitude:                w.Longitude,
				MaxPowerKW:               w.MaxPower,
				ChargeDurationSec:        w.ChargeDuration,
				ArrivalSoC:               w.ArrivalSOC,
				DepartureSoC:             w.DepartureSOC,
				ArrivalReachableMeters:   w.ArrivalReachableDistance,
				DepartureReachableMeters: w.DepartureReachableDistance,
			})
		}
		out.Routes = append(out.Routes, route)
	}
	return out, nil
}

// --- wire types ----------------------------------------------------

type planTripVars struct {
	Origin                  planTripWaypointVar      `json:"origin"`
	Destination             planTripWaypointVar      `json:"destination"`
	Bearing                 float64                  `json:"bearing"`
	VehicleID               string                   `json:"vehicleId"`
	StartingSoc             float64                  `json:"startingSoc"`
	StartingRangeMeters     float64                  `json:"startingRangeMeters"`
	TargetArrivalSocPercent *float64                 `json:"targetArrivalSocPercent,omitempty"`
	NetworkPreferences      []planTripNetworkPrefVar `json:"networkPreferences,omitempty"`
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
	PlanTrip struct {
		TripPlanStatus          string             `json:"tripPlanStatus"`
		ChargeStationsAvailable bool               `json:"chargeStationsAvailable"`
		SocBelowLimit           bool               `json:"socBelowLimit"`
		Routes                  []planTripRouteRow `json:"routes"`
	} `json:"planTrip"`
}

type planTripRouteRow struct {
	RouteResponse                     any                   `json:"routeResponse"`
	DestinationReached                bool                  `json:"destinationReached"`
	TotalChargingDuration             int                   `json:"totalChargingDuration"`
	ArrivalSOC                        float64               `json:"arrivalSOC"`
	ArrivalReachableDistance          float64               `json:"arrivalReachableDistance"`
	EnergyConsumptionOnLeg            float64               `json:"energyConsumptionOnLeg"`
	BatteryEmptyToDestinationDistance float64               `json:"batteryEmptyToDestinationDistance"`
	BatteryEmptyLocationLatitude      float64               `json:"batteryEmptyLocationLatitude"`
	BatteryEmptyLocationLongitude     float64               `json:"batteryEmptyLocationLongitude"`
	Waypoints                         []planTripWaypointRow `json:"waypoints"`
}

type planTripWaypointRow struct {
	WaypointType               string  `json:"waypointType"`
	EntityID                   string  `json:"entityId"`
	Name                       string  `json:"name"`
	Latitude                   float64 `json:"latitude"`
	Longitude                  float64 `json:"longitude"`
	MaxPower                   float64 `json:"maxPower"`
	ChargeDuration             int     `json:"chargeDuration"`
	ArrivalSOC                 float64 `json:"arrivalSOC"`
	DepartureSOC               float64 `json:"departureSOC"`
	ArrivalReachableDistance   float64 `json:"arrivalReachableDistance"`
	DepartureReachableDistance float64 `json:"departureReachableDistance"`
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
// qPlanTrip uses the operation + arg shape from the authoritative
// gateway.graphql in jrgutier/rivian-python-client (the iOS app's
// reverse-engineered schema). The earlier guesses based on
// rivian-api.kaedenb.org's docs were materially wrong: the docs page
// described a `planTripMultiStop` operation that the gateway either
// doesn't have or has broken — every variant we sent there returned
// INTERNAL_SERVER_ERROR. The real operation is `planTrip` with
// scalar origin+destination args (not a waypoints array), `bearing`
// (not `originBearing`), and required `startingRangeMeters`. Drive
// mode / trailer profile / connector types / waypointType / adapterRequired
// don't exist on this schema at all.
const qPlanTripWithMultiStop = `query planTrip(
  $origin: CoordinatesInput!,
  $destination: CoordinatesInput!,
  $bearing: Float!,
  $vehicleId: String!,
  $startingSoc: Float!,
  $startingRangeMeters: Float!,
  $targetArrivalSocPercent: Float,
  $networkPreferences: [NetworkPreference!]
) {
  planTrip(
    origin: $origin,
    destination: $destination,
    bearing: $bearing,
    vehicleId: $vehicleId,
    startingSoc: $startingSoc,
    startingRangeMeters: $startingRangeMeters,
    targetArrivalSocPercent: $targetArrivalSocPercent,
    networkPreferences: $networkPreferences
  ) {
    tripPlanStatus
    chargeStationsAvailable
    socBelowLimit
    routes {
      routeResponse
      destinationReached
      totalChargingDuration
      arrivalSOC
      arrivalReachableDistance
      energyConsumptionOnLeg
      batteryEmptyToDestinationDistance
      batteryEmptyLocationLatitude
      batteryEmptyLocationLongitude
      waypoints {
        waypointType
        entityId
        name
        latitude
        longitude
        maxPower
        chargeDuration
        arrivalSOC
        departureSOC
        arrivalReachableDistance
        departureReachableDistance
      }
    }
  }
}`
