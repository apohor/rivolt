package histimport

import (
	"errors"
	"math"
	"time"
)

// sessions reads a table with one drive or charge per row.
func (p *parser) sessions(t Table) error {
	var fatal error
	err := readAll(t, func(row []string) {
		if fatal != nil {
			return
		}
		p.rep.Rows++
		if p.noteVehicle(row) {
			p.rep.Skipped[ReasonOtherVeh]++
			return
		}
		if p.f.Kind == KindDrives {
			fatal = p.driveRow(row)
		} else {
			fatal = p.chargeRow(row)
		}
	})
	if err != nil {
		return err
	}
	if fatal != nil {
		return fatal
	}
	p.scaleSoC()
	p.checkAll()
	return nil
}

// rowErr maps a read failure onto a skip reason, or returns the zone question as fatal.
func (p *parser) rowErr(err error) (fatal error) {
	var nc *NeedChoiceError
	if errors.As(err, &nc) {
		return err
	}
	if errors.Is(err, errBadValue) {
		p.reject(ReasonBadNumber, 1)
		return nil
	}
	p.reject(ReasonMissing, 1)
	return nil
}

func (p *parser) driveRow(row []string) error {
	d := Drive{Source: p.f.Source, VehicleLabel: p.val(row, FVehicle)}
	var err error
	if d.StartedAt, err = p.when(row, FStart); err != nil {
		return p.timeErr(err)
	}
	if d.EndedAt, err = p.when(row, FEnd); err != nil {
		return p.timeErr(err)
	}
	read := func(name string, dst *float64, fn func([]string, string) (float64, bool, error)) bool {
		v, ok, e := fn(row, name)
		if e != nil {
			p.reject(ReasonBadNumber, 1)
			return false
		}
		if ok {
			*dst = v
		}
		return true
	}
	ok := read(FDistance, &d.DistanceMi, p.miles) && read(FStartOdo, &d.StartOdometerMi, p.miles) && read(FEndOdo, &d.EndOdometerMi, p.miles) &&
		read(FStartSoC, &d.StartSoCPct, p.number3) && read(FEndSoC, &d.EndSoCPct, p.number3) &&
		read(FStartLat, &d.StartLat, p.number3) && read(FStartLon, &d.StartLon, p.number3) &&
		read(FEndLat, &d.EndLat, p.number3) && read(FEndLon, &d.EndLon, p.number3) &&
		read(FMaxSpeed, &d.MaxSpeedMph, p.mph) && read(FAvgSpeed, &d.AvgSpeedMph, p.mph)
	if !ok {
		return nil
	}
	if v, has, e := p.kwh(row, FEnergyUsed); e != nil {
		p.reject(ReasonBadNumber, 1)
		return nil
	} else if has {
		d.EnergyUsedKWh = v
	}
	if d.DistanceMi == 0 && d.EndOdometerMi > d.StartOdometerMi && d.StartOdometerMi > 0 {
		d.DistanceMi = d.EndOdometerMi - d.StartOdometerMi
	}
	p.socSeen(d.StartSoCPct, d.EndSoCPct)
	if len(p.drives) >= MaxSessions {
		return ErrTooBig
	}
	p.drives = append(p.drives, d)
	return nil
}

func (p *parser) chargeRow(row []string) error {
	c := Charge{Source: p.f.Source, EnergyBasis: p.f.EnergyBasis, VehicleLabel: p.val(row, FVehicle), Currency: p.val(row, FCurrency)}
	if c.EnergyBasis == "" {
		c.EnergyBasis = "unknown"
	}
	var err error
	if c.StartedAt, err = p.when(row, FStart); err != nil {
		return p.timeErr(err)
	}
	if c.EndedAt, err = p.when(row, FEnd); err != nil {
		return p.timeErr(err)
	}
	var energy float64
	var hasEnergy bool
	read := func(name string, dst *float64, fn func([]string, string) (float64, bool, error)) bool {
		v, ok, e := fn(row, name)
		if e != nil {
			p.reject(ReasonBadNumber, 1)
			return false
		}
		if ok {
			*dst = v
		}
		return true
	}
	ok := read(FStartSoC, &c.StartSoCPct, p.number3) && read(FEndSoC, &c.EndSoCPct, p.number3) &&
		read(FMaxPower, &c.MaxPowerKW, p.kw) && read(FAvgPower, &c.AvgPowerKW, p.kw) &&
		read(FStartLat, &c.Lat, p.number3) && read(FStartLon, &c.Lon, p.number3)
	if !ok {
		return nil
	}
	var e error
	if energy, hasEnergy, e = p.kwh(row, FEnergyAdded); e != nil {
		p.reject(ReasonBadNumber, 1)
		return nil
	}
	if hasEnergy {
		c.EnergyAddedKWh = energy
	}
	if v, has, e := p.number(row, FCost); e != nil {
		p.reject(ReasonBadNumber, 1)
		return nil
	} else if has {
		c.Cost, c.HasCost = v, true
	}
	if !c.HasCost {
		c.Currency = ""
	}
	c.EnergyEstimated = !hasEnergy // resolved after the SoC scale is known
	p.socSeen(c.StartSoCPct, c.EndSoCPct)
	if len(p.charges) >= MaxSessions {
		return ErrTooBig
	}
	p.charges = append(p.charges, c)
	return nil
}

// number3 adapts number to the reader signature used by the row readers.
func (p *parser) number3(row []string, name string) (float64, bool, error) {
	return p.number(row, name)
}

