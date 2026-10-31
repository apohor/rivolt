import SwiftUI

/// "Your year in Rivolt": the January card, the web's `lib/yearInRivolt.ts`. The figures come from the server; this builds the share card
/// from them. The model carries display text only (no place, day, time, VIN or truck name), and cost is opt-in every time the sheet opens.
enum YearCardLogic {
    static let hiddenKey = "rivolt.yearCard.hidden"

    /// The card is offered in January, for the year just ended.
    static func year(now: Date, calendar: Calendar = .current) -> Int? {
        let c = calendar.dateComponents([.year, .month], from: now)
        guard c.month == 1, let y = c.year else { return nil }
        return y - 1
    }

    /// "2026-07" → "July"; a malformed month is dropped.
    static func monthName(_ ym: String?) -> String? {
        guard let ym, ym.count == 7, ym[ym.index(ym.startIndex, offsetBy: 4)] == "-",
              let m = Int(ym.suffix(2)), (1...12).contains(m) else { return nil }
        var c = Calendar(identifier: .gregorian)
        c.locale = Locale(identifier: "en_US")
        return c.monthSymbols[m - 1]
    }

    static func fileName(_ year: Int) -> String { "rivolt-year-\(year).png" }

    /// Six rows at most; cost, when asked for, takes the third.
    static func card(_ y: YearInRivolt, includeCost: Bool) -> MoneyShareCard? {
        typealias L = MoneyLogic
        guard y.eligible, let miles = y.miles, miles > 0 else { return nil }
        let n0 = { (x: Double) in Int(x.rounded()).formatted() }
        var rows = [MoneyShareCard.Stat(label: "Driven", value: "\(n0(miles)) mi")]
        if let kwh = y.kwh, kwh > 0 { rows.append(.init(label: "kWh used driving", value: "\(n0(kwh)) kWh")) }
        if includeCost, let cost = y.cost {
            let approx = cost.estimated ? "~" : ""
            let perMile = (y.costPerMile ?? 0) > 0 ? " · \(approx)\(L.money(y.costPerMile ?? 0, cost.currency, digits: 2))/mi" : ""
            rows.append(.init(label: cost.estimated ? "Cost (estimate)" : "Cost", value: "\(approx)\(L.money(cost.amount, cost.currency, digits: 0))\(perMile)"))
        }
        // The year's efficiency is an estimate by construction, so it always says so.
        if let e = y.efficiencyMiPerKwh, e > 0 { rows.append(.init(label: "Efficiency", value: "about \(L.fixed(e, 1)) mi/kWh")) }
        if let t = y.longestTrip, t.miles > 0 {
            rows.append(.init(label: "Longest trip", value: "\(n0(t.miles)) mi" + (monthName(t.month).map { " · \($0)" } ?? "")))
        }
        if let s = y.energyShares {
            let pct = { (f: Double) in "\(Int((min(1, max(0, f)) * 100).rounded()))%" }
            rows.append(.init(label: "Charged", value: "\(pct(s.home)) home · \(pct(s.away)) away · \(pct(s.fast)) fast"))
        }
        if let c = y.coldestDrive {
            rows.append(.init(label: "Coldest drive", value: "\(Int(c.tempF.rounded()))°F" + (monthName(c.month).map { " · \($0)" } ?? "")))
        }
        if let d = y.mostEfficientLongDrive {
            rows.append(.init(label: "Best long drive", value: "\(L.fixed(d.miPerKwh, 1)) mi/kWh · \(n0(d.miles)) mi"))
        }
        var parts: [String] = []
        if let n = y.driveCount, n > 0 { parts.append("\(n.formatted()) \(L.plural(n, "drive", "drives"))") }
        if let n = y.chargeCount, n > 0 { parts.append("\(n.formatted()) \(L.plural(n, "charge", "charges"))") }
        // Zero is not a story: the line is hidden rather than "0 stops on 0 trips".
        if let t = y.plannedTrips, let s = y.plannedStops, t > 0, s > 0 {
            parts.append("\(s.formatted()) \(L.plural(s, "stop", "stops")) on \(t.formatted()) planned \(L.plural(t, "trip", "trips"))")
        }
        let since = monthName(y.sinceMonth)
        return MoneyShareCard(
            eyebrow: since.map { "Recorded by Rivolt · since \($0)" } ?? "Recorded by Rivolt",
            title: (y.model.flatMap { $0.isEmpty ? nil : $0 }).map { "\(y.year) in your \($0)" } ?? "\(y.year) in Rivolt",
            subtitle: parts.joined(separator: " · "),
            stats: Array(rows.prefix(6)),
            notes: [],
            compact: true)
    }
}

