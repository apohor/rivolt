import SwiftUI

/// The Now card's "Battery while parked" graph (design_preview key now_parked_graph), in the soft look of the charging graph's: the last 24 hours of
/// the battery as one soft line with a glow, each sleep as a soft indigo fill under the line with its length, a dot at each wake-up, the start and
/// now readings, and the parked marker's pill. Under it the figures as tiles and, once the truck has been parked a few hours, "At this rate".
/// Read-only; the numbers come from NowParkedGraphModel. The line is drawn through the same monotone-cubic curve as the charging graph's.
struct NowParkedGraph: View {
    let model: NowParkedGraphModel

    @Environment(\.colorScheme) private var scheme
    @Environment(\.dynamicTypeSize) private var typeSize

    private let padT: CGFloat = 34
    private let padB: CGFloat = 26
    private let padL: CGFloat = 6
    private let padR: CGFloat = 10
    private let bar: CGFloat = 5
    private let labelMinW: CGFloat = 58

    private var band: Color { scheme == .light ? Color(red: 0.388, green: 0.4, blue: 0.945) : Color(red: 0.647, green: 0.706, blue: 0.988) }
    private var wake: Color { Theme.estimate }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            header
            plot.frame(height: typeSize.isAccessibilitySize ? 220 : 192)
            legend
            tiles
            if let p = model.projection {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Capsule().fill(Theme.accent.opacity(0.6)).frame(width: 14, height: 2)
                    Text(p).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                }
                .padding(.top, 8)
                .accessibilityIdentifier("now-parked-projection")
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(model.spoken)
        .accessibilityIdentifier("now-parked-graph")
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text("Battery while parked").themeText(.label)
            Spacer(minLength: 0)
            (Text(model.change).font(Theme.sans(ThemeTokens.typeSubline, .semibold, relativeTo: .footnote)).foregroundColor(Theme.textPrimary)
                + Text(" \(model.since)").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textSecondary))
                .monospacedDigit()
                .multilineTextAlignment(.trailing)
                .accessibilityIdentifier("now-parked-change")
        }
    }

    private var plot: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let h = geo.size.height
            let base = h - padB
            let sleepBase = base - bar - 3
            let at = { (p: NowParkedGraphModel.Pt) in CGPoint(x: padL + p.x * (w - padL - padR), y: padT + (1 - p.y) * (base - padT)) }
            let line = model.line.map(at)
            let now = at(model.last.pt)
            let start = at(model.start.pt)
            let sinceIn = model.sinceAt != nil
            // The start label is right-anchored when the parked marker sits close to now, so it never runs into the end value.
            let startRight = sinceIn && now.x - start.x < 90
            let valueLeft = now.x - padL > 60
            let parkedPt = model.parkedX.map { CGPoint(x: padL + $0 * (w - padL - padR), y: 0) }
            let pillW = (model.parkedLabel.map { CGFloat($0.count) * 6.4 } ?? 0) + 20
            let pill: String? = {
                guard !typeSize.isAccessibilitySize, let label = model.parkedLabel, let p = parkedPt else { return nil }
                return p.x - pillW / 2 > (line.first?.x ?? start.x) + 56 && p.x + pillW / 2 < w - padR ? label : nil
            }()
            ZStack(alignment: .topLeading) {
                Canvas { ctx, _ in
                    // The readings that carry a dot or a label stay on the line when it is eased: the first and last, the wake-ups and the parked marker's.
                    let pinned = Set(line.indices.filter { i in i == 0 || i == line.count - 1 || line[i] == start || model.wakes.contains { at($0) == line[i] } })
                    let path = Self.path(line, pinned: pinned)
                    var fill = path
                    fill.addLine(to: CGPoint(x: now.x, y: sleepBase))
                    fill.addLine(to: CGPoint(x: line.first?.x ?? 0, y: sleepBase))
                    fill.closeSubpath()
                    for sl in model.sleeps {
                        let a = padL + sl.x0 * (w - padL - padR)
                        let b = padL + sl.x1 * (w - padL - padR)
                        guard b - a >= 2 else { continue }
                        var layer = ctx
                        // Rounded corners: a sleep is a soft shape, never a hard-cornered block.
                        layer.clip(to: Path(roundedRect: CGRect(x: a + 1, y: 0, width: max(b - a - 2, 1), height: sleepBase), cornerRadius: 10))
                        layer.fill(fill, with: .linearGradient(Gradient(colors: [band.opacity(scheme == .light ? 0.22 : 0.34), band.opacity(scheme == .light ? 0.04 : 0.05)]),
                                                               startPoint: CGPoint(x: 0, y: padT), endPoint: CGPoint(x: 0, y: sleepBase)))
                        ctx.fill(Path(roundedRect: CGRect(x: a + 1, y: base - bar, width: max(b - a - 2, 1), height: bar), cornerRadius: bar / 2),
                                 with: .color(band.opacity(0.75)))
                    }
                    if let p = parkedPt, p.x > padL {
                        var rule = Path()
                        rule.move(to: CGPoint(x: p.x, y: padT - 4))
                        rule.addLine(to: CGPoint(x: p.x, y: sleepBase))
                        ctx.stroke(rule, with: .color(Theme.textTertiary.opacity(0.35)), lineWidth: 1)
                    }
                    var glow = ctx
                    glow.addFilter(.blur(radius: 4))
                    glow.stroke(path, with: .color(Theme.accent.opacity(0.5)), style: StrokeStyle(lineWidth: 3, lineCap: .round, lineJoin: .round))
                    ctx.stroke(path, with: .color(Theme.accent.opacity(0.95)), style: StrokeStyle(lineWidth: 3, lineCap: .round, lineJoin: .round))
                    for k in model.wakes { Self.dot(&ctx, at(k), color: wake, big: false) }
                    Self.dot(&ctx, now, color: Theme.accent, big: true)
                }
                ForEach(Array(model.sleeps.enumerated()), id: \.offset) { _, sl in
                    let a = padL + sl.x0 * (w - padL - padR)
                    let b = padL + sl.x1 * (w - padL - padR)
                    if b - a > labelMinW * (typeSize.isAccessibilitySize ? 1.6 : 1) {
                        text(sl.label, x: (a + b) / 2, y: base - bar - 10, anchor: .center, w: w, h: h, color: band, strong: false)
                    }
                }
                if let pill, let p = parkedPt {
                    Text(pill).themeText(.caption, color: Theme.textBody)
                        .padding(.horizontal, 10).padding(.vertical, 3)
                        .background(Capsule().fill(Theme.textSecondary.opacity(0.15)))
                        .fixedSize()
                        .position(x: p.x, y: 12)
                        .frame(width: w, height: h, alignment: .topLeading)
                }
                text(NumberText.fixed(model.start.pct, 1) + "%", x: start.x + (startRight ? -4 : 2), y: sinceIn ? start.y + 20 : max(start.y - 14, 12),
                     anchor: startRight ? .trailing : .leading, w: w, h: h, color: Theme.textSecondary, strong: false)
                text(NumberText.fixed(model.last.pct, 1) + "%", x: valueLeft ? now.x - 10 : now.x + 10, y: now.y + 22,
                     anchor: valueLeft ? .trailing : .leading, w: w, h: h, color: Theme.textPrimary, strong: true)
                text("24 h ago", x: padL, y: h - 8, anchor: .leading, w: w, h: h, color: Theme.textTertiary, strong: false)
                if w >= 240 { text("12 h", x: (padL + w - padR) / 2, y: h - 8, anchor: .center, w: w, h: h, color: Theme.textTertiary, strong: false) }
                text("now", x: w - padR, y: h - 8, anchor: .trailing, w: w, h: h, color: Theme.textTertiary, strong: false)
            }
        }
    }

    private var legend: some View {
        let items: [(Color, String)] =
            (model.sleepCount > 0 ? [(band, "asleep")] : []) + (model.wakeCount > 0 ? [(wake, "woke up")] : [])
        return FlowLayout(spacing: 16) {
            ForEach(items.indices, id: \.self) { i in
                HStack(spacing: 6) {
                    Circle().fill(items[i].0).frame(width: 8, height: 8)
                    Text(items[i].1).themeText(.caption, color: Theme.textSecondary)
                }
            }
        }
        .padding(.top, items.isEmpty ? 0 : 4)
    }

    /// The figures as tiles: three across, one column at accessibility sizes.
    @ViewBuilder
    private var tiles: some View {
        if !model.figures.isEmpty {
            let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .top, spacing: 8))
            layout {
                ForEach(model.figures, id: \.id.rawValue) { f in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(f.value).font(Theme.sans(ThemeTokens.typeSubline, .semibold, relativeTo: .footnote)).monospacedDigit()
                            .foregroundStyle(Theme.textPrimary).lineLimit(2).minimumScaleFactor(0.8)
                        Text(f.tileNote.isEmpty ? f.label : f.tileNote).themeText(.caption, color: Theme.textSecondary).lineLimit(2)
                    }
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .topLeading)
                    .background(RoundedRectangle(cornerRadius: 16).fill(Theme.textSecondary.opacity(0.1)))
                    .accessibilityIdentifier("now-parked-tile-\(f.id.rawValue)")
                }
            }
            .padding(.top, 12)
        }
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

    private static func path(_ pts: [CGPoint], pinned: Set<Int> = []) -> Path {
        var p = Path()
        guard let first = pts.first else { return p }
        p.move(to: first)
        if pts.count == 1 { return p }
        let eased = NowGraphCurve.easeSteps(pts, pinned: pinned)
        p = Path()
        p.move(to: eased.points[0])
        for s in NowGraphCurve.segments(eased.points, hold: eased.hold) { p.addCurve(to: s.end, control1: s.c1, control2: s.c2) }
        return p
    }

    /// A small dot with a soft halo; the big one is the live "now" dot.
    private static func dot(_ ctx: inout GraphicsContext, _ p: CGPoint, color: Color, big: Bool) {
        let halo: CGFloat = big ? 10 : 7
        var layer = ctx
        layer.addFilter(.blur(radius: 3))
        layer.fill(Path(ellipseIn: CGRect(x: p.x - halo, y: p.y - halo, width: halo * 2, height: halo * 2)), with: .color(color.opacity(big ? 0.35 : 0.3)))
        let r: CGFloat = big ? 4.5 : 3
        ctx.fill(Path(ellipseIn: CGRect(x: p.x - r, y: p.y - r, width: r * 2, height: r * 2)), with: .color(color))
    }
}
