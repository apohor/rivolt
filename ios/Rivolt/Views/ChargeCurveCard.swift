import Charts
import SwiftUI

/// The charge detail's "Charge" card, as the web's: the battery level and the charger's power over the session on one
/// chart (a right axis in kW), chips to show or hide each line (the pack's temperature is opt-in) and a readout for the
/// moment under the finger. The samples (read by `ChargeDetailSections`) are kept only while the screen is open.
struct ChargeCurveCard: View {
    let phase: ChargeCurveModel.Phase
    let charge: ActivityCharge
    var calendar: Calendar = .current

    var body: some View {
        ThemeSection(title: "Charge") {
            switch phase {
            case .loading:
                ProgressView().frame(maxWidth: .infinity, minHeight: 140)
            case .failed:
                Text("Couldn't load this charge's curve.").themeText(.subline, color: Theme.textTertiary)
            case .ready(let series):
                if series.soc.isEmpty {
                    Text("No battery readings were recorded for this charge.").themeText(.subline, color: Theme.textTertiary)
                } else {
                    ChargeCurveView(series: series, startPct: charge.startSocPct, endPct: charge.endSocPct,
                                    peakKw: charge.maxPowerKw, calendar: calendar)
                }
            }
        }
    }
}

/// The chart, chips and readout for a loaded series (apart from the card so a test can render it).
struct ChargeCurveView: View {
    let series: ChargeCurve.Series
    let startPct: Double
    let endPct: Double
    let peakKw: Double
    var calendar: Calendar = .current
    var metric: Bool = NowFormat.usesMetric

    @State private var showBattery = true
    @State private var showPower = true
    @State private var showPack = false
    @State private var cursor: Date?
    @ScaledMetric(relativeTo: .body) private var chartHeight: CGFloat = 220

    /// The web's #a78bfa for the pack temperature (its charge page's colour; not in the design tokens).
    private static let packColor = Color(red: 167 / 255, green: 139 / 255, blue: 250 / 255)
    private var batteryColor: Color { Theme.chartSocCharge }
    private var powerColor: Color { Theme.chartSocLimit }

    private var axes: ChargeCurve.Axes { ChargeCurve.axes(series, startPct: startPct, endPct: endPct, peakKw: peakKw) }
    private var hasPower: Bool { !series.power.isEmpty }
    private var hasPack: Bool { series.packAvgC.count > 1 }
    private var tempUnit: String { metric ? "°C" : "°F" }
    private var packInUnit: [ChargeCurve.Pt] { series.packAvgC.map { .init(at: $0.at, value: ChargeCurve.temperature($0.value, metric: metric)) } }

