package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"
)

// Real ota.deployment.state payloads from prod (2026-10-16): an R2 on
// 2026.36.41 and an R1T on 2026.36.0, neither with an update pending.
// getOTAUpdateDetails reported 2026.36.0 for that R1T too.
const (
	otaPayloadR2 = "CjcIARIdCgoyMDI2LjM2LjQxGOoPICQoKTIINGFiZTA1ZTQiFCoSCAEQARoCEAAiAhAAKgAweEAC"
	otaPayloadR1 = "CjYIARIaCgkyMDI2LjM2LjAY6g8gJDIIZDJmNzM0NzQiFioUCAEQARoCEAAiAhAAKgAweDgBQAI="
)

// otaPayload builds a payload: firmware on cur, plus an HD-maps entry, and
// (when avail != "") an active available OTA on the firmware entry.
func otaPayload(cur, avail string, active bool) string {
	msg := func(f int, b []byte) []byte {
		out := binary.AppendUvarint(nil, uint64(f<<3|2))
		out = binary.AppendUvarint(out, uint64(len(b)))
		return append(out, b...)
	}
	vi := func(f int, v uint64) []byte {
		return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(f<<3)), v)
	}
	version := func(v string) []byte { return msg(1, []byte(v)) }
	fw := append(vi(1, 1), msg(2, version(cur))...)
	if avail != "" {
		a := msg(2, version(avail))
		if active {
			a = append(a, vi(11, 1)...)
		}
		fw = append(fw, msg(4, a)...)
	}
	maps := append(vi(1, 2), msg(2, version("maps-2026.10"))...)
	return base64.StdEncoding.EncodeToString(append(msg(1, maps), msg(1, fw)...))
}

func TestDecodeOTADeployment(t *testing.T) {
	for _, c := range []struct {
		name, b64, cur, avail string
		ok                    bool
	}{
		{"prod R2", otaPayloadR2, "2026.36.41", "", true},
		{"prod R1T", otaPayloadR1, "2026.36.0", "", true},
		{"pending, active", otaPayload("2026.36.0", "2026.40.2", true), "2026.36.0", "2026.40.2", true},
		{"offered, not active", otaPayload("2026.36.0", "2026.40.2", false), "2026.36.0", "", true},
		{"no firmware entry", base64.StdEncoding.EncodeToString([]byte{0x0a, 0x02, 0x08, 0x02}), "", "", false},
		{"malformed", "!!", "", "", false},
	} {
		cur, avail, ok := decodeOTADeployment(c.b64)
		if cur != c.cur || avail != c.avail || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, cur, avail, ok, c.cur, c.avail, c.ok)
		}
	}
}

func TestParallaxOTAFillsVersionsWithoutVehicleState(t *testing.T) {
	ctx := context.Background()
	px := func(r *replayRun, b64 string, at time.Time) {
		r.m.onDriveDynamicsFrame(ctx, replayVehicle, DriveDynamicsFrame{RVM: rvmOtaDeploy, Payload: b64, TimestampMs: at.UnixMilli()})
	}
	vs := func(r *replayRun, cur string) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: time.Now(), OtaCurrentVersion: cur, BatteryLevelPct: 60, OdometerKm: 30000})
	}
	versions := func(r *replayRun) (string, string) {
		c := r.cached()
		return c.OtaCurrentVersion, c.OtaAvailableVersion
	}

	t.Run("R2: Parallax supplies the versions; a replay applies, an older frame doesn't", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "")
		at := time.Now().Add(-5 * 24 * time.Hour) // the last change, days ago
		px(r, otaPayload("2026.36.41", "2026.40.2", true), at)
		if c, a := versions(r); c != "2026.36.41" || a != "2026.40.2" {
			t.Fatalf("versions (%q, %q)", c, a)
		}
		px(r, otaPayload("2026.31.0", "", false), at.Add(-time.Hour))
		if c, _ := versions(r); c != "2026.36.41" {
			t.Errorf("an older frame set %q", c)
		}
		px(r, otaPayload("2026.40.2", "", false), time.Now()) // installed
		if c, a := versions(r); c != "2026.40.2" || a != "" {
			t.Errorf("after the install: (%q, %q)", c, a)
		}
		vs(r, "") // a vehicleState push without versions keeps them
		if c, _ := versions(r); c != "2026.40.2" {
			t.Errorf("a vehicleState push blanked the version: %q", c)
		}
	})

	t.Run("R1: vehicleState's version wins", func(t *testing.T) {
		r := newReplayRun("R2")
		vs(r, "2026.36.0")
		px(r, otaPayload("2026.99.0", "", false), time.Now())
		if c, _ := versions(r); c != "2026.36.0" {
			t.Errorf("version %q, want vehicleState's 2026.36.0", c)
		}
	})
}
