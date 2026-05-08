package rivian

import (
	"context"
	"fmt"
)

// PlanTripInput is the request shape for PlanTrip. Mirrors the
// arguments accepted by the Rivian gateway's planTripWithMultiStop
// operation. All required fields are non-pointer; optional fields
// (e.g. TargetArrivalSocPercent, TrailerProfile) are pointers so the
// zero value is distinguishable from "not provided".
//
// Waypoints must include both the origin (waypointType="origin") and
// the destination (waypointType="destination"); intermediate stops
// use waypointType="waypoint". The gateway computes charging stops
// between them — callers don't pre-pick chargers.
type PlanTripInput struct {
	VehicleID               string
	StartingSoC             float64
	StartingRangeMeters     float64 // optional — pass 0 to omit
	OriginBearing           float64 // degrees from true north; pass 0 if unknown
	Waypoints               []PlanTripWaypoint
	TargetArrivalSocPercent *float64
	DriveMode               string // empty = let Rivian pick
	TrailerProfile          string // empty = no trailer
	AvoidAdapterRequired    bool
	SupportedConnectorTypes []string
	NetworkPreferences      []NetworkPreference
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
	AdapterRequired          bool
}

// PlanTrip runs the gateway's planTripWithMultiStop operation and
// returns a flattened, JSON-friendly TripPlan. Per-user — the
// authenticated session must own the vehicleId.
//
// The query selects every field documented at
// https://rivian-api.kaedenb.org/app/trip-planning/plan-trip/ except
// the geometry payload, which is forwarded verbatim via
// RouteResponseRaw so the SPA renderer can decode without a second
// round-trip.
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
		return nil, fmt.Errorf("planTrip: at least origin + destination waypoints required")
	}

	vars := planTripVars{
		VehicleID:               in.VehicleID,
		StartingSoc:             in.StartingSoC,
		OriginBearing:           in.OriginBearing,
		AvoidAdapterRequired:    in.AvoidAdapterRequired,
		SupportedConnectorTypes: in.SupportedConnectorTypes,
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
	for _, wp := range in.Waypoints {
		vars.Waypoints = append(vars.Waypoints, planTripWaypointVar{
			Latitude:  wp.Latitude,
			Longitude: wp.Longitude,
		})
	}
	for _, np := range in.NetworkPreferences {
		vars.NetworkPreferences = append(vars.NetworkPreferences, planTripNetworkPrefVar{
			NetworkID:  np.NetworkID,
			Preference: np.Preference,
		})
	}

	data, err := doGraphQL[planTripData](ctx, c, graphQLRequest{
		OperationName: "planTripWithMultiStop",
		Query:         qPlanTripWithMultiStop,
		Variables:     vars,
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("planTripWithMultiStop: %w", err)
	}

	out := &TripPlan{
		Status:                  data.PlanTripMultiStop.TripPlanStatus,
		ChargeStationsAvailable: data.PlanTripMultiStop.ChargeStationsAvailable,
		SoCBelowLimit:           data.PlanTripMultiStop.SocBelowLimit,
		Routes:                  make([]TripRoute, 0, len(data.PlanTripMultiStop.Routes)),
	}
	for _, r := range data.PlanTripMultiStop.Routes {
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
				AdapterRequired:          w.AdapterRequired,
			})
		}
		out.Routes = append(out.Routes, route)
	}
	return out, nil
}

// --- wire types ----------------------------------------------------

type planTripVars struct {
	VehicleID               string                   `json:"vehicleId"`
	StartingSoc             float64                  `json:"startingSoc"`
	StartingRangeMeters     *float64                 `json:"startingRangeMeters,omitempty"`
	OriginBearing           float64                  `json:"originBearing"`
	Waypoints               []planTripWaypointVar    `json:"waypoints"`
	TargetArrivalSocPercent *float64                 `json:"targetArrivalSocPercent,omitempty"`
	DriveMode               *string                  `json:"driveMode,omitempty"`
	TrailerProfile          *string                  `json:"trailerProfile,omitempty"`
	AvoidAdapterRequired    bool                     `json:"avoidAdapterRequired"`
	SupportedConnectorTypes []string                 `json:"supportedConnectorTypes,omitempty"`
	NetworkPreferences      []planTripNetworkPrefVar `json:"networkPreferences,omitempty"`
}

// planTripWaypointVar is the wire shape of one CoordinatesInput.
// The reverse-engineered docs (rivian-api.kaedenb.org) listed
// waypointType + entityId as input fields, but the gateway returns
// BAD_USER_INPUT when those are present — the docs page conflated
// the *response* waypoint shape (which carries waypointType / name /
// entityId / etc.) with the input. CoordinatesInput is just the
// pair of floats; origin vs destination is derived from array
// position (index 0 is origin, last index is destination,
// intermediate entries are charging stops the planner may use as
// hints).
type planTripWaypointVar struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type planTripNetworkPrefVar struct {
	NetworkID  string `json:"networkId"`
	Preference int    `json:"preference"`
}

type planTripData struct {
	PlanTripMultiStop struct {
		TripPlanStatus          string             `json:"tripPlanStatus"`
		ChargeStationsAvailable bool               `json:"chargeStationsAvailable"`
		SocBelowLimit           bool               `json:"socBelowLimit"`
		Routes                  []planTripRouteRow `json:"routes"`
	} `json:"planTripMultiStop"`
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
	AdapterRequired            bool    `json:"adapterRequired"`
}

// qPlanTripWithMultiStop selects the field set used by the Rivian
// Owner App. Pulled from rivian-api.kaedenb.org's reverse-engineered
// schema; the gateway returns 200 with `errors` when the selection
// includes a removed field, so additions need a probe via
// ChargingFieldProbe / a feature-detect path before going live.
const qPlanTripWithMultiStop = `query planTripWithMultiStop(
  $vehicleId: String!,
  $startingSoc: Float!,
  $startingRangeMeters: Float,
  $originBearing: Float!,
  $waypoints: [CoordinatesInput!]!,
  $targetArrivalSocPercent: Float,
  $driveMode: String,
  $trailerProfile: String,
  $avoidAdapterRequired: Boolean,
  $supportedConnectorTypes: [String!],
  $networkPreferences: [NetworkPreference!]
) {
  planTripMultiStop(
    vehicleId: $vehicleId,
    startingSoc: $startingSoc,
    startingRangeMeters: $startingRangeMeters,
    originBearing: $originBearing,
    waypoints: $waypoints,
    targetArrivalSocPercent: $targetArrivalSocPercent,
    driveMode: $driveMode,
    trailerProfile: $trailerProfile,
    avoidAdapterRequired: $avoidAdapterRequired,
    supportedConnectorTypes: $supportedConnectorTypes,
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
        adapterRequired
      }
    }
  }
}`
