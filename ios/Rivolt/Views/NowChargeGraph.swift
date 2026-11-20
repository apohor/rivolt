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

    /// The pack temperature's own colour, as the web's `ink.pack`.
    private var packInk: Color { scheme == .light ? Color(red: 0.486, green: 0.227, blue: 0.929) : Color(red: 0.769, green: 0.71, blue: 0.992) }

    private var band: Color { scheme == .light ? Color(red: 0.388, green: 0.4, blue: 0.945) : Color(red: 0.506, green: 0.549, blue: 0.973) }

    var body: some View {
        if let model {
            VStack(alignment: .leading, spacing: 4) {
                header(model)
                plot(model).frame(height: 150)
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
            let pack = m.pack.map(at)
            let now = at(m.last)
            let end = m.estimate.map { at($0.end) }
            let startText = m.startedAt.formatted(date: .omitted, time: .shortened)
            let labelNow = now.x - padL > 170
            let labelStartValue = now.x - padL > 90
            let valueLeft = now.x - padL > 60
            // Near the end of a charge the "now" value would sit under the finish pill: it goes below the dot then.
            let pillWidth = CGFloat((m.estimate?.pill.count ?? 0)) * 6.5 + 24
            let pillLeft = w - padR - pillWidth
            let pillMidY = max((end?.y ?? 0) - 30, 2) + 11
            let valueEdge = valueLeft ? now.x - 10 : now.x + 40
            let nowBelow = end != nil && m.estimate?.pill != nil && abs(max(now.y - 12, 12) - pillMidY) < 20 && valueEdge > pillLeft - 6
            let nowText = "\(m.nowShown)%"
            let nowW = CGFloat(nowText.count) * 8.4
            // The now value under the dot stays inside the card's right edge.
            let belowX = min(now.x + 8, w - padR - nowW)
            let nowAnchorLeft = !valueLeft || nowBelow
            let nowTextY = nowBelow ? now.y + 24 : max(now.y - 12, 12)
            let nowBox = Self.box(x: nowBelow ? belowX : (valueLeft ? now.x - 10 : now.x + 10), y: nowTextY, tw: nowW, leading: nowAnchorLeft)
            // The start value goes above its dot, or just below it when the pack line starts up there.
            let startAbove = max((soc.first?.y ?? 0) - 14, 12)
            let packNearStart = pack.prefix(4).contains { p in p.x < (soc.first?.x ?? 0) + 40 && abs(p.y - startAbove) < 14 }
            let startY = packNearStart ? min((soc.first?.y ?? 0) + 15, base + 6) : startAbove
            let packSpot = Self.packSpot(m: m, pack: pack, soc: soc, pow: pow, now: now, nowBox: nowBox, end: end, pillLeft: pillLeft, pillMidY: pillMidY,
                                         startBox: labelStartValue ? soc.first.map { Self.box(x: $0.x + 2, y: startY, tw: CGFloat("\(m.startShown)%".count) * 7, leading: true) } : nil,
                                         w: w, h: h, base: base, padL: padL, padR: padR)
            ZStack(alignment: .topLeading) {
                Canvas { ctx, _ in
                    if let end {
                        var layer = ctx
                        layer.addFilter(.blur(radius: 9))
                        let r = CGRect(x: now.x + 12, y: padT + 4, width: max(end.x - now.x - 20, 4), height: base - padT - 4)
                        layer.fill(Path(roundedRect: r, cornerRadius: 28), with: .linearGradient(Gradient(colors: [band.opacity(0.08), band.opacity(0)]),
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
                    if pack.count > 1 {
                        ctx.stroke(Self.path(pack), with: .color(packInk), style: StrokeStyle(lineWidth: 1.4, lineCap: .round, lineJoin: .round))
                    }
                    var glow = ctx
                    glow.addFilter(.blur(radius: 4))
                    glow.stroke(line, with: .color(Theme.accent.opacity(0.5)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    ctx.stroke(line, with: .color(Theme.accent.opacity(0.95)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    if let end {
                        // Leaves "now" as a curve: the slope of the battery line over its last stretch (never steeper than the way to the target), easing in
                        // level at the target. Same rule as the web.
                        let dx = end.x - now.x
                        let dy = end.y - now.y
                        let from = soc.first { $0.x >= now.x - max(24, 0.08 * (now.x - (soc.first?.x ?? now.x))) } ?? now
                        let m = now.x - from.x > 1 ? (now.y - from.y) / (now.x - from.x) : 0
                        let rise = min(0, max(dy, m * dx * 0.4))
                        var dots = Path()
                        dots.move(to: now)
                        dots.addCurve(to: end, control1: CGPoint(x: now.x + dx * 0.4, y: now.y + rise), control2: CGPoint(x: end.x - dx * 0.35, y: end.y))
                        ctx.stroke(dots, with: .color(Theme.accent.opacity(0.8)), style: StrokeStyle(lineWidth: 2.2, lineCap: .round, dash: [0.1, 7]))
                        Self.dot(&ctx, end, big: false)
                    }
                    if let s = soc.first { Self.dot(&ctx, s, big: false) }
                    Self.dot(&ctx, now, big: true)
                    if let p = pack.last, pack.count > 1 { ctx.fill(Path(ellipseIn: CGRect(x: p.x - 3, y: p.y - 3, width: 6, height: 6)), with: .color(packInk)) }
                }
                if let end, let pill = m.estimate?.pill {
                    pillView(pill).padding(.trailing, padR).padding(.top, max(end.y - 30, 2)).frame(width: w, height: h, alignment: .topTrailing)
                }
                if labelStartValue, let s = soc.first {
                    text("\(m.startShown)%", x: s.x + 2, y: startY, anchor: .leading, w: w, h: h, color: Theme.textSecondary, strong: false)
                }
                if let spot = packSpot, let packNow = m.packNow {
                    text(packNow, x: spot.x, y: spot.y, anchor: spot.leading ? .leading : .trailing, w: w, h: h, color: packInk, strong: true)
                        .accessibilityIdentifier("now-charge-graph-pack-now")
                }
                if nowBelow {
                    text("\(m.nowShown)%", x: belowX, y: now.y + 24, anchor: .leading, w: w, h: h, color: Theme.textPrimary, strong: true)
                } else {
                    text("\(m.nowShown)%", x: valueLeft ? now.x - 10 : now.x + 10, y: max(now.y - 12, 12), anchor: valueLeft ? .trailing : .leading, w: w, h: h, color: Theme.textPrimary, strong: true)
                }
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
            + (m.powerNow.map { [(Theme.estimate, false, "Power \($0) kW", nil)] } ?? [])
            + (m.packNow.map { [(packInk, false, "Pack \($0)", "now-charge-graph-pack")] } ?? [])
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

    private struct Box {
        var x0: CGFloat, x1: CGFloat, y0: CGFloat, y1: CGFloat
    }

    private static func box(x: CGFloat, y: CGFloat, tw: CGFloat, leading: Bool, fs: CGFloat = 14) -> Box {
        Box(x0: leading ? x : x - tw, x1: leading ? x + tw : x, y0: y - fs, y1: y + 4)
    }

    /// Where the pack's value goes: the first candidate near the end of its line that touches no other label, pill or dot and sits on neither the
    /// battery nor the pack line. Nil when there is none: the legend alone carries the value, as on the web.
    private static func packSpot(m: NowChargeGraphModel, pack: [CGPoint], soc: [CGPoint], pow: [CGPoint], now: CGPoint, nowBox: Box, end: CGPoint?,
                                 pillLeft: CGFloat, pillMidY: CGFloat, startBox: Box?, w: CGFloat, h: CGFloat, base: CGFloat, padL: CGFloat, padR: CGFloat) -> (x: CGFloat, y: CGFloat, leading: Bool)? {
        guard let packNow = m.packNow, let last = pack.last, pack.count > 1 else { return nil }
        let tw = CGFloat(packNow.count) * 8.4
        var taken: [Box] = [nowBox, Box(x0: padL, x1: padL + 60, y0: h - 20, y1: h), Box(x0: now.x - 10, x1: now.x + 10, y0: now.y - 10, y1: now.y + 10)]
        // The power line runs level across the top; a label on it is hard to read.
        if pow.count > 1, let lo = pow.map(\.y).min(), let hi = pow.map(\.y).max() { taken.append(Box(x0: padL, x1: now.x, y0: lo - 1, y1: hi + 1)) }
        if let startBox { taken.append(startBox) }
        if m.estimate != nil { taken.append(Box(x0: pillLeft, x1: w - padR, y0: pillMidY - 10, y1: pillMidY + 10)) }
        if let end { taken.append(Box(x0: end.x - 12, x1: end.x + 12, y0: end.y - 12, y1: end.y + 12)) }
        let powTop = (pow.count > 1 ? pow.map(\.y).min() : nil).map { $0 - 8 } ?? last.y
        let spots: [(CGFloat, CGFloat, Bool)] = [
            (last.x - 10, last.y - 10, false), (last.x - 10, last.y + 18, false), (last.x + 10, last.y + 4, true), (last.x + 10, last.y - 10, true),
            (last.x + 10, last.y + 18, true), (last.x - 56, last.y - 10, false), (last.x - 56, last.y + 18, false), (last.x - 110, last.y - 10, false),
            (last.x - 110, last.y + 18, false), (last.x - 10, powTop, false), (last.x - 70, powTop, false),
        ]
        let lines = soc + pack
        func onLine(_ b: Box) -> Bool {
            for (i, p) in lines.enumerated() {
                let q = i + 1 < lines.count && (i + 1 != soc.count) ? lines[i + 1] : p
                for t in stride(from: 0.0, through: 1.0, by: 0.1) {
                    let x = p.x + (q.x - p.x) * t
                    let y = p.y + (q.y - p.y) * t
                    if x > b.x0 - 3, x < b.x1 + 3, y > b.y0 - 2, y < b.y1 + 2 { return true }
                }
            }
            return false
        }
        for (x, y, leading) in spots {
            let b = box(x: x, y: y, tw: tw, leading: leading)
            let clear = b.x0 >= padL && b.x1 <= w - padR && b.y0 >= 0 && b.y1 <= base + 2
                && !taken.contains { b.x0 < $0.x1 + 3 && b.x1 > $0.x0 - 3 && b.y0 < $0.y1 + 2 && b.y1 > $0.y0 - 2 }
            if clear, !onLine(b) { return (x, y, leading) }
        }
        return nil
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
