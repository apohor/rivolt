import SwiftUI

/// The route's legend, in a compact card directly under the map (not over it): the colour-mode switch when the drive has more than one, the
/// one-line legend ("Speed 12–64 mph") over a full-width bar, and the drive's two times, so they are always readable whatever the map shows.
/// The map stays clean and the route gets all of its height.
struct RouteLegendCard: View {
    let drawing: RouteSnap.Drawing
    /// The route's layers are on a working basemap (the legend says nothing about a line that is not there).
    let ready: Bool
    let metric: RouteGradient.Metric
    let metrics: [RouteGradient.Metric]
    var selection: Binding<RouteGradient.Metric>?
    var times: (start: String?, end: String?)?
    @Environment(\.colorScheme) private var scheme

    private var range: (lo: Double, hi: Double)? {
        guard ready, let r = drawing.range(for: metric), metric != .speed || drawing.usesGradient else { return nil }
        return r
    }

    /// "12:46 AM → 1:06 AM" when both times are known.
    static func timesLine(_ times: (start: String?, end: String?)?) -> String? {
        guard let s = times?.start, let e = times?.end else { return nil }
        return "\(s) → \(e)"
    }

    var body: some View {
        if let r = range {
            let legend = GradientLegend(lo: r.lo, hi: r.hi, metric: metric)
            let traits = UITraitCollection(userInterfaceStyle: scheme == .dark ? .dark : .light)
            VStack(alignment: .leading, spacing: 6) {
                if metrics.count > 1, let selection { MetricSwitch(metrics: metrics, selection: selection) }
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline) { line(legend); Spacer(minLength: 8); timesText }
                    VStack(alignment: .leading, spacing: 2) { line(legend); timesText }
                }
                RoundedRectangle(cornerRadius: 2)
                    .fill(LinearGradient(gradient: RouteRamp.ramp(metric).gradient(traits: traits), startPoint: .leading, endPoint: .trailing))
                    .frame(height: 4)
                    .accessibilityHidden(true)
            }
            .card()
            .accessibilityElement(children: .contain)
        } else if ready, drawing.pieces.contains(where: { $0.speed != nil }) {
            VStack(alignment: .leading, spacing: 6) {
                SpeedLegend(bands: drawing.usedSpeeds)
                timesText
            }
            .accessibilityElement(children: .contain)
        }
    }

    private func line(_ legend: GradientLegend) -> some View {
        Text(legend.line).lineLimit(1).fixedSize()
            .accessibilityLabel("Route coloured by \(GradientLegend.spoken(metric)), from \(legend.labels.min) to \(legend.labels.max)")
    }

    @ViewBuilder private var timesText: some View {
        if let t = Self.timesLine(times) {
            Text(t).monospacedDigit().foregroundStyle(Theme.textSecondary).lineLimit(1).fixedSize()
                .accessibilityLabel("Start \(times?.start ?? ""), end \(times?.end ?? "")")
        }
    }
}

private extension View {
    /// The card the legend sits in, under the map.
    func card() -> some View {
        self.mapCaption()
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .foregroundStyle(Theme.textBody)
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }
}
