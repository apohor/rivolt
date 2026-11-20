import SwiftUI

/// The trip and milestone share cards, the web's `buildTripShareModel` and `buildMilestoneShareModel` (lib/shareCard.ts).
/// Like the month card, the model is the privacy guarantee: display text only, built from figures the screen already
/// has, so no place, coordinate, id or vehicle can reach the image. The one exception is the trip's opt-in map: the
/// server's trimmed postcard route, drawn as a bare line. Cost and the map are opt-in and off every time the sheet opens.
extension MoneyShareCard {
    /// "rivolt-trip-2026-10.png" / "rivolt-milestone-2026-10.png": the kind and the month, never a drive or a place.
    static func fileName(kind: String, date: Date, calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month], from: date)
        return String(format: "rivolt-%@-%04d-%02d.png", kind, c.year ?? 0, c.month ?? 0)
    }

    /// Whole minutes, cut not rounded: the detail row's duration (ActivityLogic.shortDuration) cuts too, so the card says the same time.
    static func tripDuration(minutes: Double) -> String {
        let whole = Int(minutes)
        return whole >= 60 ? "\(whole / 60) h \(whole % 60) min" : "\(whole) min"
    }

    /// `metric` follows the detail screen's unit (the account's setting), so the card says the same distance and speed.
    static func trip(_ d: ActivityDrive, includeCost: Bool, routeSegments: [String] = [], calendar: Calendar, metric: Bool = NowFormat.usesMetric) -> MoneyShareCard {
        typealias L = MoneyLogic
        let k = metric ? 1.609344 : 1
        let dist = metric ? "km" : "mi"
        // The detail row's own duration: driving time first, so a merged round trip does not count the time parked between legs.
        let minutes = max(0, (ActivityLogic.duration(d) ?? 0) / 60)
        var stats: [Stat] = []
        if d.energyUsedKwh > 0 && d.distanceMi > 0 { stats.append(Stat(label: "Efficiency", value: "\(L.fixed(d.distanceMi * k / d.energyUsedKwh, 2)) \(dist)/kWh")) }
        if d.energyUsedKwh > 0 { stats.append(Stat(label: "Energy used", value: "\(L.fixed(d.energyUsedKwh, 1)) kWh")) }
        if d.startSocPct > 0 || d.endSocPct > 0 {
            stats.append(Stat(label: "Battery", value: "\(Int(d.startSocPct.rounded()))% → \(Int(d.endSocPct.rounded()))%"))
        }
        if d.avgSpeedMph > 0 { stats.append(Stat(label: "Average speed", value: "\(Int((d.avgSpeedMph * k).rounded())) \(metric ? "km/h" : "mph")")) }
        if includeCost, let cost = d.estimatedCost, cost > 0 {
            stats.append(Stat(label: "Charging cost (estimate)", value: "~" + L.money(cost, d.estimatedCurrency ?? "USD")))
        }
        // The day only, never the time of day.
        var f = Date.FormatStyle(timeZone: calendar.timeZone).weekday(.wide).day().month(.wide)
        f.calendar = calendar
        let duration = tripDuration(minutes: minutes)
        let title = "\(Int((d.distanceMi * k).rounded()).formatted()) \(dist)" + (minutes > 0 ? " · \(duration)" : "")
        return MoneyShareCard(eyebrow: "Trip", title: title, subtitle: d.startedAt.formatted(f), stats: stats, notes: [], routeSegments: routeSegments)
    }

    static func milestone(_ m: Milestone, calendar: Calendar) -> MoneyShareCard {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).year()
        f.calendar = calendar
        // The server's words as given, but never a dollar figure: cost is opt-in on every card.
        let body = hasMoney(m.body) ? "" : m.body
        // The month rides in the top label so the sentence ends at its own full stop.
        return MoneyShareCard(eyebrow: "Milestone · \(m.achievedAt.formatted(f))", title: m.title, subtitle: body, stats: [], notes: [])
    }

    private static let money = try! NSRegularExpression(pattern: #"[$€£]\s?\d|\d\s?(USD|EUR|GBP|CAD)\b"#, options: [.caseInsensitive])

    static func hasMoney(_ s: String) -> Bool {
        money.firstMatch(in: s, range: NSRange(s.startIndex..., in: s)) != nil
    }
}

/// "Share a card" for one trip or milestone: the preview, an optional cost toggle and map toggle (off every time it opens,
/// never remembered) and Share through the iOS share sheet. Made on the phone; nothing is uploaded or posted.
struct ShareCardSheet: View {
    let fileName: String
    /// The cost toggle's hint, or nil when the card has no cost to add (then no toggle).
    var costHint: String?
    /// The map toggle's hint, or nil when there is no route to draw (then no toggle).
    var mapHint: String?
    let build: (_ includeCost: Bool, _ includeMap: Bool) -> MoneyShareCard
    @Environment(\.dismiss) private var dismiss
    @Environment(AppState.self) private var state

    @State private var includeCost = false
    @State private var includeMap = false
    @State private var png: Data?
    /// The map is being drawn: the card shows the line meanwhile, and Share waits so it never sends the line when the map is coming.
    @State private var drawingMap = false
    /// The map could not be drawn, so the card carries the plain line.
    @State private var mapUnavailable = false
    /// The last map drawn, kept while only the cost toggle changes.
    @State private var picture: (segments: [String], map: ShareBasemap.Picture?)?

    private var card: MoneyShareCard { build(includeCost, mapHint != nil && includeMap) }

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
                    if let mapHint {
                        Toggle(isOn: $includeMap) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Include map").themeText(.value, color: Theme.textPrimary)
                                Text(mapHint).themeText(.subline).fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .tint(Theme.accentButton)
                    }
                    if mapUnavailable, includeMap {
                        Text("Map unavailable. The card shows the route as a line.").themeText(.subline, color: Theme.textTertiary)
                    }
                    Text("Made on your device. Nothing is uploaded or posted.").themeText(.subline, color: Theme.textTertiary)
                    if let png, !drawingMap {
                        ShareLink(item: MoneySharePNG(data: png, name: fileName),
                                  preview: SharePreview(card.title, image: Image(uiImage: UIImage(data: png) ?? UIImage()))) {
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
            .task(id: card) { await paint() }
        }
    }

    /// The card as a PNG. A card with a route paints the line at once, then the basemap under it when the tiles are in.
    private func paint() async {
        let segments = card.routeSegments
        guard !segments.isEmpty else {
            mapUnavailable = false
            drawingMap = false
            png = MoneyShareCardCanvas.png(card)
            return
        }
        if picture?.segments != segments {
            png = MoneyShareCardCanvas.png(card)
            drawingMap = true
            let map = await ShareBasemap.render(segments: segments, server: state.mapServer, token: state.mapToken)
            if Task.isCancelled { return }
            picture = (segments, map)
            drawingMap = false
        }
        let map = picture?.map
        mapUnavailable = map == nil
        png = MoneyShareCardCanvas.png(card, basemap: map)
    }
}
