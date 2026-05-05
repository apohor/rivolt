package elevation

import (
	"bytes"
	"image"
	"image/png"
	"math"
)

// decodePNGBytes is a small convenience wrapper that decodes a PNG
// from a byte slice and runs decodeTerrarium on the result. Returns
// (nil, nil) for an unexpected tile size (1x1 ocean placeholder etc.)
// so the caller can distinguish "decode failed" from "decoded but no
// useful elevation data".
func decodePNGBytes(data []byte) (*tile, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return decodeTerrarium(img), nil
}

// validLatLon rejects NaN / out-of-range coordinates and the (0, 0)
// "no GPS fix" sentinel the recorder writes when the gateway hasn't
// reported a position yet.
func validLatLon(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return false
	}
	if lat == 0 && lon == 0 {
		return false
	}
	if lat < -85.05112878 || lat > 85.05112878 {
		return false
	}
	if lon < -180 || lon > 180 {
		return false
	}
	return true
}

// tileCoords converts a lat/lon to the (xtile, ytile) of the
// containing slippy-map tile at zoom z, plus the fractional pixel
// offset (fx, fy) within that tile in [0, 256). Standard Web Mercator
// math: see https://wiki.openstreetmap.org/wiki/Slippy_map_tilenames.
func tileCoords(lat, lon float64, z int) (xtile, ytile int, fx, fy float64) {
	n := math.Exp2(float64(z))
	xf := (lon + 180.0) / 360.0 * n
	latRad := lat * math.Pi / 180.0
	yf := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n
	xtile = int(math.Floor(xf))
	ytile = int(math.Floor(yf))
	fx = (xf - float64(xtile)) * 256.0
	fy = (yf - float64(ytile)) * 256.0
	return
}

// decodeTerrarium turns a 256x256 PNG into a flat int16 elevation
// grid (in meters). Returns nil if the image isn't the expected
// size -- AWS occasionally serves a 1x1 placeholder over open ocean
// where there's no DEM coverage.
func decodeTerrarium(img image.Image) *tile {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w != 256 || h != 256 {
		return nil
	}
	data := make([]int16, w*h)
	// At() is slow per-pixel; specialize on the common concrete
	// types image/png produces (NRGBA / RGBA).
	switch src := img.(type) {
	case *image.NRGBA:
		for y := 0; y < h; y++ {
			row := y * src.Stride
			for x := 0; x < w; x++ {
				p := row + x*4
				r := int(src.Pix[p])
				g := int(src.Pix[p+1])
				bb := int(src.Pix[p+2])
				h := r*256 + g + bb/256 - 32768
				data[y*w+x] = int16(h)
			}
		}
	case *image.RGBA:
		for y := 0; y < h; y++ {
			row := y * src.Stride
			for x := 0; x < w; x++ {
				p := row + x*4
				r := int(src.Pix[p])
				g := int(src.Pix[p+1])
				bb := int(src.Pix[p+2])
				h := r*256 + g + bb/256 - 32768
				data[y*w+x] = int16(h)
			}
		}
	default:
		// Generic fallback. Slower (At returns color.Color
		// interface which boxes the values) but always correct.
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				rr, gg, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				// RGBA() returns 0..0xffff per channel; collapse
				// to 0..0xff first.
				r := int(rr >> 8)
				g := int(gg >> 8)
				b := int(bb >> 8)
				h := r*256 + g + b/256 - 32768
				data[y*w+x] = int16(h)
			}
		}
	}
	return &tile{w: w, h: h, data: data}
}

// sample returns the bilinearly-interpolated elevation at fractional
// pixel coordinates (fx, fy) in [0, 256). Edge pixels clamp instead
// of wrapping to keep behavior sane at the tile boundary -- the
// caller (Lookup) only ever reads from one tile at a time, so we
// don't try to stitch across tile edges.
func (t *tile) sample(fx, fy float64) float64 {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := x0 + 1
	y1 := y0 + 1
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= t.w {
		x1 = t.w - 1
	}
	if y1 >= t.h {
		y1 = t.h - 1
	}
	dx := fx - float64(x0)
	dy := fy - float64(y0)
	v00 := float64(t.data[y0*t.w+x0])
	v10 := float64(t.data[y0*t.w+x1])
	v01 := float64(t.data[y1*t.w+x0])
	v11 := float64(t.data[y1*t.w+x1])
	a := v00*(1-dx) + v10*dx
	b := v01*(1-dx) + v11*dx
	return a*(1-dy) + b*dy
}
