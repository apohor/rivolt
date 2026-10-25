import Foundation

/// The words and figures of the trip planner, with no view code so a test can see them.
enum TripPlanLogic {
    /// "5 h 20 min" or "42 min".
    static func duration(_ seconds: Double) -> String { NowFormat.duration(seconds) }

    /// A distance in metres in the account's unit, whole and (from 60 mi / 100 km) rounded as the web's, so a route's length never gives a position away.
    static func distance(meters: Double, metric: Bool) -> String { MoneyLogic.distance(km: meters / 1000, metric: metric) }

    /// "Arrive 3:20 PM" at the last waypoint, or nil when the plan has no times. When the arrival is not on the day of departure it
    /// names the day: "Sat 2:45 AM", or "Oct 31, 2:45 AM" from a week out. Times are in `timeZone` (the phone's; the plan does not
    /// say which zone the destination is in).
    static func arrival(_ route: TripRoute, departure: Date? = nil, timeZone: TimeZone = .current) -> String? {
        guard let at = route.waypoints.last?.arrivalTimeUTC?.date else { return nil }
        return when(at, since: departure ?? route.waypoints.first?.departureTimeUTC?.date, timeZone: timeZone)
    }

    /// A time of day, with the day in front when it is not the day of `since`.
    static func when(_ at: Date, since: Date?, timeZone: TimeZone = .current) -> String {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = timeZone
        guard let since, !cal.isDate(at, inSameDayAs: since) else {
            return at.formatted(Date.FormatStyle(date: .omitted, time: .shortened, timeZone: timeZone))
        }
        let days = cal.dateComponents([.day], from: cal.startOfDay(for: since), to: cal.startOfDay(for: at)).day ?? 0
        let time = at.formatted(Date.FormatStyle(date: .omitted, time: .shortened, timeZone: timeZone))
        if days > 0 && days < 7 {
            return "\(at.formatted(Date.FormatStyle(timeZone: timeZone).weekday(.abbreviated))) \(time)"
        }
        return "\(at.formatted(Date.FormatStyle(timeZone: timeZone).month(.abbreviated).day())), \(time)"
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

    static func overnightRequest(_ route: TripRoute) -> OvernightCandidatesRequest {
        OvernightCandidatesRequest(path: path(route), driveSec: route.totalDriveDurationSec, budgetHours: longDriveSeconds / 3600, kinds: ["hotel", "campground"])
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
                                stay: OvernightCandidates.Candidate, timeZone: TimeZone = .current) -> TripMultidayRequest {
        TripMultidayRequest(
            vehicleId: vehicleID, startingSoc: startingSoc, packKwh: packKwh, startAt: rfc3339(startAt), tz: timeZone.identifier,
            targetArrivalSocPercent: arrivalTargetPercent, origin: TripPoint(lat: origin.lat, lon: origin.lon),
            days: [
                TripDayRequest(end: TripPoint(lat: stay.lat, lon: stay.lon, label: stay.name), targetArrivalSocPct: arrivalTargetPercent,
                               overnight: .init(l2Kw: stay.l2Kw ?? 0, until: "08:00", maxSocPct: 80, name: stay.name)),
                TripDayRequest(end: TripPoint(lat: destination.lat, lon: destination.lon), targetArrivalSocPct: arrivalTargetPercent),
            ],
            maxOvernightSocPct: 80)
    }

    /// "Leave 8:43 AM · arrive 5:10 PM at about 18%" for one day.
    static func dayLine(_ day: TripMultiday.Day, since: Date?, timeZone: TimeZone = .current) -> String {
        var parts: [String] = []
        if let d = day.departAt?.date { parts.append("Leave \(when(d, since: since, timeZone: timeZone))") }
        if let a = day.arriveAt?.date { parts.append("arrive \(when(a, since: day.departAt?.date, timeZone: timeZone))") }
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
        return n == 0 ? "No charging stop needed" : "\(n) charging \(n == 1 ? "stop" : "stops")"
    }

    /// "200 kW · 25 min · 31% to 72%", leaving out what the plan did not say.
    static func stopDetail(_ s: TripWaypoint) -> String {
        var parts: [String] = []
        if let kw = s.maxPowerKW, kw > 0 { parts.append("\(Int(kw.rounded())) kW") }
        if let c = s.chargeDurationSec, c > 0 { parts.append(duration(c)) }
        if let a = s.arrivalSoC, let d = s.departureSoC { parts.append("\(Int(a.rounded()))% to \(Int(d.rounded()))%") }
        return parts.joined(separator: " · ")
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
