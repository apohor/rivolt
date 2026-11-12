package histimport

// TezLab exports one CSV per kind (Activity tab: Drives, Charges). Nothing public documents its columns, so these aliases are assumptions
// (design §1) to be pinned to a real export in slice 2b. Add a spelling here, never a guess in the parser. The energy basis of its charges is
// unknown until then, so it is recorded as "unknown" and kept out of efficiency ratios.
var (
	TezLabDrives = Format{
		Source: "tezlab_import",
		Kind:   KindDrives,
		Fields: []Field{
			{Name: FStart, Dim: DimTime, Required: true, Aliases: []string{"start time", "started at", "start", "start date", "departure time", "begin time", "date"}},
			{Name: FEnd, Dim: DimTime, Required: true, Aliases: []string{"end time", "ended at", "end", "end date", "arrival time", "finish time"}},
			{Name: FDistance, Dim: DimDistance, Required: true, Aliases: []string{"distance", "drive distance", "trip distance", "distance driven"}},
			{Name: FStartSoC, Dim: DimSoC, Aliases: []string{"start battery", "start soc", "start battery level", "starting battery", "starting soc", "battery start"}},
			{Name: FEndSoC, Dim: DimSoC, Aliases: []string{"end battery", "end soc", "end battery level", "ending battery", "ending soc", "battery end"}},
			{Name: FStartOdo, Dim: DimDistance, Aliases: []string{"start odometer", "odometer start", "starting odometer"}},
			{Name: FEndOdo, Dim: DimDistance, Aliases: []string{"end odometer", "odometer end", "ending odometer"}},
			{Name: FStartLat, Dim: DimAngle, Aliases: []string{"start latitude", "start lat", "origin latitude"}},
			{Name: FStartLon, Dim: DimAngle, Aliases: []string{"start longitude", "start lon", "start lng", "origin longitude"}},
			{Name: FEndLat, Dim: DimAngle, Aliases: []string{"end latitude", "end lat", "destination latitude"}},
			{Name: FEndLon, Dim: DimAngle, Aliases: []string{"end longitude", "end lon", "end lng", "destination longitude"}},
			{Name: FMaxSpeed, Dim: DimSpeed, Aliases: []string{"max speed", "maximum speed", "top speed"}},
			{Name: FAvgSpeed, Dim: DimSpeed, Aliases: []string{"avg speed", "average speed"}},
			{Name: FEnergyUsed, Dim: DimEnergy, Aliases: []string{"energy used", "energy consumed", "consumed energy"}},
			{Name: FVehicle, Dim: DimText, Aliases: []string{"vehicle", "vehicle name", "car", "vin"}},
		},
	}
	TezLabCharges = Format{
		Source:      "tezlab_import",
		Kind:        KindCharges,
		EnergyBasis: "unknown",
		Fields: []Field{
			{Name: FStart, Dim: DimTime, Required: true, Aliases: []string{"start time", "started at", "start", "start date", "begin time", "date"}},
			{Name: FEnd, Dim: DimTime, Required: true, Aliases: []string{"end time", "ended at", "end", "end date", "finish time"}},
			{Name: FStartSoC, Dim: DimSoC, Aliases: []string{"start battery", "start soc", "start battery level", "starting battery", "starting soc", "battery start"}},
			{Name: FEndSoC, Dim: DimSoC, Aliases: []string{"end battery", "end soc", "end battery level", "ending battery", "ending soc", "battery end"}},
			{Name: FEnergyAdded, Dim: DimEnergy, Aliases: []string{"energy added", "added energy", "charge energy added", "energy", "charged"}},
			{Name: FMaxPower, Dim: DimPower, Aliases: []string{"max power", "peak power", "maximum power", "max charge rate"}},
			{Name: FAvgPower, Dim: DimPower, Aliases: []string{"avg power", "average power", "avg charge rate"}},
			{Name: FStartLat, Dim: DimAngle, Aliases: []string{"latitude", "lat", "location latitude"}},
			{Name: FStartLon, Dim: DimAngle, Aliases: []string{"longitude", "lon", "lng", "location longitude"}},
			{Name: FCost, Dim: DimMoney, Aliases: []string{"cost", "total cost", "charge cost", "price"}},
			{Name: FCurrency, Dim: DimText, Aliases: []string{"currency", "cost currency"}},
			{Name: FVehicle, Dim: DimText, Aliases: []string{"vehicle", "vehicle name", "car", "vin"}},
		},
	}
)
