package histimport

// ABRP's Drive & charge history export is an .xlsx of recorded points (design §1; the format is reported by users, not documented). The
// aliases are assumptions (see AliasProvenance). Drives and charges are derived from the points (points.go); points do not go into
// vehicle_state. SoC on a Rivian is whole percent only, so energy from SoC steps is coarse by about 1.3 kWh.
var ABRPPoints = Format{
	Source: "abrp_import",
	Kind:   KindPoints,
	Fields: []Field{
		{Name: FTime, Dim: DimTime, Required: true, Aliases: []string{"time", "timestamp", "date", "datetime", "date time", "utc"}},
		{Name: FLat, Dim: DimAngle, Required: true, Aliases: []string{"latitude", "lat"}},
		{Name: FLon, Dim: DimAngle, Required: true, Aliases: []string{"longitude", "lon", "lng", "long"}},
		{Name: FSoC, Dim: DimSoC, Required: true, Aliases: []string{"soc", "state of charge", "battery level", "battery", "battery soc"}},
		{Name: FSpeed, Dim: DimSpeed, Aliases: []string{"speed", "vehicle speed", "gps speed"}},
		{Name: FOdometer, Dim: DimDistance, Aliases: []string{"odometer", "odo"}},
		{Name: FSessionID, Dim: DimText, Aliases: []string{"drive id", "session", "session id", "trip id", "trip", "drive"}},
		{Name: FVehicle, Dim: DimText, Aliases: []string{"vehicle", "vehicle name", "car", "vin"}},
	},
}
