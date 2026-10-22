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
    @State private var showPack: Bool
    @State private var showOutside: Bool
    @State private var cursor: Date?
    @ScaledMetric(relativeTo: .body) private var chartHeight: CGFloat = 220

    /// The two temperature overlays start hidden, as on the web; a test can start them shown.
    init(series: ChargeCurve.Series, startPct: Double, endPct: Double, peakKw: Double, calendar: Calendar = .current,
         metric: Bool = NowFormat.usesMetric, showPack: Bool = false, showOutside: Bool = false) {
        self.series = series
        self.startPct = startPct
        self.endPct = endPct
        self.peakKw = peakKw
        self.calendar = calendar
        self.metric = metric
        _showPack = State(initialValue: showPack)
        _showOutside = State(initialValue: showOutside)
    }

    /// The web's #a78bfa for the pack temperature (its charge page's colour; not in the design tokens).
    private static let packColor = Color(red: 167 / 255, green: 139 / 255, blue: 250 / 255)
    /// And its #60a5fa for the outside temperature.
    private static let outsideColor = Color(red: 96 / 255, green: 165 / 255, blue: 250 / 255)
    private var batteryColor: Color { Theme.chartSocCharge }
    private var powerColor: Color { Theme.chartSocLimit }

    private var axes: ChargeCurve.Axes { ChargeCurve.axes(series, startPct: startPct, endPct: endPct, peakKw: peakKw) }
    private var hasPower: Bool { !series.power.isEmpty }
    private var hasPack: Bool { series.packAvgC.count > 1 }
    private var hasOutside: Bool { series.outsideC.count > 1 }
    private var tempUnit: String { metric ? "°C" : "°F" }
    private var packInUnit: [ChargeCurve.Pt] { series.packAvgC.map { .init(at: $0.at, value: ChargeCurve.temperature($0.value, metric: metric)) } }
    private var outsideInUnit: [ChargeCurve.Pt] { series.outsideC.map { .init(at: $0.at, value: ChargeCurve.temperature($0.value, metric: metric)) } }

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
                if showOutside, hasOutside, let v = ChargeCurve.nearest(outsideInUnit, to: cursor) {
                    pill("Outside", Self.outsideColor, "\(NumberText.fixed(v, 0))\(tempUnit)")
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
            if hasOutside { CurveChip(label: "Outside temp", color: Self.outsideColor, on: showOutside) { showOutside.toggle() } }
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
            if showOutside, hasOutside { outsideMarks() }
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
    /// axis: a thin solid line, with its first and last values at the ends in its colour (there is no scale for it, so
    /// the ends say what it ran between; the touch readout says the value at a moment).
    @ChartContentBuilder
    private func packMarks() -> some ChartContent {
        let pts = packInUnit
        let range = ChargeCurve.tempRange(pts)
        ForEach(Array(pts.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Pack", ChargeCurve.unit(temp: p.value, range: range)), series: .value("Series", "Pack"))
                .foregroundStyle(Self.packColor)
                .lineStyle(StrokeStyle(lineWidth: 1.2, lineCap: .round, lineJoin: .round))
                .interpolationMethod(.linear)
        }
        endLabels(pts, range: range, color: Self.packColor, above: true)
    }

    /// A temperature value at a line's end, on a small halo in the card's colour so a line running behind it never
    /// strikes through the text.
    private func endLabel(_ text: String, color: Color) -> some View {
        Text(text).themeText(.caption, color: color)
            .padding(.horizontal, 3).padding(.vertical, 1)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 4, style: .continuous))
    }

    /// A temperature line's two ends named in its colour: its first value at the left of the plot, its last at the
    /// right, above the line (pack) or below it (outside) so the two overlays' labels don't sit on each other.
    @ChartContentBuilder
    private func endLabels(_ pts: [ChargeCurve.Pt], range: ClosedRange<Double>, color: Color, above: Bool) -> some ChartContent {
        if let first = pts.first, let last = pts.last {
            let firstY = ChargeCurve.unit(temp: first.value, range: range)
            let lastY = ChargeCurve.unit(temp: last.value, range: range)
            PointMark(x: .value("Time", first.at), y: .value("Temperature", firstY))
                .symbolSize(0)
                .annotation(position: Self.labelAbove(above, y: firstY) ? .top : .bottom, alignment: .leading, spacing: 2) {
                    endLabel("\(NumberText.fixed(first.value, 0))\(tempUnit)", color: color)
                }
            PointMark(x: .value("Time", last.at), y: .value("Temperature", lastY))
                .symbolSize(0)
                .annotation(position: Self.labelAbove(above, y: lastY) ? .top : .bottom, alignment: .trailing, spacing: 2) {
                    endLabel("\(NumberText.fixed(last.value, 0))\(tempUnit)", color: color)
                }
        }
    }

    /// A label wants to be above its line (pack) or below it (outside), but one under a line that ends near the floor of
    /// the plot would land on the time labels, so it goes above instead.
    static func labelAbove(_ above: Bool, y: Double) -> Bool { above || y < 0.15 }

    /// The outside temperature, mapped like the pack's: a thin dashed line (the pack's is solid, so the two differ by
    /// more than colour), its ends named below it, the touch readout giving the real value at a moment.
    @ChartContentBuilder
    private func outsideMarks() -> some ChartContent {
        let pts = outsideInUnit
        let range = ChargeCurve.tempRange(pts)
        ForEach(Array(pts.enumerated()), id: \.offset) { _, p in
            LineMark(x: .value("Time", p.at), y: .value("Outside", ChargeCurve.unit(temp: p.value, range: range)), series: .value("Series", "Outside"))
                .foregroundStyle(Self.outsideColor)
                .lineStyle(StrokeStyle(lineWidth: 1.2, lineCap: .round, lineJoin: .round, dash: [3, 3]))
                .interpolationMethod(.linear)
        }
        endLabels(pts, range: range, color: Self.outsideColor, above: false)
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

    /// What the pack cells' spread was: a plain fact, not a verdict (a DC charge normally has a spread).
    static func spreadNote(spreadC: Double, metric: Bool) -> String {
        let spread = metric ? spreadC : spreadC * 1.8
        return "Pack cell temperatures: the hottest and coolest cells were up to \(NumberText.fixed(spread, 1))\(metric ? "°C" : "°F") apart."
    }

    @ViewBuilder
    private var note: some View {
        if !hasPower {
            Text("No charger-power samples for this session.").themeText(.caption, color: Theme.textTertiary)
        } else if showPack, series.packSpreadC > 0 {
            Text(Self.spreadNote(spreadC: series.packSpreadC, metric: metric)).themeText(.caption, color: Theme.textTertiary)
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
