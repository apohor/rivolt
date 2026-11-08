import Foundation

/// Which charger type a power is, and how it is said. The rule is the web's (web/src/lib/chargerLabels.ts): a wall outlet at 2 kW or less;
/// Medium (Level 2) up to the most a vehicle takes on AC; anything above that is DC, so Fast, shown with its real kW wherever there is one
/// ("Fast · peak 40 kW"). Money's and Activity's Fast bucket is a different rule (`dcfcMinKw`); charge detail says when they differ.
extension ChargerLabels {
    /// The ledger's DC threshold (web DCFC_MIN_KW): what Money and Activity count as a Fast session.
    static let dcfcMinKw = 50.0
    /// The most a Rivian takes on AC (about 11.5 kW); above this a power is DC (web AC_MAX_KW).
    static let acMaxKw = 12.0

    /// `dc` is what a station's connectors say (true or false), nil when unknown (a measured session peak), which is judged by power alone.
    static func kind(kw: Double?, dc: Bool? = nil) -> ChargerKind? {
        guard let kw, kw.isFinite, kw > 0 else { return nil }
        if kw <= 2 { return .outlet }
        if dc == false { return .medium }
        if dc == true { return .fast }
        return kw <= acMaxKw ? .medium : .fast
    }

    /// A station's own rating next to its type: "Fast · station up to 250 kW" (the word alone with no rating).
    static func stationLine(_ kind: ChargerKind, ratedKw: Double?) -> String {
        guard let ratedKw, ratedKw > 0 else { return label(kind) }
        return "\(label(kind)) · station up to \(Int(ratedKw.rounded())) kW"
    }

    /// A session's type with the vehicle's measured peak: "Fast · peak 40 kW"; nil with no power.
    static func sessionLine(peakKw: Double) -> String? {
        guard let kind = kind(kw: peakKw) else { return nil }
        return "\(label(kind)) · peak \(Int(peakKw.rounded())) kW"
    }

    /// When a session is Fast here but under Money's threshold, the bucket Money counts it in ("Home" or "Away").
    static func moneyNote(peakKw: Double, ledgerIsDC: Bool, place: String?) -> String? {
        guard kind(kw: peakKw) == .fast, peakKw < dcfcMinKw, !ledgerIsDC, let place, place == "Home" || place == "Away" else { return nil }
        return "Counted as \(place) in Money (Fast there means \(Int(dcfcMinKw)) kW and up)."
    }

    /// Money's Fast bucket, under its legend.
    static let moneyFastNote = "DC sessions peaking at \(Int(dcfcMinKw)) kW and up."
}
