package radar

import (
	"errors"
	"math"
)

// LCC is the Lambert conformal conic projection of the RRFS CONUS grid on a sphere (the parameters come from the file's header).
type LCC struct {
	Type    string  `json:"type"`
	Lat0    float64 `json:"lat0"`
	Lon0    float64 `json:"lon0"`
	Lat1    float64 `json:"lat1"`
	Lat2    float64 `json:"lat2"`
	RadiusM float64 `json:"radius_m"`
}

type lccProjector struct {
	n, f, rho0, lon0, r float64
}

func newLCC(p LCC) (lccProjector, error) {
	if p.Type != "lcc" || p.RadiusM <= 0 || p.Lat1 <= -89 || p.Lat1 >= 89 || p.Lat2 <= -89 || p.Lat2 >= 89 {
		return lccProjector{}, errors.New("radar: unsupported projection")
	}
	rad := math.Pi / 180
	p1, p2, p0 := p.Lat1*rad, p.Lat2*rad, p.Lat0*rad
	var n float64
	if math.Abs(p1-p2) < 1e-10 {
		n = math.Sin(p1)
	} else {
		n = math.Log(math.Cos(p1)/math.Cos(p2)) / math.Log(math.Tan(math.Pi/4+p2/2)/math.Tan(math.Pi/4+p1/2))
	}
	f := math.Cos(p1) * math.Pow(math.Tan(math.Pi/4+p1/2), n) / n
	rho0 := p.RadiusM * f / math.Pow(math.Tan(math.Pi/4+p0/2), n)
	return lccProjector{n: n, f: f, rho0: rho0, lon0: p.Lon0 * rad, r: p.RadiusM}, nil
}

// forward is the projected (x, y) in metres of a latitude and longitude in degrees.
func (l lccProjector) forward(lat, lon float64) (x, y float64) {
	rad := math.Pi / 180
	rho := l.r * l.f / math.Pow(math.Tan(math.Pi/4+lat*rad/2), l.n)
	theta := l.n * (lon*rad - l.lon0)
	return rho * math.Sin(theta), l.rho0 - rho*math.Cos(theta)
}
