import Foundation

/// The words and figures of the trip planner, with no view code so a test can see them.
enum TripPlanLogic {
    /// "5 h 20 min" or "42 min".
    static func duration(_ seconds: Double) -> String { NowFormat.duration(seconds) }

    /// A distance in metres in the account's unit, whole and (from 60 mi / 100 km) rounded as the web's, so a route's length never gives a position away.
    static func distance(meters: Double, metric: Bool) -> String { MoneyLogic.distance(km: meters / 1000, metric: metric) }

    /// "Arrive 3:20 PM" at the last waypoint, or nil when the plan has no times. The time is the destination's own clock (`timeZone`,
    /// the plan's `destination_tz`; the phone's when the plan names none), with the zone said ("MDT") when it is not the phone's.
    /// When the arrival is not on the day of departure it names the day: "Sat 2:45 AM", or "Oct 31, 2:45 AM" from a week out.
    static func arrival(_ route: TripRoute, departure: Date? = nil, timeZone: TimeZone = .current, phoneZone: TimeZone = .current) -> String? {
        guard let at = route.waypoints.last?.arrivalTimeUTC?.date else { return nil }
        return when(at, since: departure ?? route.waypoints.first?.departureTimeUTC?.date, timeZone: timeZone, sinceZone: phoneZone, labelAgainst: phoneZone)
    }

    /// A zone from a plan's IANA name; nil when it names none or an unknown one (the caller then keeps the phone's).
    static func zone(_ name: String?) -> TimeZone? { name.flatMap { $0.isEmpty ? nil : TimeZone(identifier: $0) } }

    /// A time of day in `timeZone`, with the day in front when it is not the day of `since` (read in `sinceZone`), and the zone's
    /// abbreviation after it when `labelAgainst` is given and its clock differs.
    static func when(_ at: Date, since: Date?, timeZone: TimeZone = .current, sinceZone: TimeZone? = nil, labelAgainst: TimeZone? = nil) -> String {
        func parts(_ d: Date, _ z: TimeZone) -> DateComponents {
            var cal = Calendar(identifier: .gregorian)
            cal.timeZone = z
            return cal.dateComponents([.year, .month, .day], from: d)
        }
        var text = at.formatted(Date.FormatStyle(date: .omitted, time: .shortened, timeZone: timeZone))
        if let since {
            let a = parts(at, timeZone), b = parts(since, sinceZone ?? timeZone)
            if a != b {
                var utc = Calendar(identifier: .gregorian)
                utc.timeZone = TimeZone(identifier: "UTC")!
                let days = utc.dateComponents([.day], from: utc.date(from: b)!, to: utc.date(from: a)!).day ?? 0
                if days > 0 && days < 7 {
                    text = "\(at.formatted(Date.FormatStyle(timeZone: timeZone).weekday(.abbreviated))) \(text)"
                } else {
                    text = "\(at.formatted(Date.FormatStyle(timeZone: timeZone).month(.abbreviated).day())), \(text)"
                }
            }
        }
        if let other = labelAgainst, other.secondsFromGMT(for: at) != timeZone.secondsFromGMT(for: at), let abbr = timeZone.abbreviation(for: at) {
            text += " \(abbr)"
        }
        return text
    }

    /// The charge to arrive with that the planner is asked for, as on the web.
    static let arrivalTargetPercent = 20.0

    /// Said when the plan ends below the arrival target; nil otherwise.
    static func lowArrival(_ soc: Double?) -> String? {
        guard let soc, soc < arrivalTargetPercent else { return nil }
        return "That is below the \(Int(arrivalTargetPercent))% arrival target. Add a charging stop or start with more charge."
    }

    /// Driving longer than this in a day is offered as two (the web's sleep-after hours).
    static let longDriveSeconds = 8.0 * 3600

    static func isLongDrive(_ route: TripRoute) -> Bool {
        (route.totalDriveDurationSec ?? 0) > longDriveSeconds && route.destinationReached != false
    }

    /// Where the route goes, for finding a place to sleep: the waypoints in order.
    static func path(_ route: TripRoute) -> [[Double]] {
        route.waypoints.compactMap { w in w.latitude.flatMap { la in w.longitude.map { [la, $0] } } }
    }

