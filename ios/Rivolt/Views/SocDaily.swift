import Charts
import SwiftUI

/// The battery level by day: for each day with readings, how low and how high the level went and where it ended,
/// worked out on the phone from the same drives and charges as the line chart. A month-scale line can't show a day
/// with four cycles; one bar per day can.
enum SocDaily {
    struct Day: Equatable, Identifiable {
        let day: Date
        let low: Double
        let high: Double
        /// The last reading of the day.
        let end: Double
        /// A charge started or ended on this day.
        let charged: Bool
        /// One of those was a DC fast charge.
        let fast: Bool
        var id: Date { day }
    }

    /// Below this the battery is in the warning zone (the web's critical level is 10%; 20% is where a road trip gets
    /// uncomfortable).
    static let lowZone = 20.0

    /// One entry per local day that has a reading, oldest first. A reading of 0 or less is "unknown", as in the line.
    static func days(drives: [DrivePoint], charges: [ChargePoint], calendar: Calendar) -> [Day] {
        let trend = ChartMath.socTrend(drives: drives, charges: charges)
        var byDay: [Date: [SocPoint]] = [:]
        for p in trend { byDay[calendar.startOfDay(for: p.at), default: []].append(p) }
        var charged: Set<Date> = []
        var fast: Set<Date> = []
        for c in charges {
            for d in [calendar.startOfDay(for: c.startedAt), calendar.startOfDay(for: c.endedAt)] {
                charged.insert(d)
                if c.kind == "dcfc" { fast.insert(d) }
            }
        }
        return byDay.keys.sorted().compactMap { day in
            guard let pts = byDay[day]?.sorted(by: { $0.at < $1.at }), let last = pts.last,
                  let low = pts.map(\.pct).min(), let high = pts.map(\.pct).max() else { return nil }
            return Day(day: day, low: low, high: high, end: last.pct, charged: charged.contains(day), fast: fast.contains(day))
        }
    }

    /// The day with the highest reading and the one with the lowest; nil with no days.
    static func extremes(_ days: [Day]) -> (high: Day, low: Day)? {
        guard let high = days.max(by: { $0.high < $1.high }), let low = days.min(by: { $0.low < $1.low }) else { return nil }
        return (high, low)
    }

    /// "Oct 12".
    static func dayLabel(_ day: Date, calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).day()
        f.calendar = calendar
        return day.formatted(f)
    }

    /// The chart as sentences for VoiceOver.
    static func spoken(_ days: [Day], calendar: Calendar) -> String {
        guard let e = extremes(days) else { return "No readings this month." }
        func pc(_ v: Double) -> String { "\(NumberText.fixed(v, 0)) percent" }
        let charged = days.filter(\.charged).count
        var s = "\(days.count) days with readings. Lowest \(pc(e.low.low)) on \(dayLabel(e.low.day, calendar: calendar)); highest \(pc(e.high.high)) on \(dayLabel(e.high.day, calendar: calendar))."
        if charged > 0 { s += " Charged on \(charged) \(charged == 1 ? "day" : "days")." }
        return s
    }
}

/// One rounded bar per day from its lowest to its highest level (green on a day the truck charged, a bolt on a fast
/// charge, neutral otherwise), a dot at the end of the day joined by a thin line, a faint tint under 20%, and the charge
/// limit as a faint step line behind. The high and the low of the month are named under the chart.
struct SocDailyChart: View {
    let days: [SocDaily.Day]
    let steps: [ChartMath.LimitStep]
    let month: PeriodMonth
    var now: Date = .now
    var calendar: Calendar = .current

    @ScaledMetric(relativeTo: .body) private var chartHeight: CGFloat = 190
    @Environment(\.dynamicTypeSize) private var typeSize

