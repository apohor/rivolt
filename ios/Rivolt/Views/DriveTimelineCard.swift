import Charts
import SwiftUI
import UIKit

/// One drive's timeline figures from its samples (DriveTimelineMath), computed once when the samples arrive.
struct DriveTimelineData: Equatable {
    typealias M = DriveTimelineMath

    let start: Date
    let end: Date
    let speed: [M.Pt]
    let soc: [M.Pt]
    let power: [M.PowerPt]
    let modes: [M.ModeSegment]
    let moments: [M.Moment]
    let drawKwh: Double
    let regenKwh: Double
    let regenPct: Double

    /// The samples inside the drive (a round trip's whole span); nil with fewer than two.
    init?(drive: ActivityDrive, samples: [DriveSample]) {
        let inDrive = samples.filter { $0.at >= drive.startedAt && $0.at <= drive.endedAt }.sorted { $0.at < $1.at }
        guard inDrive.count >= 2, drive.endedAt > drive.startedAt else { return nil }
        start = drive.startedAt
        end = drive.endedAt
        speed = M.speedPts(inDrive)
        soc = M.socPts(inDrive)
        power = M.power(inDrive, elev: M.elevPts(inDrive), energyKwh: drive.energyUsedKwh)
        let r = M.regen(power)
        drawKwh = r.drawKwh
        regenKwh = r.regenKwh
        regenPct = r.pct
        modes = M.modeSegments(inDrive, start: M.ms(drive.startedAt), end: M.ms(drive.endedAt))
        moments = M.moments(speed: speed, soc: soc, power: power)
    }

    /// The modes the drive was in, in order of first use.
    var distinctModes: [M.Mode] {
        var seen: [M.Mode] = []
        for m in modes where !seen.contains(m.mode) { seen.append(m.mode) }
        return seen
    }

    private static func nearest<T>(_ pts: [T], _ x: (T) -> Double, _ t: Double) -> T? {
        pts.min { abs(x($0) - t) < abs(x($1) - t) }
    }

    /// The readings nearest a moment: speed (mph), power (kW), battery (%).
    func values(at date: Date) -> (speed: Double?, power: Double?, soc: Double?) {
        let t = M.ms(date)
        return (Self.nearest(speed, \.x, t)?.y, Self.nearest(power, \.x, t)?.y, Self.nearest(soc, \.x, t)?.y)
    }

    /// The "Regen recovered" tile (the web's): kWh and its share of the energy drawn.
    var regenTile: KpiTile {
        KpiTile(id: "regen", label: "Regen recovered", value: NumberText.fixed(regenKwh, 2), unit: "kWh",
                hint: "\(NumberText.fixed(regenPct, 0))% of consumption")
    }
}

/// The web's "Drive timeline" card on the drive's screen: the whole-drive line (or the moment under the cursor),
/// speed, the modelled power (draw above 0, regen below) and battery in three panels on one time axis, the drive
/// mode, and the moments as buttons that move the cursor. Touch-drag scrubs; VoiceOver adjusts. At accessibility text
/// sizes, rows instead of panels.
struct DriveTimelineCard: View {
    let drive: ActivityDrive
    let phase: DriveDetailModel.Phase
    let data: DriveTimelineData?
    var calendar: Calendar = .current
    var metric: Bool = NowFormat.usesMetric

    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var cursor: Date?

    typealias M = DriveTimelineMath
    private var k: Double { metric ? 1.609344 : 1 }
    private var speedUnit: String { metric ? "km/h" : "mph" }

    var body: some View {
        ThemeSection(title: "Drive timeline") {
            switch phase {
            case .loading:
                ProgressView().frame(maxWidth: .infinity).padding(.vertical, 6 * Theme.unit)
            case .failed:
                Text("Couldn't load this drive's timeline.").themeText(.subline)
            case .loaded:
                if let data { content(data) } else { Text("No timeline was recorded for this drive.").themeText(.subline) }
            }
        }
    }