    /// The search runs along the road line when there is one (thinned to a few hundred points), else the plan's waypoints: between
    /// sparse waypoints a straight line can leave the road by tens of kilometres, and the search area with it.
    static func overnightRequest(_ route: TripRoute, roadLine: [RouteSnap.Coord] = []) -> OvernightCandidatesRequest {
        let line = thin(roadLine, to: 600).map { [$0.lat, $0.lon] }
        return OvernightCandidatesRequest(path: line.count >= 2 ? line : path(route), driveSec: route.totalDriveDurationSec, budgetHours: longDriveSeconds / 3600, kinds: ["hotel", "campground"])
    }

    /// At most `limit` points, evenly spread, keeping both ends.
    static func thin(_ line: [RouteSnap.Coord], to limit: Int) -> [RouteSnap.Coord] {
        guard line.count > limit, limit >= 2 else { return line }
        let step = Double(line.count - 1) / Double(limit - 1)
        return (0..<limit).map { line[min(line.count - 1, Int((Double($0) * step).rounded()))] }
    }

    /// What to say when no stay is in range, as the web does: how far the search looked on each side of the point the day's driving
    /// budget runs out (minutes become a distance at the route's own average speed), and how far in that point is.
    static func noStaysLine(_ found: OvernightCandidates, driveSec: Double?, metric: Bool) -> String {
        let generic = "No hotel or campground with a listed charger near where the first day would end. Pick a stop by hand on the web."
        guard let t = found.target, let route = found.routeM, route > 0, let drive = driveSec, drive > 0,
              let before = t.windowBeforeMin, before > 0, let after = t.windowAfterMin, after > 0 else { return generic }
        func metres(_ min: Double) -> String { distance(meters: min * 60 * route / drive, metric: metric) }
        var line = "No hotel or campground with a listed charger within about \(metres(before)) before or \(metres(after)) after your \(Int(longDriveSeconds / 3600)) h point"
        if let along = t.alongM, along > 0 { line += " (about \(distance(meters: along, metric: metric)) in)" }
        return line + ". Pick a stop by hand on the web."
    }

    /// "Nearest listed stay: Name, about 3.3 h short of the day (about 5 h in)." for each side the server found one, before first.
    static func nearestLines(_ found: OvernightCandidates) -> [String] {
        func hours(_ s: Double) -> String { NumberText.fixed(abs(s) / 3600, 1) }
        return [found.nearest?.before, found.nearest?.after].compactMap { n -> String? in
            guard let n, let off = n.offsetSec else { return nil }
            let name = n.name ?? "a stay"
            let side = off < 0 ? "\(hours(off)) h short of the day" : "\(hours(off)) h past the day"
            let into = n.driveSec.map { " (about \(hours($0)) h in)" } ?? ""
            return "Nearest listed stay: \(name), about \(side)\(into)."
        }
    }

    /// The whole empty answer: how far the search looked, then the nearest listed stay on each side.
    static func noStaysMessage(_ found: OvernightCandidates, driveSec: Double?, metric: Bool) -> String {
        ([noStaysLine(found, driveSec: driveSec, metric: metric)] + nearestLines(found)).joined(separator: "\n")
    }

    /// A hotel with no charger listed: says so, and names the fast charger the next morning starts at.
    static func fallbackLine(_ s: OvernightCandidates.FallbackStay, metric: Bool) -> String {
        guard let f = s.fastCharger else { return "No charger listed here." }
        var line = "No charger listed here. Nearest fast charger: \(f.name ?? "a fast charger")"
        if let d = f.distanceM, d > 0 { line += ", \(distance(meters: d, metric: metric)) away" }
        if let kw = f.maxPowerKw, kw > 0 { line += " (\(Int(kw.rounded())) kW)" }
        return line + "."
    }

    /// The credit the OpenStreetMap hotels need wherever they show, and the licence it links to.
    static let osmCredit = "© OpenStreetMap contributors"
    static let odblURL = "https://opendatacommons.org/licenses/odbl/1-0/"

    /// A stay with no listed charger as a night: no charging there, so the truck leaves on what it arrived with.
    static func candidate(_ s: OvernightCandidates.FallbackStay) -> OvernightCandidates.Candidate {
        .init(name: s.name, lat: s.lat, lon: s.lon, l2Kw: 0, detourM: s.detourM, kind: "hotel")
    }

