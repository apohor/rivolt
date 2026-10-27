import Foundation

/// The price book's rules, apart from the view so a test can read them. A port of the web's `lib/networkRates.ts`,
/// `lib/networkLocation.ts` and `lib/membershipTerms.ts`, with the same words.
enum ChargingNetworksForm {
    static let unsetRateNote = "Enter the rate you pay. Prices vary by site."
    static let explanation = "Member rates and the \"I have this plan\" toggle feed the trip planner cost strip. Guest rate doubles as the one-click prefill on each charge's pricing card."
    static let emptyNote = "No networks configured yet. Add EVgo, Electrify America, or whichever fast-charge networks you use most so you can one-click apply their rate when pricing a session."
    static let locationHint = "Only used to label your own charges near it. Clear it to remove it."
    static let applyNote = "Pricing earlier sessions at a saved rate is done on the web."
    static let resetWarning = "Reset all networks to the built-in defaults? Your customizations will be lost."
    static let maxRate = 5.0

    /// A rate of the person's own is set (guest or member price above 0).
    static func rateSet(_ n: ChargingNetwork) -> Bool { n.pricePerKwh > 0 || (n.memberPricePerKwh ?? 0) > 0 }

    /// The saving between guest and member prices, only when both were entered and the member price is the lower one.
    static func memberSaving(_ n: ChargingNetwork) -> Double {
        guard n.pricePerKwh > 0, let m = n.memberPricePerKwh, m > 0, m < n.pricePerKwh else { return 0 }
        return n.pricePerKwh - m
    }

    /// Whether the row has a membership tier to edit (a member rate, or a built-in network).
    static func hasMembership(_ n: ChargingNetwork) -> Bool { (n.memberPricePerKwh ?? 0) > 0 || !(n.slug ?? "").isEmpty }

    /// Preferred networks first, then by name; the indexes are the rows' own so an edit lands on the right one.
    static func displayOrder(_ rows: [ChargingNetwork]) -> [Int] {
        rows.indices.sorted { a, b in
            let pa = rows[a].preferred == true, pb = rows[b].preferred == true
            if pa != pb { return pa }
            let c = rows[a].name.compare(rows[b].name, options: [.caseInsensitive, .diacriticInsensitive])
            return c == .orderedSame ? a < b : c == .orderedAscending
        }
    }

    static func newRow(currency: String) -> ChargingNetwork { ChargingNetwork(name: "", pricePerKwh: 0, currency: currency) }

    /// A rate typed in a field: empty is 0 (not set); a comma or a point is the decimal mark; 0 to 5 per kWh, four places.
    static func parseRate(_ text: String) -> Result<Double, ChargingRatesForm.RateError> { ChargingRatesForm.parse(text) }

    /// "lat, lon" as typed: empty is no location; otherwise both numbers in range and not 0, 0.
    enum LocationResult: Equatable {
        case none
        case at(Double, Double)
        case invalid(String)
    }

