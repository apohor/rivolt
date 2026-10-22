import SwiftUI

/// Activity → Efficiency's words and numbers, kept out of the view so they can be tested (the web's EfficiencyView and
/// EfficiencyBars).
enum EfficiencyLogic {
    struct Group: Equatable, Identifiable {
        let title: String
        let buckets: [EfficiencyBucket]
        var id: String { title }
    }

    /// The breakdowns that have at least one figure, in the web's order.
    static func groups(_ e: EfficiencyInsights) -> [Group] {
        [Group(title: "By outside temperature", buckets: e.byTemperature),
         Group(title: "By speed", buckets: e.bySpeed),
         Group(title: "By drive mode", buckets: e.byDriveMode)]
            .filter { $0.buckets.contains { $0.efficiency != nil } }
    }

    /// The figures can be shown: an overall figure on enough distance and something to break down.
    static func ready(_ e: EfficiencyInsights) -> Bool {
        e.total.efficiency != nil && !e.total.notEnoughData && !groups(e).isEmpty
    }

    /// "7 drives · 238 mi · 88 kWh · 1 left out (no energy recorded)".
    static func summary(_ e: EfficiencyInsights) -> String {
        let n = e.total.drives
        var s = "\(n) \(n == 1 ? "drive" : "drives") · \(TruckLogic.whole(e.total.distance)) \(e.distanceUnit) · \(NumberText.fixed(e.total.energyKwh, 0)) kWh"
        let left = (e.excluded?.noEnergy ?? 0) + (e.excluded?.duplicates ?? 0)
        if left > 0 { s += " · \(left) left out (no energy recorded)" }
        return s
    }

    /// The one line in place of the figures when there are too few, in the list's words.
    static func notEnough(_ e: EfficiencyInsights, month: String) -> String {
        "Not enough data yet for \(month): efficiency needs drives with recorded energy, at least \(TruckLogic.whole(e.minBucketDistance)) \(e.distanceUnit)."
    }

    /// The bar scale: a little above the best bucket or the average, never below 1 (the web's scaleMax).
    static func scaleMax(_ buckets: [EfficiencyBucket], overall: Double?) -> Double {
        max(1, overall ?? 0, buckets.compactMap(\.efficiency).max() ?? 0) * 1.15
    }

    /// The best bucket on enough distance, if any.
    static func best(_ buckets: [EfficiencyBucket]) -> String? {
        buckets.filter { $0.efficiency != nil && !$0.notEnoughData }.max { ($0.efficiency ?? 0) < ($1.efficiency ?? 0) }?.key
    }

    /// "Highway (55–70 mph)" → "Highway" over "55–70 mph"; "32–50 °F" stays whole.
    static func split(_ label: String) -> (main: String, sub: String) {
        guard let open = label.lastIndex(of: "("), label.hasSuffix(")") else { return (label, "") }
        let main = label[..<open].trimmingCharacters(in: .whitespaces)
        let sub = label[label.index(after: open)..<label.index(before: label.endIndex)]
        return main.isEmpty ? (label, "") : (main, String(sub))
    }

    /// One bar for VoiceOver: its figure in words, its drives, and whether it is thin or the best.
    static func spoken(_ b: EfficiencyBucket, unit: String, best: Bool) -> String {
        guard let v = b.efficiency else { return "\(b.label): no data" }
        var s = "\(b.label): \(NumberText.fixed(v, 2)) \(unit), \(b.drives) \(b.drives == 1 ? "drive" : "drives")"
        if b.notEnoughData { s += ", thin, a rough figure" }
        if best { s += ", best" }
        return SpokenText.words(s)
    }

    /// Where a bar's value sits (points above the plot's floor): on top of the bar, moved above the average line
    /// when it would sit on it.
    static func valueBottom(bar: Double, average: Double?, labelHeight: Double, gap: Double) -> Double {
        guard let a = average, a >= bar - gap, a <= bar + labelHeight + gap else { return bar }
        return a + gap
    }
}

