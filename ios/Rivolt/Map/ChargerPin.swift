import Foundation

/// What a charge's map pin says and where it goes, as the web's ChargeMap (web/src/lib/chargerNetwork.ts and poi.ts):
/// the nearest charger in the server's charger archive within 750 m (the basemap's charging stations when there is
/// none), kept only when it can't name the wrong network. Pure, so it can be tested.
enum ChargerPin {
    // MARK: Networks (chargerNetwork.ts)

    enum Network: Equatable {
        case tesla, teslaDestination, rivian, rivianWaypoints, electrifyAmerica, evgo, chargepoint, ionna, shellRecharge

        var display: String {
            switch self {
            case .tesla: return "Tesla Supercharger"
            case .teslaDestination: return "Tesla Destination"
            case .rivian: return "Rivian Adventure Network"
            case .rivianWaypoints: return "Rivian Waypoints"
            case .electrifyAmerica: return "Electrify America"
            case .evgo: return "EVgo"
            case .chargepoint: return "ChargePoint"
            case .ionna: return "IONNA"
            case .shellRecharge: return "Shell Recharge"
            }
        }
    }

    /// Reads a free-form network, operator, brand or name. The AC sub-brands first: they share the parent's name.
    static func networkKey(_ parts: String?...) -> Network? {
        let s = parts.compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " ").lowercased()
        guard !s.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        func has(_ pattern: String) -> Bool { s.range(of: pattern, options: .regularExpression) != nil }
        if has(#"\btesla\b.*\bdestination\b|\bdestination\b.*\btesla\b"#) { return .teslaDestination }
        if has(#"\brivian\b.*\bwaypoints?\b|\bwaypoints?\b"#) { return .rivianWaypoints }
        if has(#"\btesla\b|supercharger"#) { return .tesla }
        if has(#"\brivian\b|adventure network|\bran\b"#) { return .rivian }
        if has(#"electrify\s*america|\bea\b"#) { return .electrifyAmerica }
        if has(#"\bev\s*go\b|evgo"#) { return .evgo }
        if has(#"charge\s*point|chargepoint"#) { return .chargepoint }
        if has(#"\bionna\b"#) { return .ionna }
        if has(#"shell\s*recharge|\bshell\b"#) { return .shellRecharge }
        return nil
    }

    /// What a pin says for a network: the canonical name, else the ledger's own words.
    static func displayNetwork(_ known: String) -> String {
        networkKey(known)?.display ?? known.trimmingCharacters(in: .whitespaces)
    }

    /// A charger from the archive (or the basemap): where, what it calls itself, and how far from the charge.
    struct Node: Equatable {
        let lat: Double
        let lon: Double
        var name: String?
        var operatorName: String?
        var network: String?
        var brand: String?
        var maxPowerKW: Double?
        var distanceM: Double = 0
    }

    private static let dcOnly: [Network] = [.tesla, .rivian, .electrifyAmerica, .evgo, .ionna]

    /// Networks that only run DC fast chargers, or a node of 50 kW and up.
    static func isDcOnly(_ n: Node?) -> Bool {
        guard let n else { return false }
        if let kw = n.maxPowerKW, kw >= 50 { return true }
        guard let k = networkKey(n.network, n.operatorName, n.brand, n.name) else { return false }
        return dcOnly.contains(k)
    }

    struct Choice: Equatable {
        /// Use the snapped charger (else label the charge's own location).
        let snap: Bool
        let label: String
    }

    private static let unknownNetworks: Set<String> = ["", "unknown", "other", "n/a"]

    /// Never a wrong brand: a non-DC session never snaps to a DC-only node; an unknown ledger network keeps the snap
    /// and its name (or a generic label); a known one keeps the snap only when the node agrees, else the charge's own
    /// location carries the known network.
    static func choose(_ poi: Node?, known knownNetwork: String?, isDC: Bool = false) -> Choice {
        let known = (knownNetwork ?? "").trimmingCharacters(in: .whitespaces)
        let unknown = unknownNetworks.contains(known.lowercased())
        if !isDC && isDcOnly(poi) {
            return Choice(snap: false, label: unknown ? "Charging station" : displayNetwork(known))
        }
        let snappedLabel = (poi?.name).flatMap { $0.isEmpty ? nil : $0 } ?? "Charging station"
        if unknown {
            return poi != nil ? Choice(snap: true, label: snappedLabel) : Choice(snap: false, label: isDC ? "DC fast charger" : "Charging station")
        }
        guard let poi else { return Choice(snap: false, label: displayNetwork(known)) }
        let want = networkKey(known)
        let got = networkKey(poi.network, poi.operatorName, poi.brand, poi.name)
        if let want, let got, want == got { return Choice(snap: true, label: snappedLabel) }
        if want == nil && got == nil { return Choice(snap: true, label: snappedLabel) }
        return Choice(snap: false, label: displayNetwork(known))
    }

    /// What the map draws (ChargeMap's `choose`): the plain charge pin when there's neither a charger nor a network,
    /// the charge's own location labelled, or the snapped charger labelled with the recorded fix beside it.
    enum Pin: Equatable {
        case plain
        case own(label: String)
        case snapped(Node, label: String)
    }

    static func pin(snapped: Node?, network: String?, isDC: Bool) -> Pin {
        let known = (network ?? "").trimmingCharacters(in: .whitespaces)
        if snapped == nil && known.isEmpty { return .plain }
        let c = choose(snapped, known: known, isDC: isDC)
        if c.snap, let snapped { return .snapped(snapped, label: c.label) }
        return .own(label: c.label)
    }

    // MARK: The archive's features (poi.ts)

    static let snapRadiusM = 750.0

    /// A feature of the charger archive (or the basemap's pois) with its properties as MapLibre gives them.
    struct Candidate {
        let lat: Double
        let lon: Double
        let props: [String: Any]
    }

    /// The nearest charger within `radiusM`, skipping campgrounds without a charger of their own (the archive's
    /// Recreation.gov nodes); for the basemap only `kind == charging_station`, and only its name.
    static func nearest(_ cands: [Candidate], lat: Double, lon: Double, radiusM: Double = snapRadiusM, archive: Bool) -> Node? {
        var best: Node?
        for c in cands {
            if !archive && str(c.props, "kind") != "charging_station" { continue }
            let d = distanceM(lat, lon, c.lat, c.lon)
            guard d <= radiusM, d < (best?.distanceM ?? .infinity) else { continue }
            if archive && isCampgroundOnly(c.props) { continue }
            best = archive
                ? Node(lat: c.lat, lon: c.lon, name: str(c.props, "name"), operatorName: str(c.props, "operator"), network: str(c.props, "network"),
                       brand: str(c.props, "brand"), maxPowerKW: maxPowerKW(c.props), distanceM: d)
                : Node(lat: c.lat, lon: c.lon, name: str(c.props, "name"), distanceM: d)
        }
        return best
    }

    static func distanceM(_ lat1: Double, _ lon1: Double, _ lat2: Double, _ lon2: Double) -> Double {
        let r = 6_371_000.0
        let p1 = lat1 * .pi / 180, p2 = lat2 * .pi / 180
        let dp = (lat2 - lat1) * .pi / 180, dl = (lon2 - lon1) * .pi / 180
        let x = pow(sin(dp / 2), 2) + cos(p1) * cos(p2) * pow(sin(dl / 2), 2)
        return 2 * r * asin(min(1, x.squareRoot()))
    }

    static func str(_ p: [String: Any], _ k: String) -> String? {
        guard let v = p[k] as? String, !v.isEmpty else { return nil }
        return v
    }

    static func int(_ p: [String: Any], _ k: String) -> Int? {
        if let s = p[k] as? String { return Int(s.trimmingCharacters(in: .whitespaces)) }
        if let n = p[k] as? NSNumber, n.doubleValue.isFinite { return Int(n.doubleValue.rounded()) }
        return nil
    }

    /// "150 kW", "22kW", "11000 W" → kW.
    static func powerKW(_ v: Any?) -> Double? {
        guard let raw = v as? String else { return nil }
        var s = raw.trimmingCharacters(in: .whitespaces).lowercased().replacingOccurrences(of: ",", with: "")
        guard !s.isEmpty else { return nil }
        var watts = false
        if s.hasSuffix("kw") {
            s = String(s.dropLast(2))
        } else if s.hasSuffix("w") {
            watts = true
            s = String(s.dropLast())
        }
        guard let n = Double(s.trimmingCharacters(in: .whitespaces)), n.isFinite else { return nil }
        return watts ? n / 1000 : n
    }

    static func maxPowerKW(_ p: [String: Any]) -> Double? {
        var values: [Any?] = [p["charging:output"], p["maxpower"], p["maxpower:output"]]
        values += p.filter { $0.key.hasPrefix("socket:") && $0.key.hasSuffix(":output") }.map(\.value)
        return values.compactMap(powerKW).max()
    }

    /// The `socket:<type>` keys present (not `socket:<type>:output`, not "no", "0" or empty).
    static func socketTypes(_ p: [String: Any]) -> [String] {
        p.compactMap { k, raw in
            guard k.hasPrefix("socket:") else { return nil }
            let rest = String(k.dropFirst("socket:".count))
            guard !rest.isEmpty, !rest.contains(":") else { return nil }
            let v = (raw as? String)?.trimmingCharacters(in: .whitespaces).lowercased() ?? ""
            return v.isEmpty || v == "no" || v == "0" ? nil : rest
        }.sorted()
    }

    /// A campground or RV park with a hookup but no charger of its own (no EVSE count, no car connector).
    static func isCampgroundOnly(_ p: [String: Any]) -> Bool {
        let ft = (str(p, "facility_type") ?? "").uppercased()
        let campish = ft == "CAMPGROUND" || ft == "RV_PARK" || (int(p, "hookup_amps") ?? 0) > 0 || str(p, "ridb_id") != nil
        guard campish else { return false }
        if (int(p, "l2_count") ?? 0) > 0 || (int(p, "dcfc_count") ?? 0) > 0 { return false }
        return !socketTypes(p).contains { !$0.hasPrefix("nema") }
    }
}
