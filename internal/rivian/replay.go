package rivian

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

// replayFrame is one JSONL line — the lifecycle-relevant columns of a
// vehicle_state row, named to match the DB so a `SELECT row_to_json(...)`
// export drops straight in. Units are the DB's (miles, mph); toState
// converts to State's (km, kph).
type replayFrame struct {
	At            time.Time  `json:"at"`
	ShiftState    string     `json:"shift_state"`
	BatteryPct    float64    `json:"battery_level_pct"`
	OdometerMi    float64    `json:"odometer_mi"`
	RangeMi       float64    `json:"range_mi"`
	SpeedMph      float64    `json:"speed_mph"`
	Lat           float64    `json:"lat"`
	Lon           float64    `json:"lon"`
	ChargingState string     `json:"charging_state"`
	ChargerKW     float64    `json:"charger_power_kw"`
	PowerState    string     `json:"power_state"`
	LocationFixAt *time.Time `json:"location_fix_at"`

	// Display-only columns. Optional in JSONL fixtures; the DB-backed
	// replay corpus fills them so a demo vehicle's Live panel isn't a
	// wall of em-dashes. Nullable columns arrive as JSON null → zero.
	DriveMode       string  `json:"drive_mode"`
	ChargeLimitPct  float64 `json:"charge_limit_pct"`
	InsideTempC     float64 `json:"inside_temp_c"`
	OutsideTempC    float64 `json:"outside_temp_c"`
	TirePressureBar float64 `json:"tire_pressure_min_bar"`
	PackTempAvgC    float64 `json:"pack_temp_avg_c"`
	PackTempMaxC    float64 `json:"pack_temp_max_c"`
	PackTempMinC    float64 `json:"pack_temp_min_c"`

	// SessionPrice is the running price Rivian's live charging session
	// reports for a session it bills (a Rivian Adventure Network stop);
	// 0 when it doesn't. Only the sample corpus sets it.
	SessionPrice    float64 `json:"session_price,omitempty"`
	SessionCurrency string  `json:"session_currency,omitempty"`
	// SessionNetwork names the network billing that session.
	SessionNetwork string `json:"session_network,omitempty"`
}

func (f replayFrame) toState() State {
	s := State{
		At:              f.At,
		Gear:            f.ShiftState,
		BatteryLevelPct: f.BatteryPct,
		OdometerKm:      f.OdometerMi / kmToMi,
		DistanceToEmpty: f.RangeMi / kmToMi,
		SpeedKph:        f.SpeedMph / kphToMi,
		Latitude:        f.Lat,
		Longitude:       f.Lon,
		ChargerState:    f.ChargingState,
		ChargerPowerKW:  f.ChargerKW,
		PowerState:      f.PowerState,
		DriveMode:       f.DriveMode,
		ChargeTargetPct: f.ChargeLimitPct,
		CabinTempC:      f.InsideTempC,
		OutsideTempC:    f.OutsideTempC,
		PackTempAvgC:    f.PackTempAvgC,
		PackTempMaxC:    f.PackTempMaxC,
		PackTempMinC:    f.PackTempMinC,
		// vehicle_state keeps only the worst corner; spread it to all
		// four so the recorder's min-of-4 round-trips it unchanged.
		TirePressureFLBar: f.TirePressureBar,
		TirePressureFRBar: f.TirePressureBar,
		TirePressureRLBar: f.TirePressureBar,
		TirePressureRRBar: f.TirePressureBar,
	}
	// chargerStatus (cable connected?) isn't a vehicle_state column, but
	// the charge lifecycle requires it to open a session, so infer it.
	// Only from states that mean a live charger: Rivian also reports
	// charging_ready while unplugged and even while driving, and
	// recorded charger power can outlive the session, so neither is
	// evidence on its own.
	if f.ChargingState != "" {
		s.ChargerStatus = "chrgr_sts_not_connected"
		switch strings.ToLower(f.ChargingState) {
		case "charging_active", "charging_connecting", "waiting_on_charger",
			"charging_station_err", "charging_station_error",
			"charging_user_stopped", "charging_complete":
			if !isDrivingGear(f.ShiftState) {
				s.ChargerStatus = "chrgr_sts_connected_charging"
			}
		}
	}
	if f.LocationFixAt != nil {
		s.LocationFixAt = *f.LocationFixAt
	}
	// Rows recorded before the stale-charger-power fix carry the last
	// session's kW through idle states; the live path clears it in
	// mergeState, which replay bypasses, so clear it here too.
	dropIdleChargerPower(&s)
	if f.SessionPrice > 0 {
		s.ReplaySession = &LiveSession{Active: true, IsRivianCharger: true,
			CurrentPrice: strconv.FormatFloat(f.SessionPrice, 'f', 2, 64), CurrentCurrency: f.SessionCurrency,
			Network: f.SessionNetwork}
	}
	return s
}