    /// DC fast chargers of at least this power are shown along a route; slower ones are clutter for a truck.
    static let alongMinKw = 100.0

    /// The request for the chargers along a route: the road line when there is one (thinned), else the plan's waypoints.
    static func alongRequest(_ route: TripRoute, roadLine: [RouteSnap.Coord]) -> ChargersAlongRequest {
        let line = thin(roadLine, to: 1500).map { [$0.lat, $0.lon] }
        return ChargersAlongRequest(path: line.count >= 2 ? line : path(route), filter: "dcfc", minPowerKw: alongMinKw)
    }

    /// "Sample Fast Charge · 250 kW · Example Network" for a charger picked on the map.
    static func alongLine(_ c: AlongCharger) -> String {
        var parts = [c.name ?? "Fast charger"]
        if let kw = c.maxPowerKW, kw > 0 { parts.append("\(Int(kw.rounded())) kW") }
        if let n = c.evNetwork, !n.isEmpty { parts.append(n) }
        return parts.joined(separator: " · ")
    }

    /// "12 fast chargers within reach of the route" (a listing; whether one is free or working isn't checked).
    static func alongCount(_ n: Int) -> String {
        n == 0 ? "No fast chargers of 100 kW or more are listed near this route." : "\(n) fast charger\(n == 1 ? "" : "s") of 100 kW or more near the route. Listings only: Rivolt can't tell whether one is free or working."
    }

    /// "Hotel · Level 2 charger, about 7 kW" for the list of places to sleep; the power is the listing's, never a promise.
    static func candidateLine(_ c: OvernightCandidates.Candidate, metric: Bool) -> String {
        var parts: [String] = [c.kind == "campground" ? "Campground" : "Hotel"]
        if let kw = c.l2Kw, kw > 0 { parts.append("\(c.l2KwAssumed == true ? "about " : "")\(Int(kw.rounded())) kW listed") }
        if let d = c.detourM, d >= 500 { parts.append("\(distance(meters: d, metric: metric)) off the route") }
        return parts.joined(separator: " · ")
    }

    /// Two days, split at the stay the person picked. The charge and the truck are the server's to fill in.
    static func multidayRequest(vehicleID: String?, startingSoc: Double?, packKwh: Double?, startAt: Date,
                                origin: (lat: Double, lon: Double), destination: (lat: Double, lon: Double),
                                stay: OvernightCandidates.Candidate, morningCharger: OvernightCandidates.FastCharger? = nil,
                                timeZone: TimeZone = .current) -> TripMultidayRequest {
        // A stay with no charger of its own starts the next day at a fast charger, planned as that day's first stop.
        let morning = morningCharger.flatMap { c in c.lat.flatMap { la in c.lon.map { TripPoint(lat: la, lon: $0, label: c.name, entityId: c.entityId) } } }
        return TripMultidayRequest(
            vehicleId: vehicleID, startingSoc: startingSoc, packKwh: packKwh, startAt: rfc3339(startAt), tz: timeZone.identifier,
            targetArrivalSocPercent: arrivalTargetPercent, origin: TripPoint(lat: origin.lat, lon: origin.lon),
            days: [
                TripDayRequest(end: TripPoint(lat: stay.lat, lon: stay.lon, label: stay.name), targetArrivalSocPct: arrivalTargetPercent,
                               overnight: .init(l2Kw: stay.l2Kw ?? 0, until: "08:00", maxSocPct: 80, name: stay.name)),
                TripDayRequest(vias: morning.map { [$0] }, end: TripPoint(lat: destination.lat, lon: destination.lon), targetArrivalSocPct: arrivalTargetPercent),
            ],
            maxOvernightSocPct: 80)
    }