    private func time(_ d: Date) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).hour().minute()
        f.calendar = calendar
        return d.formatted(f)
    }

    /// The whole drive, or the moment under the cursor.
    private func summary(_ data: DriveTimelineData) -> String {
        if let c = cursor {
            let v = data.values(at: c)
            var parts = [time(c)]
            if let s = v.speed { parts.append("\(NumberText.fixed(s * k, 0)) \(speedUnit)") }
            if let p = v.power { parts.append("\(NumberText.fixed(p, 0)) kW") }
            if let b = v.soc { parts.append("battery \(NumberText.fixed(b, 0))%") }
            return parts.joined(separator: " · ")
        }
        var parts = ["Whole drive", "max \(NumberText.fixed(drive.maxSpeedMph * k, 0)) \(speedUnit)", "avg \(NumberText.fixed(drive.avgSpeedMph * k, 0)) \(speedUnit)"]
        if drive.energyUsedKwh > 0 { parts.append("used \(NumberText.fixed(drive.energyUsedKwh, 1)) kWh") }
        parts.append("battery \(NumberText.fixed(drive.startSocPct, 0)) → \(NumberText.fixed(drive.endSocPct, 0))%")
        return parts.joined(separator: " · ")
    }

    @ViewBuilder
    private func content(_ data: DriveTimelineData) -> some View {
        Text(summary(data))
            .themeText(.subline, color: Theme.textBody)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 2 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.background.opacity(0.6), in: RoundedRectangle(cornerRadius: Theme.radiusInner))
            .accessibilityLabel(SpokenText.words(summary(data)))
        if typeSize.isAccessibilitySize {
            rows(data)
        } else {
            panels(data).dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        }
        mode(data)
        if !data.moments.isEmpty { moments(data) }
    }

    // MARK: Panels

    private static let speedHeight = 30 * Theme.unit
    private static let powerHeight = 20 * Theme.unit
    private static let batteryHeight = 18 * Theme.unit

    private func panels(_ data: DriveTimelineData) -> some View {
        VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            speedPanel(data)
            if data.power.count > 1 { powerPanel(data) }
            if data.soc.count > 1 { batteryPanel(data) }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Drive timeline: speed, power and battery")
        .accessibilityValue(SpokenText.words(summary(data)))
        .accessibilityHint("Swipe up or down to move through the drive.")
        .accessibilityAdjustableAction { direction in
            let span = data.end.timeIntervalSince(data.start)
            let step = span * 0.05
            let now = cursor ?? (direction == .increment ? data.start.addingTimeInterval(-step) : data.end.addingTimeInterval(step))
            let next = direction == .increment ? now.addingTimeInterval(step) : now.addingTimeInterval(-step)
            cursor = min(max(next, data.start), data.end)
        }
    }

    private func date(_ ms: Double) -> Date { Date(timeIntervalSince1970: ms / 1000) }

    /// Points split at the samples' gaps, so a line never bridges a hole.
    private static func runs(_ p: [M.PowerPt]) -> [(id: Int, pt: M.PowerPt)] {
        var run = 0
        return p.map { pt in
            if pt.gap { run += 1 }
            return (run, pt)
        }
    }

    private func speedPanel(_ data: DriveTimelineData) -> some View {
        // The scale fits every sample; the stated max is the drive's own (the summary's, as the web), so the card never
        // shows two maxima.
        let top = Swift.max(10, Swift.max(drive.maxSpeedMph, data.speed.map(\.y).max() ?? 0) * k * 1.1)
        return Chart {
            ForEach(Array(data.speed.enumerated()), id: \.offset) { _, p in
                AreaMark(x: .value("Time", date(p.x)), yStart: .value("Floor", 0), yEnd: .value("Speed", p.y * k))
                    .foregroundStyle(LinearGradient(colors: [Theme.chartSocDrive.opacity(0.3), .clear], startPoint: .top, endPoint: .bottom))
                    .interpolationMethod(.linear)
                LineMark(x: .value("Time", date(p.x)), y: .value("Speed", p.y * k))
                    .foregroundStyle(Theme.chartSocDrive)
                    .lineStyle(StrokeStyle(lineWidth: 1.5, lineJoin: .round))
                    .interpolationMethod(.linear)
            }
            ForEach(data.moments) { m in
                PointMark(x: .value("Moment", date(m.ms)), y: .value("Speed", (data.values(at: date(m.ms)).speed ?? 0) * k))
                    .symbolSize(40)
                    .foregroundStyle(Self.color(m.tone))
            }
            cursorMark
        }
        .chartXScale(domain: data.start...data.end)
        .chartYScale(domain: 0...top)
        .chartXAxis(.hidden)
        .chartYAxis { AxisMarks(values: [top / 2]) { _ in AxisGridLine().foregroundStyle(Theme.border.opacity(0.55)) } }
        .chartXSelection(value: $cursor)
        .frame(height: Self.speedHeight)
        .panelTitled("Speed · \(speedUnit) · max \(NumberText.fixed(drive.maxSpeedMph * k, 0))")
    }

    private func powerPanel(_ data: DriveTimelineData) -> some View {
        // Draw sets the top; the regen side is at least a quarter of it, so a quiet drive's regen still has room and
        // the panel isn't half empty.
        let peak = Swift.max(10, data.power.map(\.y).max() ?? 0)
        let floor = Swift.max(peak / 4, -(data.power.map(\.y).min() ?? 0))
        let runs = Self.runs(data.power)
        return Chart {
            RuleMark(y: .value("Zero", 0)).foregroundStyle(Theme.border).lineStyle(StrokeStyle(lineWidth: 0.5))
            ForEach(Array(runs.enumerated()), id: \.offset) { _, r in
                AreaMark(x: .value("Time", date(r.pt.x)), yStart: .value("Zero", 0), yEnd: .value("Draw", Swift.max(0, r.pt.y)), series: .value("Run", "d\(r.id)"))
                    .foregroundStyle(Theme.timelineDraw.opacity(0.45))
                    .interpolationMethod(.linear)
                AreaMark(x: .value("Time", date(r.pt.x)), yStart: .value("Zero", 0), yEnd: .value("Regen", Swift.min(0, r.pt.y)), series: .value("Run", "r\(r.id)"))
                    .foregroundStyle(Theme.timelineRegen.opacity(0.55))
                    .interpolationMethod(.linear)
            }
            cursorMark
        }
        .chartXScale(domain: data.start...data.end)
        .chartYScale(domain: -floor...peak)
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartXSelection(value: $cursor)
        // The sign in words, not colour alone: "Draw" above the zero line, "Regen" below it, at the left edge.
        .overlay(alignment: .leading) {
            VStack(alignment: .leading, spacing: 0) {
                PanelLabel(text: "Draw ↑", halo: true)
                PanelLabel(text: "Regen ↓", halo: true)
            }
            .offset(y: Self.powerHeight * CGFloat((peak - floor) / (peak + floor)) / 2)
        }
        .frame(height: Self.powerHeight)
        .panelTitled("Power · kW · up to \(NumberText.fixed(peak, 0)) drawn")
    }

    private func batteryPanel(_ data: DriveTimelineData) -> some View {
        let lo = Swift.max(0, ((data.soc.map(\.y).min() ?? 0) - 2).rounded(.down))
        let hi = Swift.min(100, ((data.soc.map(\.y).max() ?? 100) + 2).rounded(.up))
        let first = data.start, last = data.end
        return Chart {
            ForEach(Array(data.soc.enumerated()), id: \.offset) { _, p in
                LineMark(x: .value("Time", date(p.x)), y: .value("Battery", p.y))
                    .foregroundStyle(Theme.routeStart)
                    .lineStyle(StrokeStyle(lineWidth: 2, lineJoin: .round))
                    .interpolationMethod(.linear)
            }
            cursorMark
        }
        .chartXScale(domain: first...last)
        .chartYScale(domain: lo...Swift.max(hi, lo + 1))
        .chartYAxis { AxisMarks(values: [(lo + hi) / 2]) { _ in AxisGridLine().foregroundStyle(Theme.border.opacity(0.55)) } }
        .chartXAxis {
            AxisMarks(values: [first, first.addingTimeInterval(last.timeIntervalSince(first) / 2), last]) { value in
                AxisValueLabel(anchor: value.index == 0 ? .topLeading : value.index == 2 ? .topTrailing : .top, collisionResolution: .disabled) {
                    if let d = value.as(Date.self) { PanelLabel(text: time(d), plain: true) }
                }
            }
        }
        .chartXSelection(value: $cursor)
        .frame(height: Self.batteryHeight + 5 * Theme.unit)
        .panelTitled("Battery · % · \(NumberText.fixed(lo, 0))–\(NumberText.fixed(hi, 0))")
    }

    @ChartContentBuilder
    private var cursorMark: some ChartContent {
        if let cursor {
            RuleMark(x: .value("Cursor", cursor)).foregroundStyle(Theme.textTertiary).lineStyle(StrokeStyle(lineWidth: 1))
        }
    }

    static func color(_ tone: M.Moment.Tone) -> Color {
        switch tone {
        case .warning: return Theme.timelineDraw
        case .success: return Theme.timelineRegen
        case .info: return Theme.chartSocDrive
        case .neutral: return Theme.textTertiary
        }
    }

    // MARK: Rows (accessibility text sizes)

    private func rows(_ data: DriveTimelineData) -> some View {
        let peakDraw = data.power.map(\.y).max() ?? 0, peakRegen = data.power.map(\.y).min() ?? 0
        let socs = data.soc.map(\.y)
        return VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            Text("Speed: max \(NumberText.fixed(drive.maxSpeedMph * k, 0)) \(speedUnit), average \(NumberText.fixed(drive.avgSpeedMph * k, 0)) \(speedUnit)")
            if data.power.count > 1 {
                Text("Power: up to \(NumberText.fixed(peakDraw, 0)) kW drawn, up to \(NumberText.fixed(abs(Swift.min(0, peakRegen)), 0)) kW recovered")
            }
            if let a = socs.first, let b = socs.last {
                Text("Battery: \(NumberText.fixed(a, 0))% → \(NumberText.fixed(b, 0))%")
            }
        }
        .themeText(.value, color: Theme.textPrimary)
        .fixedSize(horizontal: false, vertical: true)
    }

    // MARK: Mode and moments

    @ViewBuilder
    private func mode(_ data: DriveTimelineData) -> some View {
        let modes = data.distinctModes
        if modes.count > 1 && typeSize.isAccessibilitySize {
            // At accessibility sizes the names don't fit in the band: a sentence instead.
            Text("Drive modes: " + modes.map(\.label).joined(separator: ", then ")).themeText(.value, color: Theme.textPrimary)
        } else if modes.count > 1 {
            VStack(alignment: .leading, spacing: Theme.unit) {
                GeometryReader { geo in
                    let span = data.end.timeIntervalSince(data.start) * 1000
                    HStack(spacing: 0) {
                        ForEach(Array(data.modes.enumerated()), id: \.offset) { _, s in
                            let w = geo.size.width * CGFloat((s.x1 - s.x0) / span)
                            Rectangle().fill(Self.modeColor(s.mode).opacity(0.85))
                                .frame(width: w)
                                .overlay {
                                    // The name where it fits; the legend says it for the rest.
                                    if w > 20 * Theme.unit {
                                        Text(s.mode.label).themeText(.caption, color: Theme.chargePinInk).lineLimit(1)
                                    }
                                }
                        }
                    }
                }
                .frame(height: 5 * Theme.unit)
                .clipShape(RoundedRectangle(cornerRadius: Theme.unit))
                // The band names its segments; the legend only when one is too narrow to hold its name.
                let span = data.end.timeIntervalSince(data.start) * 1000
                if data.modes.contains(where: { ($0.x1 - $0.x0) / span < 0.2 }) {
                ThemeFlow(spacing: 3 * Theme.unit, rowSpacing: Theme.unit) {
                    ForEach(modes, id: \.rawValue) { m in
                        HStack(spacing: Theme.unit) {
                            RoundedRectangle(cornerRadius: 1).fill(Self.modeColor(m)).frame(width: 3 * Theme.unit, height: 2 * Theme.unit)
                            Text(m.label).themeText(.caption)
                        }
                    }
                }
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Drive modes: " + modes.map(\.label).joined(separator: ", "))
        } else if let m = modes.first {
            Text("Drive mode: \(m.label)").themeText(.subline)
        }
    }

    static func modeColor(_ m: M.Mode) -> Color {
        switch m {
        case .conserve: return Theme.modeConserve
        case .allPurpose: return Theme.modeAllPurpose
        case .sport: return Theme.modeSport
        case .other: return Theme.modeOther
        }
    }

    private func moments(_ data: DriveTimelineData) -> some View {
        VStack(alignment: .leading, spacing: Theme.unit) {
            Text("Moments").themeText(.label)
            ThemeFlow(spacing: 2 * Theme.unit, rowSpacing: 2 * Theme.unit) {
                ForEach(data.moments) { m in
                    let at = date(m.ms)
                    Button {
                        cursor = at
                        UIAccessibility.post(notification: .announcement, argument: SpokenText.words(summary(data)))
                    } label: {
                        HStack(spacing: 1.5 * Theme.unit) {
                            Circle().fill(Self.color(m.tone)).frame(width: 2 * Theme.unit, height: 2 * Theme.unit)
                            Text("\(time(at)) \(m.label)").themeText(.caption, color: Theme.textBody)
                        }
                        .padding(.horizontal, 3 * Theme.unit)
                        .frame(minHeight: 11 * Theme.unit)
                        .background(Theme.card, in: Capsule())
                        .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("\(m.label) at \(time(at))")
                    .accessibilityHint(SpokenText.words(m.detail(metric: metric)))
                }
            }
        }
    }
}

private extension View {
    /// A panel's name and scale on its own line above the plot, left-aligned with the others so the plots line up and
    /// the label never sits on the line.
    func panelTitled(_ text: String) -> some View {
        VStack(alignment: .leading, spacing: 0.5 * Theme.unit) {
            PanelLabel(text: text, plain: true)
            self
        }
    }
}

/// A small label on a panel.
private struct PanelLabel: View {
    let text: String
    var plain = false
    /// On a small halo in the card's colour (as #913's temperature labels), so a trace never runs through it.
    var halo = false

    var body: some View {
        if halo {
            label
                .padding(.horizontal, 0.75 * Theme.unit).padding(.vertical, 0.25 * Theme.unit)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.unit, style: .continuous))
        } else {
            label
                .padding(.horizontal, plain ? 0 : Theme.unit).padding(.vertical, plain ? 0 : 0.5 * Theme.unit)
                .background(plain ? .clear : Theme.background.opacity(0.7), in: RoundedRectangle(cornerRadius: Theme.unit))
        }
    }

    private var label: some View {
        Text(text)
            .font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2))
            .foregroundStyle(Theme.textTertiary)
            .accessibilityHidden(true)
    }
}
