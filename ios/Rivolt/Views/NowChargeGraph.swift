import SwiftUI

/// The Now card's charging graph (design_preview key now_charge_graph), in the soft look of the web's: the open charge's battery as one soft line
/// with a glow, the charger's power as a quieter second line, and the way on to the target as a soft dotted continuation with its finish time in a
/// small pill. Too little to draw: one line says so, and no frame. Read-only; the figures come from NowChargeGraphModel.
struct NowChargeGraph: View {
    /// Nil while the charge has too little to draw.
    let model: NowChargeGraphModel?

    @Environment(\.colorScheme) private var scheme
    @Environment(\.dynamicTypeSize) private var typeSize

    private let padT: CGFloat = 30
    private let padB: CGFloat = 26
    private let padL: CGFloat = 6
    private let padR: CGFloat = 10

    private var band: Color { scheme == .light ? Color(red: 0.388, green: 0.4, blue: 0.945) : Color(red: 0.506, green: 0.549, blue: 0.973) }

    var body: some View {
        if let model {
            VStack(alignment: .leading, spacing: 4) {
                header(model)
                plot(model).frame(height: 170)
                legend(model)
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(model.spoken)
            .accessibilityIdentifier("now-charge-graph")
        } else {
            Text("Collecting this charge's first minutes. The graph appears shortly.")
                .themeText(.subline, color: Theme.textSecondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("now-charge-graph-collecting")
        }
    }

    private func header(_ m: NowChargeGraphModel) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text("Battery while charging").themeText(.label)
            Spacer(minLength: 0)
            if let change = m.change {
                (Text(change).font(Theme.sans(ThemeTokens.typeSubline, .semibold, relativeTo: .footnote)).foregroundColor(Theme.textPrimary)
                    + Text(" \(m.since)").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textSecondary))
                    .monospacedDigit()
                    .accessibilityIdentifier("now-charge-graph-change")
            }
        }
    }

    private func plot(_ m: NowChargeGraphModel) -> some View {
        GeometryReader { geo in
            let w = geo.size.width
            let h = geo.size.height
            let base = h - padB
            let at = { (p: NowChargeGraphModel.Pt) in CGPoint(x: padL + p.x * (w - padL - padR), y: padT + (1 - p.y) * (base - padT)) }
            let soc = m.soc.map(at)
            let pow = m.power.map(at)
            let now = at(m.last)
            let end = m.estimate.map { at($0.end) }
            let startText = m.startedAt.formatted(date: .omitted, time: .shortened)
            let labelNow = now.x - padL > 170
            let labelStartValue = now.x - padL > 90
            let valueLeft = now.x - padL > 60
            ZStack(alignment: .topLeading) {
                Canvas { ctx, _ in
                    if let end {
                        var layer = ctx
                        layer.addFilter(.blur(radius: 9))
                        let r = CGRect(x: now.x + 12, y: padT + 4, width: max(end.x - now.x - 20, 4), height: base - padT - 4)
                        layer.fill(Path(roundedRect: r, cornerRadius: 14), with: .linearGradient(Gradient(colors: [band.opacity(0.08), band.opacity(0)]),
                                                                                                    startPoint: CGPoint(x: 0, y: r.minY), endPoint: CGPoint(x: 0, y: r.maxY)))
                    }
                    let line = Self.path(soc)
                    var fill = line
                    fill.addLine(to: CGPoint(x: now.x, y: base))
                    fill.addLine(to: CGPoint(x: soc.first?.x ?? 0, y: base))
                    fill.closeSubpath()
                    ctx.fill(fill, with: .linearGradient(Gradient(colors: [Theme.accent.opacity(0.18), Theme.accent.opacity(0)]),
                                                        startPoint: CGPoint(x: 0, y: padT), endPoint: CGPoint(x: 0, y: base)))
                    if pow.count > 1 {
                        ctx.stroke(Self.path(pow), with: .color(Theme.estimate.opacity(0.6)), style: StrokeStyle(lineWidth: 1.8, lineCap: .round, lineJoin: .round))
                    }
                    var glow = ctx
                    glow.addFilter(.blur(radius: 4))
                    glow.stroke(line, with: .color(Theme.accent.opacity(0.5)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    ctx.stroke(line, with: .color(Theme.accent.opacity(0.95)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    if let end {
                        var dots = Path()
                        dots.move(to: now)
                        dots.addLine(to: end)
                        ctx.stroke(dots, with: .color(Theme.accent.opacity(0.8)), style: StrokeStyle(lineWidth: 2.2, lineCap: .round, dash: [0.1, 7]))
                        Self.dot(&ctx, end, big: false)
                    }
                    if let s = soc.first { Self.dot(&ctx, s, big: false) }
                    Self.dot(&ctx, now, big: true)
                }
                if let end, let pill = m.estimate?.pill {
                    pillView(pill).padding(.trailing, padR).padding(.top, max(end.y - 30, 2)).frame(width: w, height: h, alignment: .topTrailing)
                }
                if labelStartValue, let s = soc.first {
                    text("\(m.startShown)%", x: s.x + 2, y: max(s.y - 14, 12), anchor: .leading, w: w, h: h, color: Theme.textSecondary, strong: false)
                }
                text("\(m.nowShown)%", x: valueLeft ? now.x - 10 : now.x + 10, y: max(now.y - 12, 12), anchor: valueLeft ? .trailing : .leading, w: w, h: h, color: Theme.textPrimary, strong: true)
                text(w >= 260 ? "Plugged in \(startText)" : startText, x: padL, y: h - 8, anchor: .leading, w: w, h: h, color: Theme.textTertiary, strong: false)
                if labelNow { text("now", x: now.x, y: h - 8, anchor: .center, w: w, h: h, color: Theme.textTertiary, strong: false) }
            }
        }
    }

    /// The pill ends at the plot's right padding, just above the target's dot.
    private func pillView(_ text: String) -> some View {
        Text(text).themeText(.caption, color: Theme.textBody)
            .padding(.horizontal, 10).padding(.vertical, 3)
            .background(Capsule().fill(Theme.textSecondary.opacity(0.15)))
            .fixedSize()
    }

    /// A short label with its leading, centre or trailing edge at `x`.
    private func text(_ s: String, x: CGFloat, y: CGFloat, anchor: HorizontalAlignment, w: CGFloat, h: CGFloat, color: Color, strong: Bool) -> some View {
        let label = Text(s)
            .font(strong ? Theme.sans(ThemeTokens.typeSubline, .semibold, relativeTo: .footnote) : Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption))
            .monospacedDigit().foregroundStyle(color).fixedSize()
        let align: Alignment = anchor == .leading ? .leading : anchor == .trailing ? .trailing : .center
        let box: CGFloat = anchor == .leading ? max(w - x, 1) : anchor == .trailing ? max(x, 1) : w
        let cx: CGFloat = anchor == .leading ? x + box / 2 : anchor == .trailing ? x / 2 : x
        return label.frame(width: box, alignment: align).position(x: cx, y: y)
            .frame(width: w, height: h, alignment: .topLeading)
    }

    private func legend(_ m: NowChargeGraphModel) -> some View {
        let items: [(color: Color, dash: Bool, label: String, id: String?)] =
            [(Theme.accent, false, "Battery", nil)]
            + (m.powerNow.map { [(Theme.estimate, false, "Power \($0) kW now", nil)] } ?? [])
            + (m.estimate != nil ? [(Theme.accent, true, "Estimate", "now-charge-graph-estimate")] : [])
        return FlowLayout(spacing: 16) {
            ForEach(items.indices, id: \.self) { i in
                HStack(spacing: 6) {
                    if items[i].dash {
                        Path { p in p.move(to: CGPoint(x: 0, y: 1)); p.addLine(to: CGPoint(x: 14, y: 1)) }
                            .stroke(items[i].color, style: StrokeStyle(lineWidth: 2, lineCap: .round, dash: [0.1, 4])).frame(width: 14, height: 2)
                    } else {
                        Capsule().fill(items[i].color).frame(width: 14, height: 2)
                    }
                    Text(items[i].label).themeText(.caption, color: Theme.textSecondary)
                }
                .accessibilityIdentifier(items[i].id ?? "")
            }
        }
        .padding(.top, 4)
    }

    private static func path(_ pts: [CGPoint]) -> Path {
        var p = Path()
        guard let first = pts.first else { return p }
        p.move(to: first)
        if pts.count == 1 { return p }
        for s in NowGraphCurve.segments(pts) { p.addCurve(to: s.end, control1: s.c1, control2: s.c2) }
        return p
    }

    /// A small dot with a soft halo; the big one is the live "now" dot.
    private static func dot(_ ctx: inout GraphicsContext, _ p: CGPoint, big: Bool) {
        let halo: CGFloat = big ? 10 : 7
        var layer = ctx
        layer.addFilter(.blur(radius: 3))
        layer.fill(Path(ellipseIn: CGRect(x: p.x - halo, y: p.y - halo, width: halo * 2, height: halo * 2)), with: .color(Theme.accent.opacity(big ? 0.35 : 0.3)))
        let r: CGFloat = big ? 4.5 : 3
        ctx.fill(Path(ellipseIn: CGRect(x: p.x - r, y: p.y - r, width: r * 2, height: r * 2)), with: .color(Theme.accent))
    }
}
