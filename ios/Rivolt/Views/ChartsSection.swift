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
                ChartEmpty(text: "No driving \(month.inPhrase(calendar: calendar)).")
            } else {
                // A round top and round ticks (0 · 200 · 400 · 600), not the data's own maximum split in halves (509 and 255).
                let axis = ChartMath.niceAxis(max: miles.map(\.value).max() ?? 1)
                let top = axis.top
                Chart(miles) { d in
                    BarMark(x: .value("Day", d.day, unit: .day), y: .value(distanceUnit, d.value))
                        .foregroundStyle(LinearGradient(colors: [Theme.chartMilesBar, Theme.chartMilesBar.opacity(0.35)], startPoint: .top, endPoint: .bottom))
                        .cornerRadius(2)
                }
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .chartXScale(domain: month.range(calendar: calendar).since...month.range(calendar: calendar).until)
                .chartYScale(domain: 0...top)
                .chartYAxis {
                    AxisMarks(position: .leading, values: axis.ticks) { value in
                        AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                        AxisValueLabel {
                            // No label on the baseline: its "0" would meet the first day's label in the corner.
                            if let v = value.as(Double.self), v > 0 { ChartAxisText(NumberText.fixed(v, 0)) }
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

    /// The battery chart: a soft band of each day's low to high with the end-of-day line over it (SocDailyChart). The limit
    /// is drawn only from its last change (today's on the current month, else the last known in the month shown).
    private var levelCard: some View {
        ThemeSection(title: "Battery (SoC) trend") {
            let days = SocDaily.days(drives: model.points?.drives ?? [], charges: model.points?.charges ?? [], calendar: calendar)
            if model.points == nil {
                ChartEmpty(text: model.isLoading ? "Loading…" : "Couldn't load this chart.")
            } else if trend.count < 2 {
                ChartEmpty(text: "Not enough drives or charges \(month.inPhrase(calendar: calendar)) yet.")
            } else {
                SocDailyChart(days: days, limit: limitSegment, month: month, calendar: calendar)
            }
        }
    }

    private var limitSegment: ChartMath.LimitStep? {
        let r = month.range(calendar: calendar)
        return ChartMath.limitSegment(model.points?.charges ?? [], from: r.since, to: r.until, now: now, live: limit)
    }

    /// "Oct 1", the day charts' day labels in the reader's language (the web's `dayName`).
    static func dayLabel(_ day: Date, calendar: Calendar) -> String { SocDaily.dayLabel(day, calendar: calendar) }
}

/// An axis label of any chart: the one chart-axis size (`type.chartAxis`, the caption scale) in the secondary text colour. It does not
/// grow with Dynamic Type past the default: the numbers beside the data are not the data, and every chart is also read aloud as a
/// sentence.
struct ChartAxisLabel: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View {
        Text(text).font(Theme.sans(ThemeTokens.typeChartAxis, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textSecondary)
            .dynamicTypeSize(...DynamicTypeSize.large)
    }
}

private typealias ChartAxisText = ChartAxisLabel

private struct ChartEmpty: View {
    let text: String
    var body: some View {
        Text(text).themeText(.subline)
            .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
    }
}
