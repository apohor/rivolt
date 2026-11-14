import SwiftUI

/// The elevation along the route, under the plan's summary and linked to the map: tapping the chart moves a dot on the route, and a
/// tap on the route puts the chart's marker there (the web's ElevationChart). VoiceOver gets an adjustable value (swipe up and down to move along).
/// Terrain as context only: no energy figure comes from it.
struct ElevationChartView: View {
    let profile: ElevationProfile
    let metric: Bool
    /// The legs' summed climb and descent, or nil when the plan cannot say (the row then shows no figures).
    var totals: (up: Double, down: Double)? = nil
    /// Where the cursor is, 0 to 1 along the route, or nil for none.
    @Binding var share: Double?

    private static let step = 0.02

    /// Collapsed to one row until a tap opens the chart; the choice is kept on this iPhone.
    @AppStorage("rivolt.elevationOpen") private var open = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let total = PlanElevation.total(profile)
        let readout = PlanElevation.readout(profile, share: share, metric: metric)
        VStack(alignment: .leading, spacing: 0) {
            toggleRow
            if open { expanded(total: total, readout: readout) }
        }
        .themeCard(padding: 0)
    }

    private var toggleRow: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(spacing: 8))
        return Button { withAnimation(.easeOut(duration: 0.2)) { open.toggle() } } label: {
            layout {
                Text("Elevation").font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body)).foregroundStyle(Theme.textBody)
                if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
                HStack(spacing: 6) {
                    Text(totals.map { PlanElevation.climbText($0, metric: metric) } ?? "").themeText(.value, color: Theme.textSecondary)
                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(Theme.textTertiary)
                        .rotationEffect(.degrees(open ? 90 : 0))
                }
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2.5 * Theme.unit)
            .frame(minHeight: 44)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(totals.map { "Elevation, climbs \(PlanElevation.elevation($0.up, metric: metric)), descends \(PlanElevation.elevation($0.down, metric: metric))" } ?? "Elevation")
        .accessibilityValue(open ? "Chart shown" : "Chart hidden")
        .accessibilityHint(open ? "Hides the chart" : "Shows the chart")
        .accessibilityIdentifier("plan-elevation-toggle")
    }

    @ViewBuilder
    private func expanded(total: Double, readout: String) -> some View {
        Rectangle().fill(Theme.border).frame(height: 1)
        VStack(alignment: .leading, spacing: 8) {
            readoutText(readout)
            chart(total: total)
                .frame(height: 110)
                .accessibilityElement()
                .accessibilityIdentifier("plan-elevation-chart")
                .accessibilityLabel(PlanElevation.label(profile, metric: metric))
                .accessibilityValue(share == nil ? "Not set. Swipe up or down to move along the route." : readout)
                .accessibilityAdjustableAction { direction in
                    switch direction {
                    case .increment: share = min(1, (share ?? 0) + Self.step)
                    case .decrement: share = max(0, (share ?? 0) - Self.step)
                    @unknown default: break
                    }
                }
            Text(PlanElevation.context).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(4 * Theme.unit)
    }

    private func readoutText(_ text: String) -> some View {
        Text(text).themeText(.subline, color: Theme.textSecondary)
            .accessibilityIdentifier("plan-elevation-readout")
            .accessibilityHidden(true)
    }

    private func chart(total: Double) -> some View {
        GeometryReader { geo in
            let g = Geometry(profile: profile, metric: metric, size: geo.size)
            ZStack {
                Canvas { ctx, _ in
                    var area = g.line
                    area.addLine(to: CGPoint(x: g.right, y: g.base))
                    area.addLine(to: CGPoint(x: g.left, y: g.base))
                    area.closeSubpath()
                    ctx.fill(area, with: .color(Theme.accent.opacity(0.14)))
                    ctx.stroke(g.line, with: .color(Theme.accent), lineWidth: 2)
                    var axis = Path()
                    axis.move(to: CGPoint(x: g.left, y: g.base))
                    axis.addLine(to: CGPoint(x: g.right, y: g.base))
                    ctx.stroke(axis, with: .color(Theme.border), lineWidth: 1)
                    for s in PlanElevation.stops(profile) {
                        let x = g.x(s.share), y = g.y(PlanElevation.height(profile, at: s.share))
                        var rule = Path()
                        rule.move(to: CGPoint(x: x, y: g.top))
                        rule.addLine(to: CGPoint(x: x, y: g.base))
                        ctx.stroke(rule, with: .color(Theme.estimate), style: StrokeStyle(lineWidth: 1, dash: [3, 3]))
                        ctx.fill(Path(ellipseIn: CGRect(x: x - 4, y: y - 4, width: 8, height: 8)), with: .color(Theme.estimate))
                    }
                    if let share {
                        let x = g.x(share), y = g.y(PlanElevation.height(profile, at: share))
                        var rule = Path()
                        rule.move(to: CGPoint(x: x, y: g.top))
                        rule.addLine(to: CGPoint(x: x, y: g.base))
                        ctx.stroke(rule, with: .color(Theme.textPrimary), lineWidth: 1)
                        ctx.fill(Path(ellipseIn: CGRect(x: x - 6, y: y - 6, width: 12, height: 12)), with: .color(Theme.card))
                        ctx.fill(Path(ellipseIn: CGRect(x: x - 4.5, y: y - 4.5, width: 9, height: 9)), with: .color(Theme.textPrimary))
                    }
                }
                VStack {
                    Spacer()
                    HStack {
                        Text("0").themeText(.caption, color: Theme.textTertiary)
                        Spacer()
                        Text(PlanElevation.distance(total, metric: metric)).themeText(.caption, color: Theme.textTertiary)
                    }
                    .padding(.horizontal, g.left)
                }
                .allowsHitTesting(false)
            }
            .contentShape(Rectangle())
            // A tap sets the cursor. There is no drag: any drag gesture on the chart, even one running alongside, holds back the details'
            // scroll under a vertical swipe that starts here, so scrubbing waits for a handle outside the scrolling area.
            .simultaneousGesture(SpatialTapGesture(coordinateSpace: .local).onEnded { tap in share = g.share(atX: tap.location.x) })
        }
    }

    /// The plot: the line and the places in the view's points.
    private struct Geometry {
        let left: CGFloat = 10, top: CGFloat = 8
        let right: CGFloat, base: CGFloat
        let line: Path
        private let total: Double, lo: Double, hi: Double, metric: Bool

        init(profile: ElevationProfile, metric: Bool, size: CGSize) {
            right = max(size.width - 10, 11)
            base = max(size.height - 24, 9)
            total = PlanElevation.total(profile)
            self.metric = metric
            let r = PlanElevation.range(profile)
            let pad = max((r.hi - r.lo) * 0.08, 1)
            lo = r.lo - pad
            hi = r.hi + pad
            var p = Path()
            for (i, pt) in profile.points.enumerated() {
                let point = CGPoint(x: 10 + pt[0] / total * Double(right - 10), y: Double(base) - (pt[1] - lo) / (hi - lo) * Double(base - 8))
                if i == 0 { p.move(to: point) } else { p.addLine(to: point) }
            }
            line = p
        }

        func x(_ share: Double) -> CGFloat { left + CGFloat(share) * (right - left) }
        func y(_ meters: Double) -> CGFloat { base - CGFloat((meters - lo) / (hi - lo)) * (base - top) }
        func share(atX x: CGFloat) -> Double { Double(min(1, max(0, (x - left) / (right - left)))) }
    }
}