    private var range: (since: Date, until: Date) { month.range(calendar: calendar) }
    private var extremes: (high: SocDaily.Day, low: SocDaily.Day)? { SocDaily.extremes(days) }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            chart
            captions
            legend
        }
    }

    private var chart: some View {
        let r = range
        return Chart {
            lowZone(r)
            limitMarks(r)
            barMarks
            trendMarks
            extremeMarks
            boltMarks
        }
        .chartXScale(domain: r.since...r.until)
        // 0 to 100 with a little room above for a bolt on a bar that reaches the top.
        .chartYScale(domain: 0...108)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .chartYAxis {
            AxisMarks(position: .leading, values: [0, 50, 100]) { value in
                AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                AxisValueLabel {
                    if let v = value.as(Int.self) { AxisText("\(v)%") }
                }
            }
        }
        // The same day labels as Miles per day: the 1st, 11th, 21st and the last day.
        .chartXAxis {
            AxisMarks(values: month.axisDays(calendar: calendar)) { value in
                AxisValueLabel(anchor: .top, collisionResolution: .disabled) {
                    if let d = value.as(Date.self) { AxisText(ChartsSection.dayLabel(d, calendar: calendar)) }
                }
            }
        }
        .frame(height: chartHeight)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Battery level by day, \(month.spokenTitle(calendar: calendar))")
        .accessibilityValue(SocDaily.spoken(days, calendar: calendar))
    }

    // MARK: Marks

    @ChartContentBuilder
    private func lowZone(_ r: (since: Date, until: Date)) -> some ChartContent {
        RectangleMark(xStart: .value("From", r.since), xEnd: .value("To", r.until), yStart: .value("Low", 0), yEnd: .value("Zone", SocDaily.lowZone))
            .foregroundStyle(Theme.critical.opacity(0.08))
    }

    /// The limit behind the bars: a faint thin step line, named once at its right end on the current month.
    @ChartContentBuilder
    private func limitMarks(_ r: (since: Date, until: Date)) -> some ChartContent {
        let points = steps.flatMap { [(at: $0.start, pct: $0.pct), (at: $0.end, pct: $0.pct)] }
        ForEach(Array(points.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Limit", p.pct), series: .value("Series", "Limit"))
                .foregroundStyle(Theme.chartSocLimit.opacity(0.45))
                .lineStyle(StrokeStyle(lineWidth: 1))
                .interpolationMethod(.linear)
        }
        if let last = steps.last, now >= r.since, now <= r.until, now.timeIntervalSince(last.end) < 60 {
            let above = r.until.timeIntervalSince(now) < 4 * 86_400
            PointMark(x: .value("Time", last.end), y: .value("Limit", last.pct))
                .symbolSize(0)
                .annotation(position: above ? .top : .trailing, alignment: above ? .trailing : .center, spacing: 4) {
                    Text("Limit \(NumberText.fixed(last.pct, 0))%").themeText(.caption, color: Theme.chartSocLimit)
                }
        }
    }

    @ChartContentBuilder
    private var barMarks: some ChartContent {
        ForEach(days) { d in
            RectangleMark(x: .value("Day", d.day, unit: .day), yStart: .value("Low", d.low), yEnd: .value("High", max(d.high, d.low + 1.5)), width: .fixed(6))
                .cornerRadius(3)
                .foregroundStyle(d.charged ? Theme.chartSocCharge : Theme.textTertiary.opacity(0.5))
        }
    }

    /// The end-of-day level as a dot on each day, joined by a thin line: the trend.
    @ChartContentBuilder
    private var trendMarks: some ChartContent {
        ForEach(days) { d in
            LineMark(x: .value("Day", d.day, unit: .day), y: .value("End", d.end), series: .value("Series", "Trend"))
                .foregroundStyle(Theme.textSecondary.opacity(0.7))
                .lineStyle(StrokeStyle(lineWidth: 1, lineCap: .round, lineJoin: .round))
                .interpolationMethod(.linear)
        }
        ForEach(days) { d in
            PointMark(x: .value("Day", d.day, unit: .day), y: .value("End", d.end))
                .symbolSize(14)
                .foregroundStyle(Theme.textPrimary)
        }
    }

    /// A larger dot on the month's highest and lowest points, named under the chart.
    @ChartContentBuilder
    private var extremeMarks: some ChartContent {
        if let e = extremes {
            PointMark(x: .value("Day", e.high.day, unit: .day), y: .value("High", e.high.high))
                .symbolSize(44).foregroundStyle(Theme.textPrimary)
            PointMark(x: .value("Day", e.low.day, unit: .day), y: .value("Low", e.low.low))
                .symbolSize(44).foregroundStyle(Theme.textPrimary)
        }
    }

    @ChartContentBuilder
    private var boltMarks: some ChartContent {
        ForEach(days.filter(\.fast)) { d in
            PointMark(x: .value("Day", d.day, unit: .day), y: .value("High", d.high))
                .symbolSize(0)
                .annotation(position: .top, spacing: 1) {
                    Image(systemName: "bolt.fill").font(.system(size: 9)).foregroundStyle(Theme.fast)
                }
        }
    }

    // MARK: Captions and legend

    /// "Highest 97% · Oct 9" and "Lowest 3% · Oct 12", with the dot that marks each on the chart.
    @ViewBuilder
    private var captions: some View {
        if let e = extremes, e.high.high != e.low.low {
            ThemeFlow(spacing: 16, rowSpacing: 4) {
                caption("Highest \(NumberText.fixed(e.high.high, 0))% · \(SocDaily.dayLabel(e.high.day, calendar: calendar))")
                caption("Lowest \(NumberText.fixed(e.low.low, 0))% · \(SocDaily.dayLabel(e.low.day, calendar: calendar))")
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func caption(_ text: String) -> some View {
        HStack(spacing: 6) {
            Circle().fill(Theme.textPrimary).frame(width: 7, height: 7).accessibilityHidden(true)
            Text(text).themeText(.value, color: Theme.textPrimary)
        }
    }

    private var legend: some View {
        ThemeFlow(spacing: 14, rowSpacing: 6) {
            legendItem("Charged day") { Capsule().fill(Theme.chartSocCharge).frame(width: 6, height: 14) }
            legendItem("Other day") { Capsule().fill(Theme.textTertiary.opacity(0.5)).frame(width: 6, height: 14) }
            legendItem("End of day") { Circle().fill(Theme.textPrimary).frame(width: 6, height: 6) }
            legendItem("Fast charge") { Image(systemName: "bolt.fill").font(.system(size: 10)).foregroundStyle(Theme.fast) }
            if !steps.isEmpty { legendItem("Charge limit") { Rectangle().fill(Theme.chartSocLimit.opacity(0.6)).frame(width: 14, height: 1) } }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func legendItem<S: View>(_ label: String, @ViewBuilder swatch: () -> S) -> some View {
        HStack(spacing: 5) {
            swatch().accessibilityHidden(true)
            Text(label).themeText(.caption, color: Theme.textTertiary)
        }
    }
}

private struct AxisText: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary) }
}
