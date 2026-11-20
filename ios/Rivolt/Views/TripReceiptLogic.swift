import Foundation

/// What the trip receipt says, the web's `buildReceiptModel` (lib/tripReceipt.ts). The model is the privacy guarantee: built from the server's
/// receipt (areas and rounded figures, never a stored label or a coordinate) and carrying display text only, so nothing else can reach the painter.
struct TripReceiptModel: Equatable {
    struct Line: Equatable {
        var title: String
        var right: String
        var sub: String
        var subRight: String
    }

    struct Total: Equatable {
        var label: String
        var value: String
    }

    var head: (String, String)
    var title: String
    var sub: String
    var sketch: [String]?
    var lines: [Line]
    var totals: [Total]
    var cost: Total?
    var big: (String, String)
    var footer: [String]

    static func == (a: Self, b: Self) -> Bool {
        a.head == b.head && a.title == b.title && a.sub == b.sub && a.sketch == b.sketch && a.lines == b.lines && a.totals == b.totals
            && a.cost == b.cost && a.big == b.big && a.footer == b.footer
    }

    /// Every word the card says, for tests and the preview's accessible label.
    var text: String {
        ([head.0, head.1, title, sub] + lines.flatMap { [$0.title, $0.right, $0.sub, $0.subRight] } + totals.flatMap { [$0.label, $0.value] }
            + [cost.map { "\($0.label) \($0.value)" } ?? "", big.0, big.1] + footer).joined(separator: "\n")
    }
}

enum TripReceiptLogic {
    /// What the sheet says next to its buttons, in plain words (DA R4).
    static let says = "The image shows the start and destination areas, your departure day and time, and the charging stops. Stops you added show only as an area. Distances and times near the start, the destination and your stops are rounded."
    static let sketchSays = "A line only. The start, the destination and any stops you added are cut off."
    /// No place, date or name: the file name says the kind and nothing else (DA R9).
    static let fileName = "rivolt-trip-plan.png"
    static let footer = ["Planned with Rivolt · rivolt.dev", "An estimate; real conditions change it.", "Unofficial, not affiliated with Rivian."]

    /// Whole minutes: "1 h 27 min", "18 min".
    static func hmin(_ seconds: Double) -> String {
        let m = Int(seconds / 60 + 0.5)
        if m < 60 { return "\(m) min" }
        return "\(m / 60) h \(String(format: "%02d", m % 60)) min"
    }

    private static func pct(_ v: Double) -> String { "\(Int(v.rounded()))%" }

    private static func dist(_ m: Double, metric: Bool) -> String {
        let v = metric ? m / 1000 : m / 1609.344
        return "\(Int(v.rounded()).formatted(.number.locale(Locale(identifier: "en_US")))) \(metric ? "km" : "mi")"
    }