    static func parseLocation(_ text: String) -> LocationResult {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return .none }
        let parts = t.split(whereSeparator: { $0 == "," || $0 == " " || $0 == "\t" }).map(String.init)
        guard parts.count == 2, let a = Double(parts[0]), let b = Double(parts[1]), a.isFinite, b.isFinite else {
            return .invalid("Enter a latitude and a longitude, e.g. 40.7128, -74.0060.")
        }
        if a < -90 || a > 90 { return .invalid("Latitude must be between -90 and 90.") }
        if b < -180 || b > 180 { return .invalid("Longitude must be between -180 and 180.") }
        if a == 0 && b == 0 { return .invalid("That is 0, 0 (off the coast of Africa); enter the network's location.") }
        return .at(a, b)
    }

    static func locationText(_ n: ChargingNetwork) -> String {
        guard let lat = n.lat, let lon = n.lon else { return "" }
        return "\(trim(lat)), \(trim(lon))"
    }

    private static func trim(_ v: Double) -> String {
        var s = String(format: "%.6f", v)
        while s.hasSuffix("0") { s.removeLast() }
        if s.hasSuffix(".") { s.removeLast() }
        return s
    }

    /// A row as edited: rates and the location are text until they are valid.
    struct Draft: Equatable, Identifiable {
        let id: UUID
        var net: ChargingNetwork
        var guest: String
        var member: String
        /// "lat, lon", shown for a network of the person's own only.
        var location: String

        init(_ n: ChargingNetwork, id: UUID = UUID()) {
            self.id = id
            net = n
            guest = ChargingRatesForm.fieldText(n.pricePerKwh)
            member = ChargingRatesForm.fieldText(n.memberPricePerKwh ?? 0)
            location = ChargingNetworksForm.locationText(n)
        }

        /// A member rate above 0 is typed.
        var memberEntered: Bool { ((try? ChargingRatesForm.parse(member).get()) ?? 0) > 0 }
        /// A guest or member rate above 0 is typed (the unset note shows until one is).
        var rateEntered: Bool { ((try? ChargingRatesForm.parse(guest).get()) ?? 0) > 0 || memberEntered }
    }

    static func drafts(_ rows: [ChargingNetwork]) -> [Draft] { rows.map { Draft($0) } }

    struct FormError: Error, Equatable {
        let message: String
        init(_ m: String) { message = m }
    }

    /// The list as it goes out (the save replaces the whole list): names trimmed, currencies upper-case, the member flag only where a
    /// member rate exists, a location only on a row of the person's own. The first problem comes back as a sentence.
    static func prepared(_ drafts: [Draft]) -> Result<[ChargingNetwork], FormError> {
        var out: [ChargingNetwork] = []
        for d in drafts {
            var r = d.net
            r.name = r.name.trimmingCharacters(in: .whitespaces)
            r.currency = r.currency.uppercased()
            let who = r.name.isEmpty ? "A network" : r.name
            switch parseRate(d.guest) {
            case .success(let v): r.pricePerKwh = v
            case .failure(let e): return .failure(FormError("\(who): \(e.message)"))
            }
            if hasMembership(r) || !d.member.trimmingCharacters(in: .whitespaces).isEmpty {
                switch parseRate(d.member) {
                case .success(let v): r.memberPricePerKwh = v > 0 ? v : nil
                case .failure(let e): return .failure(FormError("\(who): \(e.message)"))
                }
            }
            if let m = r.memberPricePerKwh, m >= r.pricePerKwh, r.pricePerKwh > 0 {
                return .failure(FormError("\(who): the member rate must be lower than the guest rate."))
            }
            if (r.slug ?? "").isEmpty {
                switch parseLocation(d.location) {
                case .none: r.lat = nil; r.lon = nil
                case .at(let a, let b): r.lat = a; r.lon = b
                case .invalid(let why): return .failure(FormError("\(who): \(why)"))
                }
            }
            if (r.memberPricePerKwh ?? 0) <= 0 { r.memberActive = nil }
            out.append(r)
        }
        return .success(out)
    }

    /// Whether Save is live: the drafts are valid and differ from what is saved.
    static func changed(_ drafts: [Draft], saved: [ChargingNetwork]) -> Bool {
        guard case .success(let rows) = prepared(drafts) else { return true }
        return rows != saved
    }

    static func savingText(_ s: Double, currency: String) -> String { "save \(NumberText.fixed(s, 2))/kWh" }

    // MARK: Membership terms

    static let termsTitle = "Membership plans, as the networks list them"
    static let termsNote = "Read from each network's own page, with the date it was last checked. These are the plans' terms, not your prices; plans change, so check the network before you sign up."

    struct TermsGroup: Equatable {
        let slug: String
        let network: String
        let plans: [MembershipTerm]
    }

    static func groupByNetwork(_ rows: [MembershipTerm]) -> [TermsGroup] {
        var out: [TermsGroup] = []
        for r in rows {
            if let i = out.firstIndex(where: { $0.slug == r.networkSlug }) {
                out[i] = TermsGroup(slug: out[i].slug, network: out[i].network, plans: out[i].plans + [r])
            } else {
                out.append(TermsGroup(slug: r.networkSlug, network: r.network, plans: [r]))
            }
        }
        return out
    }

    private static let sourceHosts = ["rivian.com", "electrifyamerica.com", "evgo.com", "shell.us", "tesla.com"]

    /// A source is linked only when it is an https page on one of the networks' own sites.
    static func safeSourceURL(_ s: String?) -> URL? {
        guard let s, let u = URL(string: s), u.scheme?.lowercased() == "https", u.user == nil, u.password == nil,
              let host = u.host?.lowercased() else { return nil }
        return sourceHosts.contains { host == $0 || host.hasSuffix("." + $0) } ? u : nil
    }

    /// "Read from the network's page." / "Read from a search snippet." after the label, as on the web.
    static func readLine(_ t: MembershipTerm) -> String {
        guard let via = t.readVia, !via.isEmpty else { return t.label }
        return "\(t.label) Read from \(via == "page" ? "the network's page" : "a search snippet")."
    }
}
