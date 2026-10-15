package rivian

import (
	"hash/fnv"
	"math"
	"math/rand"
	"time"
)

// A drive's shape inside the sample corpus. The plan fixes a drive's
// distance, duration and start/end SoC; the profile decides how the
// speed, odometer, position and SoC move between them: stop-and-go in
// the city, a steady highway with ramps and the odd slowdown, SoC that
// follows power (and upticks under regen). It is deterministic per drive
// (seeded from the drive's start and distance, never from the corpus
// RNG, so the rest of the plan is unchanged) and every total is
// preserved exactly: the profile is renormalised so the integral of
// speed equals the distance and the SoC fraction ends at 1.
type sampleProfile struct {
	speed []float64 // mph at frame i
	dist  []float64 // fraction of the drive's distance covered before frame i (len n+1)
	soc   []float64 // fraction of the drive's SoC drop spent before frame i (len n+1)
	// modeAt is the frame at which the drive mode switches to "sport";
	// -1 for the whole drive in "everyday".
	modeAt int
}

const (
	profileStepS    = 5.0
	profileMassKg   = 2800.0
	profileCrr      = 0.010
	profileCdAHalfR = 0.6 // 0.5 * rho * CdA, kg/m
	profileAccessKW = 0.8
	profileEtaDrive = 0.90
	profileEtaRegen = 0.65
)

// newSampleProfile builds the profile of a drive of `miles` over `dur`
// at an average of cruise mph. step is the frame cadence.
func newSampleProfile(start time.Time, miles float64, dur, step time.Duration, cruise float64) *sampleProfile {
	n := int((dur + step - 1) / step)
	if n < 4 || miles <= 0 || cruise <= 0 {
		return nil
	}
	h := fnv.New64a()
	var b [16]byte
	u := uint64(start.UnixNano())
	m := math.Float64bits(miles)
	for i := 0; i < 8; i++ {
		b[i] = byte(u >> (8 * i))
		b[8+i] = byte(m >> (8 * i))
	}
	_, _ = h.Write(b[:])
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	dt := step.Seconds()
	v := make([]float64, n)
	if cruise < 45 {
		cityProfile(v, rng, dt)
	} else {
		highwayProfile(v, rng, dt, cruise)
	}

	// The last frame may cover less than a full step.
	w := make([]float64, n)
	for i := range w {
		w[i] = 1
	}
	if last := dur.Seconds() - float64(n-1)*dt; last > 0 && last < dt {
		w[n-1] = last / dt
	}
	// Renormalise: sum(w*v)*dt/3600 == miles.
	var integ float64
	for i := range v {
		integ += w[i] * v[i]
	}
	integ *= dt / 3600
	if integ <= 0 {
		return nil
	}
	k := miles / integ
	for i := range v {
		v[i] = round1(v[i] * k)
	}
	// Re-integrate the rounded series so dist ends at exactly 1.
	p := &sampleProfile{speed: v, dist: make([]float64, n+1), soc: make([]float64, n+1), modeAt: -1}
	var tot float64
	for i := range v {
		tot += w[i] * v[i]
	}
	if tot <= 0 {
		return nil
	}
	var acc float64
	for i := range v {
		p.dist[i] = acc / tot
		acc += w[i] * v[i]
	}
	p.dist[n] = 1

	// Energy per step from a longitudinal force balance with a smooth
	// seeded grade; regen steps are negative.
	phi1, phi2 := rng.Float64()*2*math.Pi, rng.Float64()*2*math.Pi
	T1, T2 := (8+rng.Float64()*12)*60, (25+rng.Float64()*30)*60
	amp := 0.012
	if cruise >= 45 {
		amp = 0.025
	}
	e := make([]float64, n)
	var sum float64
	for i := range v {
		u0 := v[i] * 0.44704
		u1 := 0.0
		if i+1 < n {
			u1 = v[i+1] * 0.44704
		}
		a := (u1 - u0) / dt
		t := float64(i) * dt
		grade := amp * (math.Sin(2*math.Pi*t/T1+phi1) + 0.5*math.Sin(2*math.Pi*t/T2+phi2))
		uMid := (u0 + u1) / 2
		pw := (profileCrr*profileMassKg*9.81*uMid + profileCdAHalfR*uMid*uMid*uMid +
			profileMassKg*a*uMid + profileMassKg*9.81*grade*uMid) / 1000
		var pb float64
		if pw >= 0 {
			pb = pw/profileEtaDrive + profileAccessKW
		} else {
			pb = pw*profileEtaRegen + profileAccessKW
		}
		e[i] = pb * dt / 3600 * w[i]
		sum += e[i]
	}
	if sum <= 1e-9 {
		copy(p.soc, p.dist) // degenerate: follow distance
	} else {
		var acc float64
		for i := range e {
			p.soc[i] = acc / sum
			acc += e[i]
		}
		p.soc[n] = 1
	}
	// About one drive in ten, over ~15 minutes, changes drive mode once.
	if rng.Float64() < 0.10 && dur >= 15*time.Minute {
		p.modeAt = int(float64(n) * (0.35 + 0.30*rng.Float64()))
	}
	return p
}