    /// "Leave 8:43 AM · arrive 5:10 PM at about 18%" for one day. The day leaves on the clock of `departZone` and arrives on the
    /// clock of `arriveZone` (the day's `end_tz`); a zone that is not the phone's is named.
    static func dayLine(_ day: TripMultiday.Day, since: Date?, departZone: TimeZone = .current, arriveZone: TimeZone = .current,
                        phoneZone: TimeZone = .current) -> String {
        var parts: [String] = []
        if let d = day.departAt?.date { parts.append("Leave \(when(d, since: since, timeZone: departZone, sinceZone: phoneZone, labelAgainst: phoneZone))") }
        if let a = day.arriveAt?.date {
            parts.append("arrive \(when(a, since: day.departAt?.date, timeZone: arriveZone, sinceZone: departZone, labelAgainst: phoneZone))")
        }
        var line = parts.joined(separator: " · ")
        if let soc = day.arrivalSoc { line += "\(line.isEmpty ? "A" : " a")t about \(Int(soc.rounded()))%" }
        return line
    }

    /// "Parked about 10 h, charging to 80%" for the night after a day.
    static func nightLine(_ o: TripMultiday.Overnight) -> String {
        var parts: [String] = []
        if let h = o.parkedHours, h > 0 { parts.append("Parked about \(Int(h.rounded())) h") }
        if let kw = o.l2Kw, kw > 0 { parts.append("Level 2 at \(Int(kw.rounded())) kW listed") } else { parts.append("no charger") }
        if let p = o.postChargeSocPct, (o.addedKwh ?? 0) > 0 { parts.append("to about \(Int(p.rounded()))%") }
        return parts.joined(separator: " · ")
    }

    /// The one-line summary under the route: how long it takes and how far.
    static func summary(_ route: TripRoute, metric: Bool) -> String {
        var parts: [String] = []
        if let d = route.totalDriveDistanceMeters { parts.append(distance(meters: d, metric: metric)) }
        if let t = route.totalDriveDurationSec { parts.append("\(duration(t)) driving") }
        if let c = route.totalChargingDurationSec, c > 0 { parts.append("\(duration(c)) charging") }
        return parts.joined(separator: " · ")
    }

    /// "1 charging stop", "3 charging stops", or "No charging stop needed".
    static func stopsTitle(_ route: TripRoute) -> String {
        let n = route.chargingStops.count
        if n == 0 && (route.totalChargingDurationSec ?? 0) > 0 { return "Charging" }
        return n == 0 ? "No charging stop needed" : "\(n) charging \(n == 1 ? "stop" : "stops")"
    }

    /// Under the stops title when there are no stops to list. A plan that charges never says the battery needs none: if its stops
    /// can't be read, it says how long it charges and that they aren't listed.
    static func noStopsLine(_ route: TripRoute) -> String {
        if let c = route.totalChargingDurationSec, c > 0 {
            return "This plan charges for about \(duration(c)), but its stops aren't listed here. Open it on the web to see them."
        }
        return "The battery reaches the destination without charging."
    }

    /// "200 kW · 25 min · 31% to 72%", leaving out what the plan did not say.
    static func stopDetail(_ s: TripWaypoint) -> String {
        var parts: [String] = []
        if let kw = s.maxPowerKW, kw > 0 { parts.append("\(Int(kw.rounded())) kW") }
        if let c = s.chargeDurationSec, c > 0 { parts.append(duration(c)) }
        if let a = s.arrivalSoC, let d = s.departureSoC { parts.append("\(Int(a.rounded()))% to \(Int(d.rounded()))%") }
        return parts.joined(separator: " · ")
    }

    /// Each charging stop's leg (metres from the stop before it, or from the start) and the last leg to the destination, from the
    /// plan's distances from the start. A leg is nil where the plan gives none (a distance of 0 past the start means missing).
    static func legs(_ route: TripRoute) -> (stops: [Double?], last: Double?) {
        var prev = 0.0
        var stops: [Double?] = []
        var last: Double?
        for w in route.waypoints {
            guard let d = w.distanceFromOriginMeters, d > 0 else {
                if w.isCharger { stops.append(nil) }
                continue
            }
            if w.isCharger {
                stops.append(d >= prev ? d - prev : nil)
                prev = d
            } else if w.waypointType?.lowercased() == "destination" {
                last = d >= prev ? d - prev : nil
            }
        }
        return (stops, last)
    }

    /// "172 mi from the start", "130 mi from the last stop".
    static func legLine(_ meters: Double?, first: Bool, metric: Bool) -> String? {
        guard let meters, meters > 0 else { return nil }
        return "\(distance(meters: meters, metric: metric)) from \(first ? "the start" : "the last stop")"
    }

