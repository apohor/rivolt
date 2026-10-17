package rivian

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// The next Parallax read topics (scratchpad parallax-next.md, tiers 1 and
// 2): subscribed on the existing drive-dynamics subscription for a
// time-boxed capture and decode-logged only, so backend and ux can promote
// them one at a time from real frames. Nothing is applied to State.
//
// Each frame is logged as its protobuf field tree (pbTree), with the field
// names pinned from the APK 3.17 decompile where the app's decoder for the
// topic is known. The raw payload is never logged, and a topic can drop
// strings altogether (vehicle.network.state carries the Wi-Fi SSID).

// pxNextCaptureUntil ends the capture: the topics leave the subscription
// at the first reconnect after it, and their frames stop being logged.
var pxNextCaptureUntil = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

func pxNextCapturing(now time.Time) bool { return now.Before(pxNextCaptureUntil) }

// pxNextSpec is how one topic is logged.
type pxNextSpec struct {
	// log is the short topic name on the log line.
	log string
	// names are the top-level field names from the decompile; nil when the
	// app's decoder for the topic isn't pinned (numbers only).
	names map[int]string
	// strings lets the topic's strings into the log. Off for every topic:
	// strings are dropped at any depth until a decode proves the topic
	// holds no free text (DA; network carries the SSID, others could carry
	// names, VINs or destinations).
	strings bool
	// coarse rounds every float to 2 decimals (~1 km for a coordinate) and
	// replaces every varint of a million or more with "big" (E7 integers),
	// for the topics that may hold a position: location, smart charging.
	coarse bool
}

// pxNextTopics are the tier 1 and tier 2 topics. Proto classes are the
// decompile's (R8 names) for whoever promotes a topic.
var pxNextTopics = map[string]pxNextSpec{
	// Tier 1: user value.
	"energy_edge_compute.graphs.parked_energy_distributions": {log: "parked_energy"},
	"charging.session.time_estimation": {log: "charge_time_estimation", // bbl
		names: map[int]string{1: "validity_flag", 2: "remaining_minutes"}},
	"charging.smart_charging.settings":                   {log: "smart_charging_settings", coarse: true},
	"charging.smart_charging.smart_charging_info":        {log: "smart_charging_info", coarse: true},
	"charging.smart_charging.weighted_charging_forecast": {log: "smart_charging_forecast", coarse: true},
	"comfort.user_modes.state":                           {log: "user_modes"},
	// The Wi-Fi block carries the SSID: never opt this topic into strings.
	"vehicle.network.state":                       {log: "network"},
	"energy_edge_compute.graphs.cold_weather_soc": {log: "cold_weather_soc"},
	"vehicle.wheels.vehicle_wheels":               {log: "wheels"},
	"dynamics.vehicle.location":                   {log: "known_location", names: map[int]string{1: "known_location"}, coarse: true}, // brc
	// Tier 2: Phase 5 completeness.
	"charging.session.soc_slider":  {log: "soc_slider", names: map[int]string{1: "user_soc_limit"}},                        // zbi
	"charging.session.trip_target": {log: "trip_target", names: map[int]string{1: "soc", 2: "time_estimate", 3: "status"}}, // c8m
	"charging.session.notification": {log: "charge_notification", // dle
		names: map[int]string{1: "unexpected_stop_reason", 2: "derate_status", 3: "fault_chime"}},
	"comfort.cabin.hvac_settings_status":     {log: "hvac_settings"},
	"comfort.cabin.defrost_defog_status":     {log: "defrost_defog"},
	"comfort.cabin.seat_conditioning_status": {log: "seat_conditioning", names: map[int]string{1: "levels"}},                                  // lyi
	"security.access.vas_fault":              {log: "vas_fault", names: map[int]string{1: "secure_element_faulted", 2: "access_can_faulted"}}, // ozm
	"security.access.btm": {log: "btm", names: map[int]string{ // vr1
		1: "ff_hardware_failure_status", 2: "ic_hardware_failure_status", 3: "lfd_hardware_failure_status",
		4: "rf_hardware_failure_status", 5: "rfd_hardware_failure_status", 6: "oc_hardware_failure_error",
		7: "ffr_hardware_failure_error", 8: "rfr_hardware_failure_error", 9: "ffl_hardware_failure_error",
		10: "rfl_hardware_failure_error"}},
	// Armed state only; no video, no streaming topics.
	"security.video_monitoring.state": {log: "video_monitoring", names: map[int]string{1: "video_monitoring_status", 2: "video_mode", 3: "tos_acceptance"}}, // eko
}