    private var timeStyle: Date.FormatStyle {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).hour().minute()
        f.calendar = calendar
        return f
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            readout
            chips
            chart
            note
        }
        .padding(4 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: Readout and chips

    private var readout: some View {
        ThemeFlow(spacing: 12, rowSpacing: 4) {
            if let cursor {
                Text(cursor.formatted(timeStyle)).themeText(.caption, color: Theme.textSecondary)
                if showBattery, let v = ChargeCurve.nearest(series.soc, to: cursor) {
                    pill("Battery", batteryColor, "\(NumberText.fixed(v, 1))%")
                }
                if showPower, hasPower, let v = ChargeCurve.nearest(series.power, to: cursor) {
                    pill("Power", powerColor, "\(NumberText.fixed(v, 1)) kW")
                }
                if showPack, hasPack, let v = ChargeCurve.nearest(packInUnit, to: cursor) {
                    pill("Pack", Self.packColor, "\(NumberText.fixed(v, 0))\(tempUnit)")
                }
            } else {
                Text("Touch the chart to inspect a moment").themeText(.caption, color: Theme.textTertiary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func pill(_ label: String, _ color: Color, _ value: String) -> some View {
        HStack(spacing: 4) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text(label).themeText(.caption, color: Theme.textSecondary)
            Text(value).themeText(.caption, color: Theme.textPrimary)
        }
        .accessibilityElement(children: .combine)
    }

    private var chips: some View {
        ThemeFlow(spacing: 8, rowSpacing: 8) {
            CurveChip(label: "Battery", color: batteryColor, on: showBattery) { showBattery.toggle() }
            if hasPower { CurveChip(label: "Power", color: powerColor, on: showPower) { showPower.toggle() } }
            if hasPack { CurveChip(label: "Pack temp", color: Self.packColor, on: showPack) { showPack.toggle() } }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: Chart

    private var chart: some View {
        let ax = axes
        let first = series.soc.first?.at ?? .now
        let last = max(series.soc.last?.at ?? first, first.addingTimeInterval(60))
        return Chart {
            if showBattery { batteryMarks(ax) }
            if showPower, hasPower { powerMarks(ax) }
            if showPack, hasPack { packMarks() }
            cursorMark
        }
        .chartXScale(domain: first...last)
        .chartYScale(domain: 0...1)
        .chartXSelection(value: $cursor)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .chartYAxis {
            // One set of gridlines, the battery's; the kW labels stand at the same heights.
            AxisMarks(position: .leading, values: ax.ticks.map { ChargeCurve.unit(soc: $0, ax) }) { value in
                AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                AxisValueLabel {
                    if let v = value.as(Double.self) { AxisText("\(NumberText.fixed(ax.socLo + v * (ax.socHi - ax.socLo), 0))%") }
                }
            }
            if showPower, hasPower {
                AxisMarks(position: .trailing, values: ax.ticks.map { ChargeCurve.unit(soc: $0, ax) }) { value in
                    AxisValueLabel {
                        if let v = value.as(Double.self) { AxisText("\(NumberText.fixed(v * ax.powerMax, 0)) kW") }
                    }
                }
            }
        }
        // Three times: the start, the middle and the end, the first and last kept inside the plot so the last one
        // doesn't run into the kW labels.
        .chartXAxis {
            AxisMarks(values: [first, first.addingTimeInterval(last.timeIntervalSince(first) / 2), last]) { value in
                AxisValueLabel(anchor: value.index == 0 ? .topLeading : value.index == 2 ? .topTrailing : .top, collisionResolution: .disabled) {
                    if let d = value.as(Date.self) { AxisText(d.formatted(timeStyle)) }
                }
            }
        }
        .frame(height: chartHeight)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Charging curve")
        .accessibilityValue(SpokenText.words(ChargeCurve.spoken(series, peakKw: peakKw)))
    }

    @ChartContentBuilder
    private func batteryMarks(_ axes: ChargeCurve.Axes) -> some ChartContent {
        ForEach(Array(series.soc.enumerated()), id: \.offset) { _, p in
            AreaMark(x: .value("Time", p.at), yStart: .value("Floor", 0), yEnd: .value("Battery", ChargeCurve.unit(soc: p.value, axes)))
                .foregroundStyle(LinearGradient(colors: [batteryColor.opacity(0.22), .clear], startPoint: .top, endPoint: .bottom))
        }
        ForEach(Array(series.soc.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Battery", ChargeCurve.unit(soc: p.value, axes)), series: .value("Series", "Battery"))
                .foregroundStyle(batteryColor)
                .lineStyle(StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
                .interpolationMethod(.linear)
        }
    }

    @ChartContentBuilder
    private func powerMarks(_ axes: ChargeCurve.Axes) -> some ChartContent {
        ForEach(Array(series.power.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Power", ChargeCurve.unit(power: p.value, axes)), series: .value("Series", "Power"))
                .foregroundStyle(powerColor)
                .lineStyle(StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
                .interpolationMethod(.linear)
        }
    }

    /// The pack temperature, mapped into the same 0...1 space over the power's range, as the web maps it into its right
    /// axis (the readout says the real value).
    @ChartContentBuilder
    private func packMarks() -> some ChartContent {
        let pts = packInUnit
        let range = ChargeCurve.tempRange(pts)
        ForEach(Array(pts.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Pack", ChargeCurve.unit(temp: p.value, range: range)), series: .value("Series", "Pack"))
                .foregroundStyle(Self.packColor)
                .lineStyle(StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round, dash: [4, 2]))
                .interpolationMethod(.linear)
        }
    }

    @ChartContentBuilder
    private var cursorMark: some ChartContent {
        if let cursor {
            RuleMark(x: .value("Time", cursor))
                .foregroundStyle(Theme.textTertiary.opacity(0.6))
                .lineStyle(StrokeStyle(lineWidth: 1))
        }
    }

    // MARK: Note

    @ViewBuilder
    private var note: some View {
        if !hasPower {
            Text("No charger-power samples for this session.").themeText(.caption, color: Theme.textTertiary)
        } else if showPack, series.packSpreadC > 0 {
            let spread = metric ? series.packSpreadC : series.packSpreadC * 1.8
            Text("Pack cell temperatures: the widest spread between the hottest and the coolest cell was \(NumberText.fixed(spread, 1))\(tempUnit), a sign of thermal imbalance.")
                .themeText(.caption, color: Theme.textTertiary)
        }
    }
}

private struct AxisText: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary) }
}

/// A show/hide switch for one line of the chart: filled when on, outlined when off.
private struct CurveChip: View {
    let label: String
    let color: Color
    let on: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Circle().fill(color).frame(width: 8, height: 8)
                Text(label).themeText(.caption, color: on ? Theme.textPrimary : Theme.textSecondary)
            }
            .padding(.horizontal, 12)
            .frame(minHeight: 44)
            .background(on ? color.opacity(0.16) : .clear, in: Capsule())
            .overlay(Capsule().strokeBorder(on ? color : Theme.border, lineWidth: 1))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}