/// The card on Money in January. Not in the demo; nothing is asked of the server outside January.
struct YearCardView: View {
    @Environment(AppState.self) private var state
    @AppStorage(YearCardLogic.hiddenKey) private var hiddenYear = 0
    @State private var answer: YearInRivolt?
    @State private var sharing = false

    private var year: Int? { state.isDemo ? nil : YearCardLogic.year(now: .now) }

    var body: some View {
        Group {
            if let year, hiddenYear != year, let y = answer, let card = YearCardLogic.card(y, includeCost: false) {
                VStack(alignment: .leading, spacing: 8) {
                    Text(card.title).themeText(.value, color: Theme.textPrimary).accessibilityAddTraits(.isHeader)
                    Text("\(card.subtitle). A card you can share or keep; it is made on your phone and nothing is posted.")
                        .themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: 12) {
                        Button("See your year") { sharing = true }.buttonStyle(ThemeButtonStyle())
                        Button("Hide") { hiddenYear = year }.buttonStyle(ThemeSecondaryButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .themeCard(padding: 0)
                .accessibilityElement(children: .contain)
                .sheet(isPresented: $sharing) {
                    YearShareSheet(year: y).followsAppearance()
                }
            }
        }
        .task { await load() }
    }

    private func load() async {
        guard let year, hiddenYear != year else { return }
        if let a = try? await state.client.yearInRivolt(year: year), a.eligible { answer = a }
    }
}

/// "Share a card" for the year: the preview, the cost toggle (off every time it opens, never remembered) and Share. Made on the phone.
struct YearShareSheet: View {
    let year: YearInRivolt
    @Environment(\.dismiss) private var dismiss
    @State private var includeCost = false
    @State private var png: Data?

    private var card: MoneyShareCard? { YearCardLogic.card(year, includeCost: includeCost) }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Group {
                        if let png, let image = UIImage(data: png) {
                            Image(uiImage: image).resizable().aspectRatio(1, contentMode: .fit)
                        } else {
                            Theme.shareCardBackground.aspectRatio(1, contentMode: .fit)
                        }
                    }
                    .clipShape(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
                    .accessibilityElement()
                    .accessibilityLabel("Preview of the card: \(card.map { ([$0.eyebrow, $0.title, $0.subtitle] + $0.stats.map { "\($0.label) \($0.value)" }).joined(separator: ", ") } ?? "")")

                    if year.cost != nil {
                        Toggle(isOn: $includeCost) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Include cost").themeText(.value, color: Theme.textPrimary)
                                Text("Adds the estimated charging cost and cost per mile.").themeText(.subline).fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .tint(Theme.accentButton)
                    }
                    Text("Made on your device. Nothing is uploaded or posted.").themeText(.subline, color: Theme.textTertiary)
                    if let png {
                        ShareLink(item: MoneySharePNG(data: png, name: YearCardLogic.fileName(year.year)),
                                  preview: SharePreview("Rivolt", image: Image(uiImage: UIImage(data: png) ?? UIImage()))) {
                            Text("Share")
                        }
                        .buttonStyle(ThemeButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .navigationTitle("Share a card")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
            .task(id: card) { if let card { png = MoneyShareCardCanvas.png(card) } }
        }
    }
}
