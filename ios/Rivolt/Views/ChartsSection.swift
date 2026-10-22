import Charts
import SwiftUI

/// The Home charts: miles per day, the charge level over time and idle-awake time per day, for the last week or month.
/// Read-only history from the account's own drives and charges; no locations.
struct ChartsSection: View {
    let model: ChartsModel
    @Binding var window: ChartWindow
    var now: Date = .now
    var calendar: Calendar = .current

    private var useKm: Bool { Locale.current.measurementSystem == .metric }
    private var distanceUnit: String { useKm ? "km" : "mi" }
    @ScaledMetric(relativeTo: .body) private var milesHeight: CGFloat = 160
    @ScaledMetric(relativeTo: .body) private var idleHeight: CGFloat = 140

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Picker("Period", selection: $window) {
                Text("7 days").tag(ChartWindow.week)
                Text("30 days").tag(ChartWindow.month)
            }
            .pickerStyle(.segmented)
            milesCard
            levelCard
            idleCard
        }
    }

    // MARK: Miles per day

    private var miles: [DayValue] {
        guard let points = model.points else { return [] }
        return ChartMath.milesPerDay(points.drives, days: window.days, now: now, calendar: calendar)
            .map { DayValue(day: $0.day, value: convert($0.value)) }
    }

    private func convert(_ mi: Double) -> Double {
        useKm ? Measurement(value: mi, unit: UnitLength.miles).converted(to: .kilometers).value : mi
    }

    private var milesSpoken: (label: String, value: String) {
        ChartMath.spokenDaily(title: "\(useKm ? "Kilometres" : "Miles") per day", days: window.days, bars: miles, calendar: calendar) {
            "\(Int($0.rounded())) \(useKm ? "kilometres" : "miles")"
        }
    }

    private var milesCard: some View {
        ChartCard(title: "\(useKm ? "Kilometres" : "Miles") per day") {
            if model.points == nil {
                ChartEmpty(text: model.isLoading ? "Loading…" : "Couldn't load this chart.")
            } else if miles.allSatisfy({ $0.value == 0 }) {
                ChartEmpty(text: "No driving in this period.")
            } else {
                Chart(miles) { d in
                    BarMark(x: .value("Day", d.day, unit: .day), y: .value(distanceUnit, d.value))
                        .foregroundStyle(Color.accentColor)
                }
                .chartYAxisLabel(distanceUnit)
                .frame(height: milesHeight)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(milesSpoken.label)
                .accessibilityValue(milesSpoken.value)
            }
        }
    }

    // MARK: Charge level

    private var trend: [SocPoint] {
        guard let points = model.points else { return [] }
        return ChartMath.socTrend(drives: points.drives, charges: points.charges)
    }

    private var levelCard: some View {
        ChartCard(title: "Charge level") {
            if model.points == nil {
                ChartEmpty(text: model.isLoading ? "Loading…" : "Couldn't load this chart.")
            } else if trend.count < 2 {
                ChartEmpty(text: "Not enough drives or charges in this period yet.")
            } else {
                Chart(trend) { p in
                    LineMark(x: .value("Time", p.at), y: .value("Charge", p.pct))
                        .interpolationMethod(.monotone)
                        .foregroundStyle(Color.accentColor)
                }
                .chartXAxis {
                    // Date only: the default "Oct 19 at 2 PM" labels get cut off on a phone.
                    AxisMarks(values: .stride(by: .day, count: window.days <= 7 ? 2 : 7)) { _ in
                        AxisGridLine()
                        AxisValueLabel(format: .dateTime.month(.abbreviated).day())
                    }
                }
                // The whole period, so the line sits where its days are and the ticks span the same days as the bars.
                .chartXScale(domain: windowRange.since...windowRange.until)
                .chartYScale(domain: 0...100)
                .chartYAxis {
                    AxisMarks(values: [0, 25, 50, 75, 100]) { v in
                        AxisGridLine()
                        AxisValueLabel { if let n = v.as(Int.self) { Text("\(n)%") } }
                    }
                }
                .frame(height: milesHeight)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(levelSpoken.label)
                .accessibilityValue(levelSpoken.value)
            }
        }
    }

    private var windowRange: (since: Date, until: Date) { window.range(now: now, calendar: calendar) }

    private var levelSpoken: (label: String, value: String) {
        ChartMath.spokenLevel(days: window.days, trend: trend)
    }

    // MARK: Idle awake

    private var idle: [DayValue] {
        ChartMath.idleMinutes(model.sleep ?? [], calendar: calendar)
    }

    private var idleCard: some View {
        ChartCard(title: "Idle-awake time per day") {
            if idle.isEmpty {
                ChartEmpty(text: model.sleep == nil && !model.isLoading
                           ? "Couldn't load this chart."
                           : "Collecting data. This fills in from when power-state recording started.")
            } else {
                Chart(idle) { d in
                    BarMark(x: .value("Day", d.day, unit: .day), y: .value("Minutes", d.value))
                        .foregroundStyle(.indigo)
                }
                .chartYAxisLabel("min")
                .frame(height: idleHeight)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(idleSpoken.label)
                .accessibilityValue(idleSpoken.value)
                Text("Time per day the truck was awake while parked and not charging. Driving and charging are left out.")
                    .font(.caption).foregroundStyle(NowInk.secondary)
            }
        }
    }

    private var idleSpoken: (label: String, value: String) {
        ChartMath.spokenDaily(title: "Idle-awake time per day", days: window.days, bars: idle, calendar: calendar) {
            ChartMath.spokenMinutes($0)
        }
    }
}

private struct ChartCard<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(.subheadline.weight(.semibold)).accessibilityAddTraits(.isHeader)
            content
        }
        .themeCard(padding: 20)
    }
}

private struct ChartEmpty: View {
    let text: String
    var body: some View {
        Text(text).font(.footnote).foregroundStyle(NowInk.secondary)
            .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
    }
}