    /// The clock in a zone, always with the zone's short name: the leave time is in the viewer's zone and the arrival in the destination's, so a
    /// reader can reconcile the two with the total time.
    static func clock(_ date: Date, zone: TimeZone, locale: Locale) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = zone
        f.setLocalizedDateFormatFromTemplate("jmmz")
        return f.string(from: date)
    }

    private static func leaveLine(_ r: TripReceipt, zone: TimeZone, locale: Locale) -> String {
        let start = "start with \(pct(r.startSoc))"
        guard let d = r.departAt else { return start.prefix(1).uppercased() + start.dropFirst() }
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = zone
        f.setLocalizedDateFormatFromTemplate("EEEMMMd")
        return "Leave \(f.string(from: d)) · \(clock(d, zone: zone, locale: locale)) · \(start)"
    }

    private static func money(_ n: Double, _ currency: String) -> String {
        n.formatted(.currency(code: currency).precision(.fractionLength(0)))
    }

    /// The sketch is in the model only when the person asked for it and the server sent segments (it sends none when the cut leaves nothing); the
    /// cost line only when asked for and there is an estimate.
    static func model(_ r: TripReceipt, vehicle: String, metric: Bool, includeSketch: Bool, includeCost: Bool, cost: TripCost?,
                      viewerZone: TimeZone = .current, locale: Locale = .current) -> TripReceiptModel {
        let unit = metric ? 1000.0 : 1609.344
        let lines: [TripReceiptModel.Line] = r.rows.map { row in
            let tilde = row.rounded == true ? "~" : ""
            switch row.kind {
            case "drive":
                let d = row.distM ?? 0
                // A rounded leg that rounds to nothing is "under one step" (the server rounds to 5), not "about 0".
                let title = row.rounded == true && Int((d / unit).rounded()) == 0 ? "< 5 \(metric ? "km" : "mi")" : "\(tilde)\(dist(d, metric: metric))"
                return .init(title: "DRIVE · \(title)", right: (row.durS ?? 0) > 0 ? "\(tilde)\(hmin(row.durS ?? 0))" : "",
                             sub: "\(row.from ?? "") → \(row.to ?? "")", subRight: "\(pct(row.fromSoc)) → \(pct(row.toSoc))")
            case "charge":
                let kw = (row.maxKw ?? 0) > 0 ? " · \(Int((row.maxKw ?? 0).rounded())) kW" : ""
                return .init(title: "CHARGE\(kw)", right: hmin(row.durS ?? 0), sub: row.name ?? "", subRight: "\(pct(row.fromSoc)) → \(pct(row.toSoc))")
            default:
                // An added stop: how long it lasts when the plan says so, and the battery only when it changes.
                return .init(title: row.name ?? "", right: (row.durS ?? 0) > 0 ? hmin(row.durS ?? 0) : "", sub: "",
                             subRight: Int(row.fromSoc.rounded()) == Int(row.toSoc.rounded()) ? "" : "\(pct(row.fromSoc)) → \(pct(row.toSoc))")
            }
        }
        let t = r.totals
        var totals: [TripReceiptModel.Total] = [
            .init(label: "Driving", value: "~\(hmin(t.driveS))"),
            .init(label: "Charging · \(t.stops) \(t.stops == 1 ? "stop" : "stops")", value: hmin(t.chargeS)),
        ]
        if let arrive = t.arriveAt {
            let zone = r.tz.flatMap(TimeZone.init(identifier:)) ?? viewerZone
            totals.append(.init(label: "Arrive", value: "~\(clock(arrive, zone: zone, locale: locale)) · \(pct(t.arrivalSoc))"))
        }
        var costLine: TripReceiptModel.Total?
        if includeCost, let c = cost, let cur = c.currency, let lo = c.dcfcSpendLow ?? c.dcfcSpendHigh, let hi = c.dcfcSpendHigh ?? c.dcfcSpendLow, hi > 0 {
            costLine = .init(label: "Charging cost (estimate)", value: Int(lo.rounded()) == Int(hi.rounded()) ? money(hi, cur) : "\(money(lo, cur))–\(money(hi, cur))")
        }
        return TripReceiptModel(
            head: ("RIVOLT · TRIP PLAN", vehicle),
            title: "\(r.start) → \(r.end)",
            sub: leaveLine(r, zone: viewerZone, locale: locale),
            sketch: includeSketch && !(r.sketch ?? []).isEmpty ? r.sketch : nil,
            lines: lines,
            totals: totals,
            cost: costLine,
            big: ("~\(dist(t.distM, metric: metric))", "~\(hmin(t.driveS + t.chargeS))"),
            footer: footer)
    }

    /// The first cost estimate of a saved trip's plan, nil when it has none (or is a multi-day trip).
    static func cost(of trip: SavedTrip) -> TripCost? {
        trip.plan?.decoded(TripPlan.self)?.costs?.first
    }

    /// What to tell the person when the receipt could not be made. Never the server's own text.
    static func failure(_ error: Error) -> String {
        if case APIError.server(let status, _, _) = error {
            if status == 409 { return "A receipt isn't available for this trip yet (multi-day trips and trips with no route line)." }
            if status == 422 { return "This trip is too short for a receipt." }
        }
        return "Couldn't make the receipt. Try again."
    }
}
