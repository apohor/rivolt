import Foundation

/// Saving and reopening trips, with no view code so a test can see what is written. A saved trip is the web's shape, so each
/// side opens the other's: `inputs` has the origin and destination as `{lat, lon, label}`, the stops (a night is a stop flagged in
/// `overnight_flags`), and the settings; `plan` is the planner's answer as it came, or `{"kind":"multiday","days":[…]}`.
enum SavedTripLogic {
    /// "Seattle to Spokane": the first part of each place's name, short enough for a name field.
    static func suggestedName(origin: String?, destination: String) -> String {
        func head(_ s: String) -> String { s.split(separator: ",").first.map { $0.trimmingCharacters(in: .whitespaces) } ?? s }
        let to = head(destination)
        let name = origin.map { "\(head($0)) to \(to)" } ?? "To \(to)"
        return String(name.prefix(60))
    }

    private static func point(_ lat: Double, _ lon: Double, _ label: String) -> JSONValue {
        .object(["lat": .number(lat), "lon": .number(lon), "label": .string(label)])
    }

    /// The `datetime-local` text the web keeps a departure in: the phone's clock, no zone; empty means now.
    static func departureText(_ date: Date?, timeZone: TimeZone = .current) -> String {
        guard let date else { return "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "yyyy-MM-dd'T'HH:mm"
        return f.string(from: date)
    }

    /// The inputs of a trip. The truck's own charge is left empty (a re-plan reads it live, as the web's does); a charge the person
    /// typed is kept.
    static func inputs(origin: (lat: Double, lon: Double, label: String), destination: (lat: Double, lon: Double, label: String),
                       stay: OvernightCandidates.Candidate?, startingSoc: Double?, departAt: Date?, timeZone: TimeZone = .current) -> JSONValue {
        var o: [String: JSONValue] = [
            "origin": point(origin.lat, origin.lon, origin.label),
            "destination": point(destination.lat, destination.lon, destination.label),
            "extra_stops": .array(stay.map { [point($0.lat, $0.lon, $0.name)] } ?? []),
            "target_soc": .string(String(Int(TripPlanLogic.arrivalTargetPercent))),
            "starting_soc": .string(startingSoc.map { String(Int($0.rounded())) } ?? ""),
            "departure_at": .string(departureText(departAt, timeZone: timeZone)),
        ]
        if let stay {
            let kw = stay.l2Kw ?? 0
            o["overnight_flags"] = .array([.bool(true)])
            o["max_overnight_soc_pct"] = .number(80)
            o["overnight_l2_kw"] = .number(kw)
            o["overnight_departure_local"] = .string("08:00")
            o["overnight_settings"] = .array([.object([
                "lat": .number(stay.lat), "lon": .number(stay.lon), "until": .string("08:00"), "l2_kw": .number(kw), "max_soc_pct": .number(80),
            ])])
        }
        return .object(o)
    }

    /// The plan to keep: a two-day trip as the web keeps one (the days and totals as the planner answered), else the single plan as it came.
    static func planBlob(plan: TripPlan?, multiday: TripMultiday?) -> JSONValue? {
        if let raw = multiday?.raw, case .object(let o) = raw, let days = o["days"] {
            var out: [String: JSONValue] = ["kind": .string("multiday"), "days": days]
            for k in ["total", "simulated", "far_future"] { if let v = o[k] { out[k] = v } }
            return .object(out)
        }
        return plan?.raw
    }

    /// What a saved trip opens as. Nil when its inputs or plan do not read (a trip from somewhere else): it is then shown by name only.
    struct Restored: Equatable {
        var origin: PlaceMatch
        var destination: PlaceMatch
        var plan: TripPlan
        var multiday: TripMultiday?
        var stay: OvernightCandidates.Candidate?
    }

    static func restore(_ saved: SavedTrip) -> Restored? {
        guard case .object(let inputs)? = saved.inputs, let o = place(inputs["origin"]), let d = place(inputs["destination"]), let blob = saved.plan else { return nil }
        if case .object(let p) = blob, case .string("multiday")? = p["kind"] {
            guard let trip = blob.decoded(TripMultiday.self), let first = trip.days.first?.plan, first.best != nil else { return nil }
            var stay: OvernightCandidates.Candidate?
            if let day = trip.days.first(where: { $0.overnight != nil }), let la = day.end?.latitude, let lo = day.end?.longitude {
                stay = .init(name: day.overnight?.name ?? day.end?.label ?? "Stay", lat: la, lon: lo, l2Kw: day.overnight?.l2Kw)
            }
            return Restored(origin: o, destination: d, plan: first, multiday: trip, stay: stay)
        }
        guard let plan = blob.decoded(TripPlan.self), plan.best != nil else { return nil }
        return Restored(origin: o, destination: d, plan: plan, multiday: nil, stay: nil)
    }

    private static func place(_ v: JSONValue?) -> PlaceMatch? {
        guard case .object(let o)? = v, case .number(let la)? = o["lat"], case .number(let lo)? = o["lon"] else { return nil }
        var name = "Place"
        if case .string(let s)? = o["label"], !s.isEmpty { name = s }
        return PlaceMatch(name: name, latitude: la, longitude: lo, admin1: nil, country: nil)
    }

    /// What to tell the person when saving or opening failed. Never the server's own text.
    static func failure(_ error: Error) -> String {
        if case APIError.server(let status, _) = error {
            switch status {
            case 409: return "You already have a saved trip with that name. Choose another name, or update that one."
            case 413: return "This trip is too big to save."
            case 422: return "You have 100 saved trips. Delete one on the web to save another."
            case 400: return "This trip couldn't be saved. Check the name."
            case 404: return "That saved trip is gone. Save this one as a new trip."
            default: break
            }
        }
        return TripPlanLogic.failure(error)
    }
}