// FramesFromJSONL parses a newline-delimited stream of replayFrame objects
// (a DB export or captured stream) into State frames, in file order. Blank
// lines are skipped.
func FramesFromJSONL(r io.Reader) ([]State, error) {
	var out []State
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var f replayFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, err
		}
		out = append(out, f.toState())
	}
	return out, sc.Err()
}

// Replayer feeds a captured sequence of State frames through the real
// recorder lifecycle with persistence disabled, collecting the drives it
// would have produced. It lets recorder/lifecycle changes be regression-
// tested against real (DB-exported) or hand-authored frame streams offline
// — no database and no live Rivian feed (which preview can no longer
// provide; see docs/REPLAY_HARNESS.md and the preview-rivian-disconnected
// note).
//
// Frames are full State snapshots (the shape a vehicle_state row exports
// to), fed in arrival order — NOT deltas — so each frame is applied as-is,
// exactly as the merged cache state the live callback hands the recorder.
type Replayer struct {
	m       *StateMonitor
	vehicle string
	prev    *State

	mu      sync.Mutex
	closed  []drives.Drive
	charged []charges.Charge
}

// NewReplayer builds a persistence-free monitor for one vehicle id. All
// stores are left nil, so the recorder's writes are no-ops (the SetStores
// contract) and only the in-memory lifecycle runs; closed drives are
// captured via the drive-close hook.
func NewReplayer(vehicleID string) *Replayer {
	r := &Replayer{vehicle: vehicleID}
	// The full constructor, so every lifecycle map (charge bonds, live
	// sessions, …) exists once frames exercise the charge path too.
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.parallaxGPS, m.parallaxDriveDynamics, m.parallaxCapture = false, false, false
	m.driveCloseHook = func(_ context.Context, d drives.Drive) {
		r.mu.Lock()
		r.closed = append(r.closed, d)
		r.mu.Unlock()
	}
	m.chargeCloseHook = func(_ context.Context, c charges.Charge) {
		r.mu.Lock()
		r.charged = append(r.charged, c)
		r.mu.Unlock()
	}
	r.m = m
	return r
}

// Feed pushes one frame through the recorder exactly as the live WS
// callback would after merging: it becomes the current cache state and
// drives the lifecycle. VehicleID is stamped automatically.
func (r *Replayer) Feed(f State) {
	cur := f
	cur.VehicleID = r.vehicle
	sess := cur.ReplaySession
	cur.ReplaySession = nil
	r.m.mu.Lock()
	r.m.cache[r.vehicle] = &cur
	r.m.mu.Unlock()
	r.m.record(context.Background(), r.vehicle, r.prev, &cur)
	r.m.applyReplaySession(context.Background(), r.vehicle, &cur, sess)
	r.prev = &cur
}

// FeedAll replays a whole sequence in order.
func (r *Replayer) FeedAll(frames []State) {
	for _, f := range frames {
		r.Feed(f)
	}
}

// Drives returns the drives the replay produced. The recorder fires the
// close hook in a goroutine, so this waits for the collection to go quiet
// (no new drive for a short settle window) before reading it back — safe
// to call once after the last Feed.
func (r *Replayer) Drives() []drives.Drive {
	const settle = 100 * time.Millisecond
	last := -1
	for {
		r.mu.Lock()
		n := len(r.closed)
		r.mu.Unlock()
		if n == last {
			break
		}
		last = n
		time.Sleep(settle)
	}
	r.mu.Lock()
	out := append([]drives.Drive(nil), r.closed...)
	r.mu.Unlock()
	return out
}

// Charges returns the charge sessions the replay closed, after the same
// settle wait as Drives (the close hook also runs in a goroutine).
func (r *Replayer) Charges() []charges.Charge {
	r.Drives() // settle
	const settle = 100 * time.Millisecond
	last := -1
	for {
		r.mu.Lock()
		n := len(r.charged)
		r.mu.Unlock()
		if n == last {
			break
		}
		last = n
		time.Sleep(settle)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]charges.Charge(nil), r.charged...)
}