// pxNextRVMs lists pxNextTopics for the subscription, in a fixed order.
var pxNextRVMs = []string{
	"energy_edge_compute.graphs.parked_energy_distributions",
	"charging.session.time_estimation",
	"charging.smart_charging.settings",
	"charging.smart_charging.smart_charging_info",
	"charging.smart_charging.weighted_charging_forecast",
	"comfort.user_modes.state",
	"vehicle.network.state",
	"energy_edge_compute.graphs.cold_weather_soc",
	"vehicle.wheels.vehicle_wheels",
	"dynamics.vehicle.location",
	"charging.session.soc_slider",
	"charging.session.trip_target",
	"charging.session.notification",
	"comfort.cabin.hvac_settings_status",
	"comfort.cabin.defrost_defog_status",
	"comfort.cabin.seat_conditioning_status",
	"security.access.vas_fault",
	"security.access.btm",
	"security.video_monitoring.state",
}

// pbTree limits: deep or long trees (the energy graphs) are cut, not
// dropped.
const (
	pbTreeMaxDepth   = 4
	pbTreeMaxRepeats = 24
)

// pbTree renders a protobuf payload as a field tree for the log: keys are
// "N" or "N_name"; varints as numbers, 32/64-bit as floats, printable
// UTF-8 as strings, other length-delimited values as nested trees (or a
// byte count). A repeated field becomes a list. ok=false: not a message.
func pbTree(raw []byte, names map[int]string, spec pxNextSpec, depth int) (map[string]any, bool) {
	out := map[string]any{}
	err := pbScan(raw, func(f, w int, val []byte) {
		key := strconv.Itoa(f)
		if n, ok := names[f]; ok {
			key += "_" + n
		}
		var v any
		switch w {
		case 0:
			n, _ := binary.Uvarint(val)
			v = n
			// A coarse topic may carry a position as E7 integers
			// (lat×1e7; a negative one is a 10-byte uvarint): any varint
			// of a million or more is logged as "big", never its value.
			if spec.coarse && n >= 1_000_000 {
				v = "big"
			}
		case 1, 5:
			var x float64
			if w == 1 {
				x = math.Float64frombits(binary.LittleEndian.Uint64(val))
			} else {
				x = float64(math.Float32frombits(binary.LittleEndian.Uint32(val)))
			}
			if spec.coarse {
				x = math.Round(x*100) / 100
			}
			v = x
		case 2:
			if len(val) > 0 && utf8.Valid(val) && printable(val) {
				if !spec.strings {
					return
				}
				v = string(val)
			} else if depth < pbTreeMaxDepth {
				if sub, ok := pbTree(val, nil, spec, depth+1); ok {
					v = sub
				} else {
					v = map[string]any{"bytes": len(val)}
				}
			} else {
				v = map[string]any{"bytes": len(val)}
			}
		}
		switch prev := out[key].(type) {
		case nil:
			out[key] = v
		case []any:
			if len(prev) < pbTreeMaxRepeats {
				out[key] = append(prev, v)
			} else if len(prev) == pbTreeMaxRepeats {
				out[key] = append(prev, "…")
			}
		default:
			out[key] = []any{prev, v}
		}
	})
	return out, err == nil
}

func printable(b []byte) bool {
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return false
		}
	}
	return true
}

// logPxNext decode-logs one frame of a pxNextTopics topic.
func (m *StateMonitor) logPxNext(vehicleID string, spec pxNextSpec, f DriveDynamicsFrame) {
	raw, err := base64.StdEncoding.DecodeString(f.Payload)
	tree, ok := map[string]any(nil), false
	if err == nil {
		tree, ok = pbTree(raw, spec.names, spec, 0)
	}
	m.logger.Info("parallax drive-dynamics shadow",
		"vehicle", vehicleID, "topic", spec.log, "rvm", f.RVM,
		"px_ok", ok, "px_bytes", len(raw), "px_fields", tree, "ts_ms", f.TimestampMs)
}
