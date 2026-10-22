import Charts
import SwiftUI

/// The Now page's charts for one month, as the web's: miles per day and the battery (SoC) trend with its charging and
/// driving bands and the charge limit. (Idle-awake time is the tile under the hero; the full per-day chart is on the
/// web's Truck page.) Read-only history from the account's own drives and charges; no locations.
struct ChartsSection: View {
    let model: ChartsModel
    let month: PeriodMonth
    /// The truck's charge limit, drawn as the dashed line on the battery chart.
    var limit: Double?
    var now: Date = .now
    var calendar: Calendar = .current

    private var useKm: Bool { NowFormat.usesMetric }
    private var distanceUnit: String { useKm ? "km" : "mi" }
    private var period: String { month.spokenTitle(calendar: calendar) }
    @ScaledMetric(relativeTo: .body) private var chartHeight: CGFloat = 160

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            milesCard
            levelCard
        }
    }

    // MARK: Miles per day

    private var days: [Date] { month.days(now: now, calendar: calendar) }

    private var miles: [DayValue] {
        guard let points = model.points else { return [] }
        return ChartMath.milesPerDay(points.drives, over: days, calendar: calendar)
            .map { DayValue(day: $0.day, value: useKm ? $0.value * 1.609344 : $0.value) }
    }

    private var milesSpoken: (label: String, value: String) {
        ChartMath.spokenDaily(title: "\(useKm ? "Kilometres" : "Miles") per day", period: period, bars: miles, calendar: calendar) {
            "\(Int($0.rounded())) \(useKm ? "kilometres" : "miles")"
        }
    }

    private var milesCard: some View {
        ThemeSection(title: "\(useKm ? "Kilometres" : "Miles") per day · \(month.title(calendar: calendar))") {
            if model.points == nil {
                ChartEmpty(text: model.isLoading ? "Loading…" : "Couldn't load this chart.")
            } else if miles.allSatisfy({ $0.value == 0 }) {
                ChartEmpty(text: "No driving in \(month.title(calendar: calendar)).")
            } else {
                let top = max(1, (miles.map(\.value).max() ?? 1).rounded(.up))
                Chart(miles) { d in
                    BarMark(x: .value("Day", d.day, unit: .day), y: .value(distanceUnit, d.value))
                        .foregroundStyle(LinearGradient(colors: [Theme.chartMilesBar, Theme.chartMilesBar.opacity(0.35)], startPoint: .top, endPoint: .bottom))
                        .cornerRadius(2)
                }
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .chartXScale(domain: month.range(calendar: calendar).since...month.range(calendar: calendar).until)
                .chartYScale(domain: 0...top)
                .chartYAxis {
                    AxisMarks(position: .leading, values: [0, top / 2, top]) { value in
                        AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                        AxisValueLabel {
                            if let v = value.as(Double.self) { ChartAxisText(NumberText.fixed(v, 0)) }
                        }
                    }
                }
                .chartXAxis {
                    AxisMarks(values: month.axisDays(calendar: calendar)) { value in
                        AxisValueLabel(anchor: .top, collisionResolution: .disabled) {
                            if let d = value.as(Date.self) { ChartAxisText(Self.dayLabel(d, calendar: calendar)) }
                        }
                    }
                }
                .frame(height: chartHeight)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(milesSpoken.label)
                .accessibilityValue(milesSpoken.value)
            }
        }
    }

    // MARK: Battery trend

    private var trend: [SocPoint] {
        guard let points = model.points else { return [] }
        return ChartMath.socTrend(drives: points.drives, charges: points.charges)
    }

    private var levelSpoken: (label: String, value: String) {
        ChartMath.spokenLevel(period: period, trend: trend)
    }

    private var levelCard: some View {
        ThemeSection(title: "Battery (SoC) trend") {
            if model.points == nil {
                ChartEmpty(text: model.isLoading ? "Loading…" : "Couldn't load this chart.")
            } else if trend.count < 2 {
                ChartEmpty(text: "Not enough drives or charges in \(month.title(calendar: calendar)) yet.")
            } else {
                let range = month.range(calendar: calendar)
                // At least ~1% of the month wide, so a short drive is still a mark on the strip.
                let bands = ChartMath.bands(drives: model.points?.drives ?? [], charges: model.points?.charges ?? [], from: range.since, to: range.until,
                                            minSpan: range.until.timeIntervalSince(range.since) / 100)
                let extremes = ChartMath.highLow(trend)
                Chart {
                    bandMarks(bands)
                    trendMarks(trend)
                    limitMark(range: range)
                    extremeMarks(extremes)
                }
                .chartXScale(domain: range.since...range.until)
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                // Below 0% is the strip; above 100% is room for the high label.
                .chartYScale(domain: -12...108)
                .chartYAxis {
                    AxisMarks(position: .leading, values: [0, 50, 100]) { value in
                        AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                        AxisValueLabel {
                            if let v = value.as(Int.self) { ChartAxisText("\(v)%") }
                        }
                    }
                }
                .chartXAxis {
                    // The same day labels as Miles per day: 10-01, 10-11, 10-21, 10-31.
                    AxisMarks(values: month.axisDays(calendar: calendar)) { value in
                        AxisValueLabel(anchor: .top, collisionResolution: .disabled) {
                            if let d = value.as(Date.self) { ChartAxisText(Self.dayLabel(d, calendar: calendar)) }
                        }
                    }
                }
                .frame(height: chartHeight + 20)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(levelSpoken.label)
                .accessibilityValue(levelSpoken.value)
                ChartLegend(items: legendItems(range: range))
            }
        }
    }

    private func legendItems(range: (since: Date, until: Date)) -> [ChartLegend.Item] {
        var items: [ChartLegend.Item] = [
            .init(label: "Battery %", style: .line(Theme.textPrimary)),
            .init(label: "Charging", style: .swatch(Theme.chartSocCharge)),
            .init(label: "Driving", style: .swatch(Theme.chartSocDrive)),
        ]
        if limitShown(range: range) { items.append(.init(label: "Charge limit now", style: .dashed(Theme.chartSocLimit))) }
        items.append(.init(label: "High and low", style: .dot(Theme.textPrimary)))
        return items
    }

    private func bandColor(_ kind: ChartMath.Band.Kind) -> Color {
        kind == .charging ? Theme.chartSocCharge : Theme.chartSocDrive
    }

    /// One thin strip under the 0% line: sky for driving, the accent for charging. No wash over the plot.
    @ChartContentBuilder
    private func bandMarks(_ bands: [ChartMath.Band]) -> some ChartContent {
        ForEach(bands) { b in
            RectangleMark(xStart: .value("From", b.start), xEnd: .value("To", b.end), yStart: .value("Low", -10), yEnd: .value("Top", -4))
                .foregroundStyle(bandColor(b.kind))
        }
    }

    @ChartContentBuilder
    private func trendMarks(_ trend: [SocPoint]) -> some ChartContent {
        ForEach(trend) { p in
            AreaMark(x: .value("Time", p.at), yStart: .value("Floor", 0), yEnd: .value("Charge", p.pct))
                .interpolationMethod(.linear)
                .foregroundStyle(LinearGradient(colors: [Theme.textPrimary.opacity(0.12), .clear], startPoint: .top, endPoint: .bottom))
        }
        ForEach(trend) { p in
            LineMark(x: .value("Time", p.at), y: .value("Charge", p.pct))
                .interpolationMethod(.linear)
                .foregroundStyle(Theme.textPrimary)
                .lineStyle(StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
        }
    }

    /// The limit is what the truck is set to now, not what it was on earlier days (it was changed during the month), so
    /// it is drawn only on the current month and only over the last day, ending at now, labelled "now".
    private func limitShown(range: (since: Date, until: Date)) -> Bool {
        ChartMath.limitVisible(limit, now: now, since: range.since, until: range.until)
    }

    @ChartContentBuilder
    private func limitMark(range: (since: Date, until: Date)) -> some ChartContent {
        if limitShown(range: range), let limit {
            RuleMark(xStart: .value("From", max(range.since, now.addingTimeInterval(-86_400))), xEnd: .value("To", now), y: .value("Limit", limit))
                .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [4, 3]))
                .foregroundStyle(Theme.chartSocLimit)
                .annotation(position: .top, alignment: .trailing, spacing: 2) {
                    Text("Limit now \(NumberText.fixed(limit, 0))%").themeText(.caption, color: Theme.chartSocLimit)
                }
        }
    }

    @ChartContentBuilder
    private func extremeMarks(_ extremes: (high: SocPoint, low: SocPoint)?) -> some ChartContent {
        if let e = extremes {
            PointMark(x: .value("Time", e.high.at), y: .value("Charge", e.high.pct))
                .foregroundStyle(Theme.textPrimary).symbolSize(40)
                .annotation(position: .top, spacing: 2) { Text("\(NumberText.fixed(e.high.pct, 0))%").themeText(.caption, color: Theme.textPrimary) }
            PointMark(x: .value("Time", e.low.at), y: .value("Charge", e.low.pct))
                .foregroundStyle(Theme.textPrimary).symbolSize(40)
                .annotation(position: .trailing, spacing: 4) { Text("\(NumberText.fixed(e.low.pct, 0))%").themeText(.caption, color: Theme.textPrimary) }
        }
    }

    /// "10-01", the web's day labels.
    static func dayLabel(_ day: Date, calendar: Calendar) -> String {
        let f = DateFormatter()
        f.calendar = calendar
        f.timeZone = calendar.timeZone
        f.dateFormat = "MM-dd"
        return f.string(from: day)
    }
}