/// Activity → Efficiency: the month's average efficiency over the three breakdowns (outside temperature, speed, drive
/// mode) as bar charts against the average, as the web's; rows instead of charts at accessibility text sizes.
struct EfficiencyView: View {
    let shown: EfficiencyModel.Shown
    let monthName: String

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        switch shown {
        case .loading:
            ProgressView().frame(maxWidth: .infinity).padding(.vertical, 8 * Theme.unit)
        case .unavailable:
            Text("Efficiency isn't available on this server yet.").themeText(.subline)
        case .failed:
            Text("Couldn't load efficiency. Use Refresh in the account menu to try again.").themeText(.subline, color: Theme.estimate)
        case .insights(let e):
            if EfficiencyLogic.ready(e), let avg = e.total.efficiency {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    hero(e, avg)
                    ForEach(EfficiencyLogic.groups(e)) { g in
                        ThemeSection(title: g.title) {
                            if typeSize.isAccessibilitySize {
                                EfficiencyRows(buckets: g.buckets, average: avg, unit: e.efficiencyUnit)
                            } else {
                                EfficiencyBars(buckets: g.buckets, average: avg, unit: e.efficiencyUnit)
                                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                            }
                            footnote(e)
                        }
                    }
                }
            } else {
                Text(EfficiencyLogic.notEnough(e, month: monthName)).themeText(.subline)
            }
        }
    }

    private func hero(_ e: EfficiencyInsights, _ avg: Double) -> some View {
        VStack(alignment: .leading, spacing: Theme.unit) {
            Text("Average efficiency").themeText(.label)
            HStack(alignment: .firstTextBaseline, spacing: 1.5 * Theme.unit) {
                Text(NumberText.fixed(avg, 2)).themeText(.hero)
                Text(e.efficiencyUnit).themeText(.subline)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(SpokenText.words("Average efficiency \(NumberText.fixed(avg, 2)) \(e.efficiencyUnit)"))
            Text(EfficiencyLogic.summary(e)).themeText(.subline)
                .accessibilityLabel(SpokenText.words(EfficiencyLogic.summary(e).replacingOccurrences(of: " · ", with: ", ")))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard()
    }

    private func footnote(_ e: EfficiencyInsights) -> some View {
        Text("\(e.efficiencyUnit); the number under each label is its drives. Dashed bars: under \(TruckLogic.whole(e.minBucketDistance)) \(e.distanceUnit) driven, a rough figure.")
            .themeText(.caption)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.top, 1.5 * Theme.unit)
            .accessibilityLabel(SpokenText.words("Dashed bars: under \(TruckLogic.whole(e.minBucketDistance)) \(e.distanceUnit) driven, a rough figure."))
    }
}

/// The bars: the web's EfficiencyBars. Values sit inside the plot above each bar (moved off the dashed average line),
/// the best bucket in brighter emerald with a semibold value, thin buckets lighter with a dashed outline, empty ones a
/// dashed "—" slot. Two-line labels and the drive count under each.
struct EfficiencyBars: View {
    let buckets: [EfficiencyBucket]
    let average: Double
    let unit: String

    /// The web's 150 px plot, its bars at most 128 px (room for a value above the tallest).
    static let plotHeight = 37.5 * Theme.unit
    static let barArea = 32 * Theme.unit
    private static let labelHeight = 4 * Theme.unit

