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
                .frame(height: 160)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(useKm ? "Kilometres" : "Miles") per day, last \(window.days) days")
                .accessibilityValue("\(Int(miles.reduce(0) { $0 + $1.value }.rounded())) \(distanceUnit) in total")
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
                .chartYScale(domain: 0...100)
                .chartYAxis {
                    AxisMarks(values: [0, 25, 50, 75, 100]) { v in
                        AxisGridLine()
                        AxisValueLabel { if let n = v.as(Int.self) { Text("\(n)%") } }
                    }
                }
                .frame(height: 160)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("Charge level over the last \(window.days) days")
                .accessibilityValue(levelSummary)
            }
        }
    }

    private var levelSummary: String {
        guard let first = trend.first, let last = trend.last else { return "" }
        return "From \(Int(first.pct.rounded())) percent to \(Int(last.pct.rounded())) percent"
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
                .frame(height: 140)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("Idle-awake time per day, last \(window.days) days")
                .accessibilityValue(idleSummary)
                Text("Time per day the truck was awake while parked and not charging. Driving and charging are left out.")
                    .font(.caption).foregroundStyle(NowInk.secondary)
            }
        }
    }

    private var idleSummary: String {
        guard let avg = ChartMath.averageIdle(idle, now: now, calendar: calendar) else { return "" }
        return "Average \(NowFormat.duration(avg * 60)) per full day"
    }
}

private struct ChartCard<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(.subheadline.weight(.semibold))
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }
}

private struct ChartEmpty: View {
    let text: String
    var body: some View {
        Text(text).font(.footnote).foregroundStyle(NowInk.secondary)
            .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
    }
}