private struct ChartAxisText: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary) }
}

private struct ChartEmpty: View {
    let text: String
    var body: some View {
        Text(text).themeText(.subline)
            .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
    }
}

/// The key under the battery chart: what each line, band and dot means.
private struct ChartLegend: View {
    struct Item: Identifiable {
        enum Style { case line(Color), swatch(Color), dashed(Color), dot(Color) }
        let label: String
        let style: Style
        var id: String { label }
    }
    let items: [Item]

    var body: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 104), alignment: .leading)], alignment: .leading, spacing: 6) {
            ForEach(items) { item in
                HStack(spacing: 6) {
                    mark(item.style)
                    Text(item.label).themeText(.subline, color: Theme.textBody)
                }
            }
        }
        .accessibilityHidden(true)
    }

    @ViewBuilder
    private func mark(_ style: Item.Style) -> some View {
        switch style {
        case .line(let c): Capsule().fill(c).frame(width: 18, height: 2)
        case .swatch(let c): RoundedRectangle(cornerRadius: 2).fill(c).frame(width: 14, height: 6)
        case .dashed(let c): Path { p in p.move(to: .zero); p.addLine(to: CGPoint(x: 18, y: 0)) }
            .stroke(c, style: StrokeStyle(lineWidth: 1.5, dash: [4, 3])).frame(width: 18, height: 2)
        case .dot(let c): Circle().fill(c).frame(width: 8, height: 8)
        }
    }
}