    /// What charging at stop `index` is expected to cost, always an estimate; the stop's own price at the charger when there is none.
    static func stopCostLine(_ cost: TripCost?, index: Int) -> String? {
        guard let stops = cost?.breakdown, stops.indices.contains(index), let cur = cost?.currency else { return nil }
        let s = stops[index]
        guard let low = s.costLow, let high = s.costHigh else { return "Price shown at the charger." }
        let l = NumberText.money(low, cur), h = NumberText.money(high, cur)
        return "About \(abs(high - low) < 0.005 ? l : "\(l) to \(h)") (estimate)"
    }

    /// What to say when the battery would not reach the destination; nil when it does (or the plan does not say).
    static func shortfall(_ route: TripRoute, metric: Bool) -> String? {
        guard route.destinationReached == false || (route.batteryEmptyToDestMeters ?? 0) > 0 else { return nil }
        if let m = route.batteryEmptyToDestMeters, m > 0 {
            return "The battery would run out about \(distance(meters: m, metric: metric)) before the destination. Start with more charge, or add a stop."
        }
        return "This route doesn't reach the destination. Start with more charge, or add a stop."
    }

    /// The fast-charging estimate, always called an estimate: "About $11.50 to $14.00 for the fast-charging stops (estimate)".
    static func costLine(_ cost: TripCost?) -> String? {
        guard let c = cost, let low = c.dcfcSpendLow, let high = c.dcfcSpendHigh, (c.pricedStops ?? 0) > 0, let cur = c.currency else { return nil }
        let l = NumberText.money(low, cur), h = NumberText.money(high, cur)
        let range = abs(high - low) < 0.005 ? l : "\(l) to \(h)"
        let unpriced = (c.unpricedStops ?? 0) > 0 ? " Some stops have no price yet and are left out." : ""
        return "About \(range) for the fast-charging stops (estimate).\(unpriced)"
    }

    /// What charging the same energy at home would cost, as a comparison; nil without a home figure.
    static func homeLine(_ cost: TripCost?) -> String? {
        guard let c = cost, let home = c.homeEquivalent, home > 0, let cur = c.currency else { return nil }
        return "The same energy at home: about \(NumberText.money(home, cur)) (estimate)."
    }

    /// RFC 3339 in UTC for a departure time, as the server takes it.
    static func rfc3339(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.string(from: date)
    }

    /// The request for a trip: the truck's charge only when the person set one, the departure only when it is not now.
    static func request(vehicleID: String?, origin: (lat: Double, lon: Double), destination: (lat: Double, lon: Double),
                        startingSoc: Double?, departAt: Date?, packKwh: Double?) -> TripPlanRequest {
        TripPlanRequest(
            vehicleId: vehicleID, startingSoc: startingSoc, originBearing: 0, departAt: departAt.map(rfc3339),
            waypoints: [TripPlanWaypoint(latitude: origin.lat, longitude: origin.lon, waypointType: "origin"),
                        TripPlanWaypoint(latitude: destination.lat, longitude: destination.lon, waypointType: "destination")],
            targetArrivalSocPercent: arrivalTargetPercent, packKwh: packKwh)
    }

    /// What to tell the person when planning failed. Never the server's own text.
    static func failure(_ error: Error) -> String {
        switch error {
        case APIError.rivianReauth:
            return "Rivian needs to be reconnected. Do that in Rivolt on the web, then plan again."
        case APIError.planQuota(let after):
            if let after, after > 0 { return "You've used this hour's planning allowance. Try again in about \(max(1, Int((Double(after) / 60).rounded(.up)))) min." }
            return "You've used this hour's planning allowance. Try again in a while."
        case APIError.server(let status, _):
            switch status {
            case 400: return "That trip couldn't be planned. Check the places and the time."
            case 403: return "This server doesn't allow trip planning from the app."
            case 404: return "Trip planning isn't turned on for this server."
            case 409: return "The truck hasn't reported where it is. Pick a starting place."
            case 429: return "Too many requests. Wait a minute, then try again."
            case 500...599: return "The planner isn't answering right now. Try again in a minute."
            default: break
            }
        default: break
        }
        return UserFacingError.words(error)
    }
}
