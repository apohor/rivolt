package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// pbVarints encodes {field: varint} pairs (in order) as a base64 proto.
func pbVarints(kv ...uint64) string {
	var b []byte
	for i := 0; i+1 < len(kv); i += 2 {
		b = binary.AppendUvarint(b, kv[i]<<3)
		b = binary.AppendUvarint(b, kv[i+1])
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestDecodeChargingStatus(t *testing.T) {
	cs, ok := decodeChargingStatus(pbVarints(1, 2, 2, 5, 3, 1))
	if !ok || cs != (ChargingStatus{Plug: 2, Display: 5, EVSE: 1}) {
		t.Fatalf("decode = %+v, %v", cs, ok)
	}
	if got := chargerStatusFromParallax(cs.Plug); got != "chrgr_sts_connected_no_chrg" {
		t.Errorf("plug 2 → %q", got)
	}
	if got := chargerStateFromParallax(cs.Display); got != "charging_scheduled" {
		t.Errorf("display 5 → %q", got)
	}
	// An empty message is all defaults: INIT leaves the status alone.
	if cs, ok := decodeChargingStatus(""); !ok || chargerStatusFromParallax(cs.Plug) != "" {
		t.Errorf("empty payload = %+v, %v", cs, ok)
	}
	if _, ok := decodeChargingStatus("not base64!"); ok {
		t.Error("malformed payload decoded")
	}
	for plug, want := range map[uint64]string{0: "", 1: "chrgr_sts_not_connected", 3: "chrgr_sts_fault", 4: "chrgr_sts_not_connected", 9: ""} {
		if got := chargerStatusFromParallax(plug); got != want {
			t.Errorf("plug %d → %q, want %q", plug, got, want)
		}
	}
}

func TestDecodeBatteryCharacteristics(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(pbVarints(1, 3, 2, 4, 3, 5, 4, 6))
	f32 := func(field int, v float32) {
		raw = binary.AppendUvarint(raw, uint64(field<<3|5))
		raw = binary.LittleEndian.AppendUint32(raw, math.Float32bits(v))
	}
	f32(5, 86.5)
	f32(6, 87.9)
	bc, ok := decodeBatteryCharacteristics(base64.StdEncoding.EncodeToString(raw))
	want := BatteryCharacteristics{Chemistry: 3, CellType: 4, ModuleType: 5, PackCapacity: 6, UserTotalKWh: 86.5, UserMaxKWh: 87.9}
	if !ok || bc != want {
		t.Fatalf("decode = %+v, %v; want %+v", bc, ok, want)
	}
	if kwh := packCapacityKWh(bc.PackCapacity); kwh != 88 {
		t.Errorf("pack capacity 6 → %v kWh, want 88", kwh)
	}
}

// An R2's vehicleState has no chargerStatus: Parallax charging.session.status
// supplies it, so plugged-but-idle (a scheduled window) reads as plugged.
func TestR2PlugStateFromParallax(t *testing.T) {
	ctx := context.Background()
	vs := func(r *replayRun, cs, status string, kw float64) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: time.Now(), Gear: "P",
			ChargerState: cs, ChargerStatus: status, ChargerPowerKW: kw, BatteryLevelPct: 60, OdometerKm: 30000})
	}
	px := func(r *replayRun, plug uint64, at time.Time) {
		r.m.onDriveDynamicsFrame(ctx, replayVehicle, DriveDynamicsFrame{RVM: rvmChargingStatus,
			Payload: pbVarints(1, plug, 2, 5), TimestampMs: at.UnixMilli()})
	}
	status := func(r *replayRun) (string, bool) {
		c := r.cached()
		return c.ChargerStatus, c.ChargerStatusPx
	}

	t.Run("plugged, scheduled", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "charging_scheduled", "", 0)
		px(r, 2, time.Now())
		if s, fromPx := status(r); s != "chrgr_sts_connected_no_chrg" || !fromPx {
			t.Fatalf("status %q (px %v), want connected_no_chrg from Parallax", s, fromPx)
		}
		vs(r, "charging_scheduled", "", 0) // vehicleState without the field keeps it
		if s, _ := status(r); s != "chrgr_sts_connected_no_chrg" {
			t.Errorf("a vehicleState push blanked the status: %q", s)
		}
		r.m.sessMu.Lock()
		plugged := r.m.sessions[replayVehicle].plugged(r.cached())
		r.m.sessMu.Unlock()
		if !plugged {
			t.Error("plugged() = false for a Parallax connected status")
		}
	})

	t.Run("replay after a restart applies; an older frame doesn't", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "charging_complete", "", 0)
		at := time.Now().Add(-9 * time.Hour) // plugged in last night
		px(r, 2, at)
		if s, _ := status(r); s != "chrgr_sts_connected_no_chrg" {
			t.Fatalf("replayed latest frame: status %q, want connected_no_chrg", s)
		}
		px(r, 1, at.Add(-time.Hour)) // out of order
		if s, _ := status(r); s != "chrgr_sts_connected_no_chrg" {
			t.Errorf("an older frame changed the status to %q", s)
		}
		px(r, 1, time.Now()) // unplugged
		if s, _ := status(r); s != "chrgr_sts_not_connected" {
			t.Errorf("after an unplug frame: status %q", s)
		}
	})

	t.Run("a Parallax unplugged doesn't stop a charge that draws power", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "charging_ready", "", 0)
		px(r, 1, time.Now())
		vs(r, "charging_active", "", 7.4)
		if _, c := r.session(); c == nil {
			t.Error("no charge opened: a Parallax not_connected overruled 7.4 kW")
		}
	})

	t.Run("an R1's vehicleState status wins", func(t *testing.T) {
		r := newReplayRun("R2")
		r.m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: "R1S"}
		vs(r, "charging_ready", "chrgr_sts_not_connected", 0)
		px(r, 2, time.Now())
		if s, fromPx := status(r); s != "chrgr_sts_not_connected" || fromPx {
			t.Errorf("status %q (px %v), want vehicleState's not_connected", s, fromPx)
		}
	})

	t.Run("a vehicleState status clears the Parallax mark", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "charging_ready", "", 0)
		px(r, 2, time.Now())
		vs(r, "charging_ready", "chrgr_sts_connected_charging", 0)
		if s, fromPx := status(r); s != "chrgr_sts_connected_charging" || fromPx {
			t.Errorf("status %q (px %v), want vehicleState's, unmarked", s, fromPx)
		}
	})
}
