import SwiftUI

/// The trip and milestone share cards, the web's `buildTripShareModel` and `buildMilestoneShareModel` (lib/shareCard.ts).
/// Like the month card, the model is the privacy guarantee: display text only, built from figures the screen already
/// has, so no map, place, route, coordinate, id or vehicle can reach the image. Cost is opt-in and off every time the
/// sheet opens.
extension MoneyShareCard {
    /// "rivolt-trip-2026-10.png" / "rivolt-milestone-2026-10.png": the kind and the month, never a drive or a place.
    static func fileName(kind: String, date: Date, calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month], from: date)
        return String(format: "rivolt-%@-%04d-%02d.png", kind, c.year ?? 0, c.month ?? 0)
    }

    static func trip(_ d: ActivityDrive, includeCost: Bool, calendar: Calendar) -> MoneyShareCard {
        typealias L = MoneyLogic
        let minutes = max(0, d.endedAt.timeIntervalSince(d.startedAt) / 60)
        var stats: [Stat] = []
        if d.energyUsedKwh > 0 && d.distanceMi > 0 { stats.append(Stat(label: "Efficiency", value: "\(L.fixed(d.distanceMi / d.energyUsedKwh, 2)) mi/kWh")) }
        if d.energyUsedKwh > 0 { stats.append(Stat(label: "Energy used", value: "\(L.fixed(d.energyUsedKwh, 1)) kWh")) }
        if d.startSocPct > 0 || d.endSocPct > 0 {
            stats.append(Stat(label: "Battery", value: "\(Int(d.startSocPct.rounded()))% → \(Int(d.endSocPct.rounded()))%"))
        }
        if d.avgSpeedMph > 0 { stats.append(Stat(label: "Average speed", value: "\(Int(d.avgSpeedMph.rounded())) mph")) }
        if includeCost, let cost = d.estimatedCost, cost > 0 {
            stats.append(Stat(label: "Charging cost (estimate)", value: "~" + L.money(cost, d.estimatedCurrency ?? "USD")))
        }
        // The day only, never the time of day.
        var f = Date.FormatStyle(timeZone: calendar.timeZone).weekday(.wide).day().month(.wide)
        f.calendar = calendar
        let duration = minutes >= 60 ? "\(Int(minutes / 60)) h \(Int((minutes.truncatingRemainder(dividingBy: 60)).rounded())) min" : "\(Int(minutes.rounded())) min"
        let title = "\(Int(d.distanceMi.rounded()).formatted()) mi" + (minutes > 0 ? " · \(duration)" : "")
        return MoneyShareCard(eyebrow: "Trip", title: title, subtitle: d.startedAt.formatted(f), stats: stats, notes: [])
    }

    static func milestone(_ m: Milestone, calendar: Calendar) -> MoneyShareCard {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).year()
        f.calendar = calendar
        // The server's words as given, but never a dollar figure: cost is opt-in on every card.
        let body = hasMoney(m.body) ? "" : m.body
        return MoneyShareCard(eyebrow: "Milestone", title: m.title, subtitle: [body, m.achievedAt.formatted(f)].filter { !$0.isEmpty }.joined(separator: " · "),
                              stats: [], notes: [])
    }

    private static let money = try! NSRegularExpression(pattern: #"[$€£]\s?\d|\d\s?(USD|EUR|GBP|CAD)\b"#, options: [.caseInsensitive])

    static func hasMoney(_ s: String) -> Bool {
        money.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
    }
}

/// "Share a card" for one trip or milestone: the preview, an optional cost toggle (off every time it opens, never
/// remembered) and Share through the iOS share sheet. Made on the phone; nothing is uploaded or posted.
struct ShareCardSheet: View {
    let fileName: String
    /// The cost toggle's hint, or nil when the card has no cost to add (then no toggle).
    var costHint: String?
    let build: (_ includeCost: Bool) -> MoneyShareCard
    @Environment(\.dismiss) private var dismiss

    @State private var includeCost = false
    @State private var png: Data?

    private var card: MoneyShareCard { build(includeCost) }

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
                    .accessibilityLabel("Preview of the card: \(([card.eyebrow, card.title, card.subtitle] + card.stats.map { "\($0.label) \($0.value)" } + card.notes).joined(separator: ", "))")

                    if let costHint {
                        Toggle(isOn: $includeCost) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Include cost").themeText(.value, color: Theme.textPrimary)
                                Text(costHint).themeText(.subline).fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .tint(Theme.accentButton)
                    }
                    Text("Made on your device. Nothing is uploaded or posted.").themeText(.subline, color: Theme.textTertiary)
                    if let png {
                        ShareLink(item: MoneySharePNG(data: png, name: fileName),
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
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
            .task(id: card) { png = MoneyShareCardCanvas.png(card) }
        }
    }
}
