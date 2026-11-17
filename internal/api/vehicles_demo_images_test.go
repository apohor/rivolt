package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/rivian"
)

// picturedClient lists one vehicle that carries Rivian-hosted pictures, as a feed, a stored row or the monitor's cache can hand one over.
type picturedClient struct{ rivian.Client }

func (picturedClient) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	return []rivian.Vehicle{{
		ID: "veh-1", Name: "Demo", Model: "R1T",
		ImageURL: "https://media.rivian.com/hero.png",
		Images:   []rivian.VehicleImage{{URL: "https://media.rivian.com/side.png"}},
	}}, nil
}

// The public demo account's vehicle list names no Rivian host, in the hero or the gallery, whichever path built it; another account keeps its pictures.
func TestDemoVehiclesCarryNoRemotePictures(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	list := func(uid uuid.UUID) string {
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil)
		req = req.WithContext(auth.WithUser(req.Context(), uid))
		rec := httptest.NewRecorder()
		handleVehicles(picturedClient{}, nil, nil, nil, nil, nil, nil)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := list(demo.UserID()); strings.Contains(body, "rivian.com") || strings.Contains(body, "image") || !strings.Contains(body, "veh-1") {
		t.Errorf("demo vehicles: %s", body)
	}
	if body := list(uuid.New()); !strings.Contains(body, "https://media.rivian.com/hero.png") || !strings.Contains(body, "side.png") {
		t.Errorf("another account lost its pictures: %s", body)
	}
}
