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
        for c in charges {
            charged.insert(calendar.startOfDay(for: c.startedAt))
            charged.insert(calendar.startOfDay(for: c.endedAt))
        }
        return byDay.keys.sorted().compactMap { day in
            guard let pts = byDay[day]?.sorted(by: { $0.at < $1.at }), let last = pts.last,
                  let low = pts.map(\.pct).min(), let high = pts.map(\.pct).max() else { return nil }
            return Day(day: day, low: low, high: high, end: last.pct, charged: charged.contains(day))
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

/// The battery level by day, calm: a soft band from each day's lowest to highest level (smoothed between days), one smooth
/// line over it for where the day ended, only the month's highest and lowest points named, one faint line for the charge
/// limit named once, a tiny tick on the axis for each day it charged and a barely visible tint under 20%. A road trip reads
/// as a deep, wide dip in the band. No legend: one short caption says what the marks are.
struct SocDailyChart: View {
    let days: [SocDaily.Day]
    /// The limit to draw (nil for none): today's on the current month, else the last known in that month.
    let limit: Double?
    let month: PeriodMonth
    var now: Date = .now
    var calendar: Calendar = .current

    @ScaledMetric(relativeTo: .body) private var chartHeight: CGFloat = 200

    private var range: (since: Date, until: Date) { month.range(calendar: calendar) }
    private var extremes: (high: SocDaily.Day, low: SocDaily.Day)? { SocDaily.extremes(days) }

    /// The middle of a day, where its marks sit.
    private func center(_ day: Date) -> Date { day.addingTimeInterval(43_200) }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            chart
            Text("Band: each day's range · line: end of day · ticks: charged")
                .themeText(.caption, color: Theme.textTertiary)
                .accessibilityHidden(true)
        }
    }

    private var chart: some View {
        let r = range
        return Chart {
            lowZone(r)
            limitMarks(r)
            bandMarks
            lineMarks
            tickMarks
            extremeMarks(r)
        }
        .chartXScale(domain: r.since...r.until)
        .chartYScale(domain: 0...104)
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

    /// Barely there: under 20% the battery is low, and the eye finds the dips without a shout.
    @ChartContentBuilder
    private func lowZone(_ r: (since: Date, until: Date)) -> some ChartContent {
        RectangleMark(xStart: .value("From", r.since), xEnd: .value("To", r.until), yStart: .value("Low", 0), yEnd: .value("Zone", SocDaily.lowZone))
            .foregroundStyle(Theme.critical.opacity(0.045))
    }

    /// One faint line at the limit, named once at its right end.
    @ChartContentBuilder
    private func limitMarks(_ r: (since: Date, until: Date)) -> some ChartContent {
        if let limit {
            RuleMark(xStart: .value("From", r.since), xEnd: .value("To", r.until), y: .value("Limit", limit))
                .lineStyle(StrokeStyle(lineWidth: 1))
                .foregroundStyle(Theme.chartSocLimit.opacity(0.32))
            PointMark(x: .value("To", r.until), y: .value("Limit", limit))
                .symbolSize(0)
                .annotation(position: .top, alignment: .trailing, spacing: 2) {
                    Text("Limit \(NumberText.fixed(limit, 0))%").themeText(.caption, color: Theme.chartSocLimit)
                }
        }
    }

    /// The band: low to high for each day, smoothed so it never overshoots the real lows and highs.
    @ChartContentBuilder
    private var bandMarks: some ChartContent {
        ForEach(days) { d in
            AreaMark(x: .value("Day", center(d.day)), yStart: .value("Low", d.low), yEnd: .value("High", max(d.high, d.low + 0.6)), series: .value("Series", "Band"))
                .interpolationMethod(.monotone)
                .foregroundStyle(Theme.chartSocCharge.opacity(0.22))
        }
    }

    /// Where each day ended, as one smooth line.
    @ChartContentBuilder
    private var lineMarks: some ChartContent {
        ForEach(days) { d in
            LineMark(x: .value("Day", center(d.day)), y: .value("End", d.end), series: .value("Series", "End"))
                .interpolationMethod(.monotone)
                .foregroundStyle(Theme.chartSocCharge)
                .lineStyle(StrokeStyle(lineWidth: 1.8, lineCap: .round, lineJoin: .round))
        }
    }

    /// A tiny tick on the axis for each day the truck charged.
    @ChartContentBuilder
    private var tickMarks: some ChartContent {
        ForEach(days.filter(\.charged)) { d in
            RuleMark(x: .value("Day", center(d.day)), yStart: .value("Floor", 0), yEnd: .value("Tick", 3.2))
                .lineStyle(StrokeStyle(lineWidth: 1.6, lineCap: .round))
                .foregroundStyle(Theme.chartSocCharge)
        }
    }

    /// The month's highest and lowest points, named inside the plot ("97% · Oct 9"), on the side with room.
    @ChartContentBuilder
    private func extremeMarks(_ r: (since: Date, until: Date)) -> some ChartContent {
        if let e = extremes, e.high.high != e.low.low {
            let highRight = center(e.high.day).timeIntervalSince(r.since) < 0.6 * r.until.timeIntervalSince(r.since)
            let lowRight = center(e.low.day).timeIntervalSince(r.since) < 0.6 * r.until.timeIntervalSince(r.since)
            PointMark(x: .value("Day", center(e.high.day)), y: .value("High", e.high.high))
                .symbolSize(40).foregroundStyle(Theme.textPrimary)
                .annotation(position: highRight ? .trailing : .leading, spacing: 4) { extremeLabel("\(NumberText.fixed(e.high.high, 0))% · \(SocDaily.dayLabel(e.high.day, calendar: calendar))") }
            PointMark(x: .value("Day", center(e.low.day)), y: .value("Low", e.low.low))
                .symbolSize(40).foregroundStyle(Theme.textPrimary)
                // A low near the floor is lifted off the charging ticks.
                .annotation(position: e.low.low < 12 ? (lowRight ? .topTrailing : .topLeading) : (lowRight ? .trailing : .leading), spacing: 4) { extremeLabel("\(NumberText.fixed(e.low.low, 0))% · \(SocDaily.dayLabel(e.low.day, calendar: calendar))") }
        }
    }

    private func extremeLabel(_ text: String) -> some View {
        Text(text).themeText(.caption, color: Theme.textPrimary).fontWeight(.semibold)
    }
}

private struct AxisText: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary) }
}
