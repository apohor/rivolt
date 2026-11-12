package main

import (
	"encoding/json"
	"log/slog"

	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/radar"
)

// demoRadarBoxes is the longitude/latitude box of each sample trip's route: the only places the signed-out demo's radar covers.
func demoRadarBoxes() []radar.Box {
	var idx struct {
		Trips []struct {
			RoutePolyline string `json:"route_polyline"`
		} `json:"trips"`
	}
	if json.Unmarshal(demo.PresetsIndex().Body, &idx) != nil {
		return nil
	}
	var boxes []radar.Box
	for _, t := range idx.Trips {
		pts := maps.DecodePolyline6(t.RoutePolyline) // (lat, lon)
		if len(pts) == 0 {
			continue
		}
		b := radar.Box{W: pts[0][1], E: pts[0][1], S: pts[0][0], N: pts[0][0]}
		for _, p := range pts {
			b.W, b.E = min(b.W, p[1]), max(b.E, p[1])
			b.S, b.N = min(b.S, p[0]), max(b.N, p[0])
		}
		boxes = append(boxes, b)
	}
	return boxes
}

// newDemoRadar builds the demo's cache-only radar over svc for the sample trips, counting each answer in m; nil when the demo has no trips.
func newDemoRadar(svc *radar.Service, m *metrics.Metrics, log *slog.Logger) *radar.Demo {
	boxes := demoRadarBoxes()
	if len(boxes) == 0 {
		return nil
	}
	for _, r := range []string{"hit", "miss", "limited"} {
		m.DemoRadarTotal.WithLabelValues(r)
	}
	d := radar.NewDemo(svc, boxes, func(result string) { m.DemoRadarTotal.WithLabelValues(result).Inc() }, log)
	log.Info("demo radar enabled", "tiles", d.Len())
	return d
}