func (p *parser) timeErr(err error) error {
	var nc *NeedChoiceError
	if errors.As(err, &nc) {
		return err
	}
	p.reject(ReasonBadTime, 1)
	return nil
}

// SoC scale: the header may say (% or fraction); else a file whose every value is at most 1 is a fraction. Decided after the pass.
func (p *parser) socSeen(a, b float64) {
	p.socMax = math.Max(p.socMax, math.Max(a, b))
}

func (p *parser) scaleSoC() {
	p.socFrac = p.socHint == "fraction" || (p.socHint == "" && p.socMax > 0 && p.socMax <= 1)
	p.rep.SoCScale = "percent"
	if p.socFrac {
		p.rep.SoCScale = "fraction"
		for i := range p.drives {
			p.drives[i].StartSoCPct *= 100
			p.drives[i].EndSoCPct *= 100
		}
		for i := range p.charges {
			p.charges[i].StartSoCPct *= 100
			p.charges[i].EndSoCPct *= 100
		}
	}
}

// checkAll derives the fields that need the scale, then drops the sessions that fail a check, counting each by reason.
func (p *parser) checkAll() {
	out := p.drives[:0]
	for _, d := range p.drives {
		p.deriveDrive(&d)
		if r := checkDrive(&d, p.o.Now); r != "" {
			p.reject(r, 1)
			continue
		}
		out = append(out, d)
	}
	p.drives = out
	outc := p.charges[:0]
	for _, c := range p.charges {
		p.deriveCharge(&c)
		if r := checkCharge(&c, p.o.Now); r != "" {
			p.reject(r, 1)
			continue
		}
		outc = append(outc, c)
	}
	p.charges = outc
}

func (p *parser) deriveDrive(d *Drive) {
	hours := d.EndedAt.Sub(d.StartedAt).Hours()
	if d.AvgSpeedMph == 0 && hours > 0 {
		d.AvgSpeedMph = d.DistanceMi / hours
	}
	if d.EnergyUsedKWh == 0 && p.o.PackKWh > 0 && d.StartSoCPct > d.EndSoCPct {
		d.EnergyUsedKWh = (d.StartSoCPct - d.EndSoCPct) / 100 * p.o.PackKWh
		d.EnergyEstimated = true
	}
}

func (p *parser) deriveCharge(c *Charge) {
	if c.EnergyAddedKWh == 0 && c.EnergyEstimated && p.o.PackKWh > 0 && c.EndSoCPct > c.StartSoCPct {
		c.EnergyAddedKWh = (c.EndSoCPct - c.StartSoCPct) / 100 * p.o.PackKWh
	} else {
		c.EnergyEstimated = false
	}
	if c.EnergyEstimated && c.EnergyBasis != "battery" {
		c.EnergyBasis = "battery" // SoC × pack is pack-side, whatever the file's own basis is
	}
	hours := c.EndedAt.Sub(c.StartedAt).Hours()
	if c.AvgPowerKW == 0 && hours > 0 && c.EnergyAddedKWh > 0 {
		c.AvgPowerKW = c.EnergyAddedKWh / hours
	}
	if c.MaxPowerKW == 0 {
		c.MaxPowerKW = c.AvgPowerKW
	}
	c.SessionType = "ac"
	if c.MaxPowerKW >= dcThresholdKW {
		c.SessionType = "dc"
	}
}

// checkDrive returns the reason a drive is not kept, or "".
func checkDrive(d *Drive, now time.Time) string {
	dur := d.EndedAt.Sub(d.StartedAt)
	switch {
	case dur < 0:
		return ReasonEndBeforeBeg
	case d.EndedAt.After(now):
		return ReasonFuture
	case dur > maxDriveDur:
		return ReasonTooLong
	case badSoC(d.StartSoCPct) || badSoC(d.EndSoCPct):
		return ReasonSoCRange
	case d.DistanceMi < 0 || d.DistanceMi > maxDistMi || d.StartOdometerMi < 0 || d.EndOdometerMi < 0:
		return ReasonDistance
	case d.EnergyUsedKWh < 0 || d.EnergyUsedKWh > maxEnergyKWh:
		return ReasonEnergy
	case d.MaxSpeedMph < 0 || d.MaxSpeedMph > maxSpeedMph || d.AvgSpeedMph < 0 || (dur > 0 && d.DistanceMi/dur.Hours() > maxSpeedMph) || (dur == 0 && d.DistanceMi > 0):
		return ReasonSpeed
	case d.DistanceMi == 0:
		return ReasonEmpty
	}
	return ""
}

func checkCharge(c *Charge, now time.Time) string {
	dur := c.EndedAt.Sub(c.StartedAt)
	switch {
	case dur < 0:
		return ReasonEndBeforeBeg
	case c.EndedAt.After(now):
		return ReasonFuture
	case dur > maxChargeDur:
		return ReasonTooLong
	case badSoC(c.StartSoCPct) || badSoC(c.EndSoCPct):
		return ReasonSoCRange
	case c.EnergyAddedKWh < 0 || c.EnergyAddedKWh > maxEnergyKWh:
		return ReasonEnergy
	case c.MaxPowerKW < 0 || c.MaxPowerKW > maxPowerKW || c.AvgPowerKW < 0:
		return ReasonPower
	case c.Cost < 0:
		return ReasonEnergy
	case dur == 0 && c.EnergyAddedKWh == 0:
		return ReasonEmpty
	}
	return ""
}

func badSoC(v float64) bool { return v < 0 || v > 100 || !finite(v) }