    var body: some View {
        let max = EfficiencyLogic.scaleMax(buckets, overall: average)
        let best = EfficiencyLogic.best(buckets)
        let y = { (v: Double) in CGFloat(v / max) * Self.barArea }
        VStack(alignment: .leading, spacing: 1.5 * Theme.unit) {
            HStack(spacing: 1.5 * Theme.unit) {
                DashedRule().stroke(Theme.chartSocLimit, style: StrokeStyle(lineWidth: 1, dash: [3, 2])).frame(width: 4 * Theme.unit, height: 1)
                Text("Average \(NumberText.fixed(average, 2)) \(unit)").themeText(.caption)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(SpokenText.words("Average \(NumberText.fixed(average, 2)) \(unit)"))
            ZStack(alignment: .bottomLeading) {
                Rectangle().fill(Theme.border).frame(height: 1)
                HStack(alignment: .bottom, spacing: 1.5 * Theme.unit) {
                    ForEach(buckets) { b in
                        bar(b, height: b.efficiency.map(y), averageY: y(average), best: b.key == best)
                            .frame(maxWidth: .infinity)
                    }
                }
                DashedRule().stroke(Theme.chartSocLimit.opacity(0.7), style: StrokeStyle(lineWidth: 1, dash: [3, 2]))
                    .frame(height: 1)
                    .offset(y: -y(average))
                    .accessibilityHidden(true)
            }
            .frame(height: Self.plotHeight, alignment: .bottom)
            HStack(alignment: .top, spacing: 1.5 * Theme.unit) {
                ForEach(buckets) { b in
                    let l = EfficiencyLogic.split(b.label)
                    VStack(spacing: 0) {
                        Text(l.main).themeText(.caption, color: Theme.textBody).lineLimit(1)
                        if !l.sub.isEmpty { Text(l.sub).themeText(.caption).lineLimit(1) }
                        Text("\(b.drives)").themeText(.caption, color: b.notEnoughData ? Theme.estimate : Theme.textTertiary)
                    }
                    .minimumScaleFactor(0.8)
                    .frame(maxWidth: .infinity)
                }
            }
            .accessibilityHidden(true)
        }
    }

    @ViewBuilder
    private func bar(_ b: EfficiencyBucket, height: CGFloat?, averageY: CGFloat, best: Bool) -> some View {
        Group {
            if let height, let v = b.efficiency {
                let h = Swift.max(2, height)
                let bottom = EfficiencyLogic.valueBottom(bar: h, average: averageY, labelHeight: Self.labelHeight, gap: 0.5 * Theme.unit)
                ZStack(alignment: .bottom) {
                    UnevenRoundedRectangle(topLeadingRadius: 1.5 * Theme.unit, topTrailingRadius: 1.5 * Theme.unit)
                        .fill(b.notEnoughData
                              ? AnyShapeStyle(Theme.routeStart.opacity(0.2))
                              : best
                                ? AnyShapeStyle(LinearGradient(colors: [Theme.mapDrive, Theme.efficiencyBarEnd], startPoint: .top, endPoint: .bottom))
                                : AnyShapeStyle(LinearGradient(colors: [Theme.routeStart.opacity(0.8), Theme.efficiencyBarEnd.opacity(0.67)], startPoint: .top, endPoint: .bottom)))
                        .overlay {
                            if b.notEnoughData {
                                UnevenRoundedRectangle(topLeadingRadius: 1.5 * Theme.unit, topTrailingRadius: 1.5 * Theme.unit)
                                    .stroke(Theme.routeStart.opacity(0.6), style: StrokeStyle(lineWidth: 1, dash: [3, 2]))
                            }
                        }
                        .frame(height: h)
                    Text(NumberText.fixed(v, 2))
                        .font(Theme.sans(ThemeTokens.typeCaption, best ? .semibold : .regular, relativeTo: .caption2))
                        .monospacedDigit()
                        .foregroundStyle(Theme.textPrimary)
                        .lineLimit(1)
                        .minimumScaleFactor(0.7)
                        .frame(height: Self.labelHeight)
                        .offset(y: -bottom)
                }
                .frame(height: Self.plotHeight, alignment: .bottom)
            } else {
                Text("—").themeText(.caption)
                    .frame(maxWidth: .infinity, minHeight: 10 * Theme.unit, alignment: .bottom)
                    .padding(.bottom, Theme.unit)
                    .overlay(UnevenRoundedRectangle(topLeadingRadius: 1.5 * Theme.unit, topTrailingRadius: 1.5 * Theme.unit)
                        .stroke(Theme.border, style: StrokeStyle(lineWidth: 1, dash: [3, 2])))
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(EfficiencyLogic.spoken(b, unit: unit, best: best))
    }
}

/// The same figures as rows, for accessibility text sizes: the name, a horizontal bar, the value and the drives; the
/// average as a row of its own.
struct EfficiencyRows: View {
    let buckets: [EfficiencyBucket]
    let average: Double
    let unit: String

    var body: some View {
        let max = EfficiencyLogic.scaleMax(buckets, overall: average)
        let best = EfficiencyLogic.best(buckets)
        VStack(alignment: .leading, spacing: 3 * Theme.unit) {
            Text("Average \(NumberText.fixed(average, 2)) \(unit)").themeText(.value, color: Theme.chartSocLimit)
                .accessibilityLabel(SpokenText.words("Average \(NumberText.fixed(average, 2)) \(unit)"))
            ForEach(buckets) { b in
                VStack(alignment: .leading, spacing: Theme.unit) {
                    Text(b.label).themeText(.value, color: Theme.textPrimary)
                    if let v = b.efficiency {
                        GeometryReader { geo in
                            Capsule()
                                .fill(b.notEnoughData ? Theme.routeStart.opacity(0.2) : (b.key == best ? Theme.mapDrive : Theme.routeStart.opacity(0.8)))
                                .overlay {
                                    if b.notEnoughData { Capsule().stroke(Theme.routeStart.opacity(0.6), style: StrokeStyle(lineWidth: 1, dash: [3, 2])) }
                                }
                                .frame(width: geo.size.width * CGFloat(v / max))
                        }
                        .frame(height: 2 * Theme.unit)
                        Text("\(NumberText.fixed(v, 2)) \(unit) · \(b.drives) \(b.drives == 1 ? "drive" : "drives")\(b.notEnoughData ? " · rough" : "")")
                            .themeText(.subline)
                            .fontWeight(b.key == best ? .semibold : .regular)
                    } else {
                        Text("— no data").themeText(.subline)
                    }
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(EfficiencyLogic.spoken(b, unit: unit, best: b.key == best))
            }
        }
    }
}

/// A horizontal line across its frame, for dashed rules.
private struct DashedRule: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: rect.minX, y: rect.midY))
        p.addLine(to: CGPoint(x: rect.maxX, y: rect.midY))
        return p
    }
}