// cityProfile fills v with stop-and-go: lights (0 mph for 20-60 s),
// accelerations to 25-45 mph, holds with a little noise.
func cityProfile(v []float64, rng *rand.Rand, dt float64) {
	const (
		stopped = iota
		accel
		hold
		decel
	)
	phase, timer := stopped, 5+rng.Float64()*10
	var target, cur float64
	n := len(v)
	for i := 0; i < n; i++ {
		switch phase {
		case stopped:
			cur = 0
			timer -= dt
			if timer <= 0 {
				phase, target = accel, 25+rng.Float64()*20
			}
		case accel:
			cur = math.Min(target, cur+2.2*dt)
			if cur >= target {
				phase, timer = hold, 30+rng.Float64()*90
			}
		case hold:
			cur = target + (rng.Float64()*2-1)*1.5
			timer -= dt
			if timer <= 0 {
				if rng.Float64() < 0.7 {
					phase = decel
				} else {
					target = 25 + rng.Float64()*20
					phase = accel
				}
			}
		case decel:
			cur = math.Max(0, cur-3.0*dt)
			if cur <= 0 {
				phase, timer = stopped, 20+rng.Float64()*40
			}
		}
		out := cur
		// Come to rest at the very end.
		if rem := float64(n-1-i) * dt; out > rem*0.8 {
			out = rem * 0.8
		}
		v[i] = math.Max(0, out)
	}
}

// highwayProfile fills v with a steady cruise near the limit: small
// noise, a gentle on-ramp and off-ramp, and a few traffic slowdowns.
func highwayProfile(v []float64, rng *rand.Rand, dt float64, cruise float64) {
	limit := cruise * 1.04
	n := len(v)
	var noise, slowLeft, slowTarget float64
	cur := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) * dt
		rem := float64(n-1-i) * dt
		noise = 0.8*noise + (rng.Float64()*2-1)*1.2
		noise = math.Max(-3, math.Min(3, noise))
		want := limit + noise
		if slowLeft > 0 {
			want = slowTarget + noise*0.5
			slowLeft -= dt
		} else if rng.Float64() < 0.004 { // roughly one slowdown per 20 min
			slowLeft = 60 + rng.Float64()*180
			slowTarget = limit * (0.55 + 0.2*rng.Float64())
		}
		// Ramps: surface streets, a gentle on-ramp and off-ramp.
		if c := 3 + t*0.55; want > c {
			want = c
		}
		if c := rem * 0.55; want > c {
			want = c
		}
		// Rate-limit: 1.6 mph/s up, 2.5 mph/s down.
		cur = math.Max(cur-2.5*dt, math.Min(cur+1.6*dt, want))
		v[i] = math.Max(0, cur)
	}
}
