import SwiftUI

/// The Now card's driving graph (design_preview key now_drive_graph), in the soft look of the charging graph's: the speed from the start of the
/// drive as one soft line with a glow, the battery as a quieter second line, a quiet label at the top speed, and, when the truck's navigation has a
/// destination, one line under the header with the distance and the arrival time. Read-only; the figures come from NowDriveGraphModel.
struct NowDriveGraph: View {
    let model: NowDriveGraphModel
    /// "12 mi to Work · arrive 12:05 PM", or nil with no trip.
    var destination: String?

    private let padT: CGFloat = 30
    private let padB: CGFloat = 26
    private let padL: CGFloat = 6
    private let padR: CGFloat = 10

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            header
            if let destination {
                Text(destination).themeText(.subline, color: Theme.textBody).lineLimit(1).truncationMode(.tail)
                    .accessibilityIdentifier("now-drive-dest")
            }
            plot.frame(height: 170)
            legend
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(model.spoken + (destination.map { ". \($0)" } ?? ""))
        .accessibilityIdentifier("now-drive-graph")
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text("This drive").themeText(.label)
            Spacer(minLength: 0)
            if let change = model.change {
                (Text(change).font(Theme.sans(ThemeTokens.typeSubline, .semibold, relativeTo: .footnote)).foregroundColor(Theme.textPrimary)
                    + Text(" \(model.since)").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textSecondary))
                    .monospacedDigit()
                    .accessibilityIdentifier("now-drive-change")
            }
        }
    }

    private var plot: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let h = geo.size.height
            let base = h - padB
            let at = { (p: NowDriveGraphModel.Pt) in CGPoint(x: padL + p.x * (w - padL - padR), y: padT + (1 - p.y) * (base - padT)) }
            let speed = model.speed.map(at)
            let soc = model.soc.map(at)
            let now = at(model.last)
            let labelNow = now.x - padL > 170
            ZStack(alignment: .topLeading) {
                Canvas { ctx, _ in
                    let line = Self.path(speed)
                    var fill = line
                    fill.addLine(to: CGPoint(x: now.x, y: base))
                    fill.addLine(to: CGPoint(x: speed.first?.x ?? 0, y: base))
                    fill.closeSubpath()
                    ctx.fill(fill, with: .linearGradient(Gradient(colors: [Theme.accent.opacity(0.18), Theme.accent.opacity(0)]),
                                                        startPoint: CGPoint(x: 0, y: padT), endPoint: CGPoint(x: 0, y: base)))
                    if soc.count > 1 {
                        ctx.stroke(Self.path(soc), with: .color(Theme.estimate.opacity(0.6)), style: StrokeStyle(lineWidth: 1.8, lineCap: .round, lineJoin: .round))
                    }
                    var glow = ctx
                    glow.addFilter(.blur(radius: 4))
                    glow.stroke(line, with: .color(Theme.accent.opacity(0.5)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    ctx.stroke(line, with: .color(Theme.accent.opacity(0.95)), style: StrokeStyle(lineWidth: 2.6, lineCap: .round, lineJoin: .round))
                    Self.dot(&ctx, now)
                }
                if let top = model.top {
                    let p = at(top.pt)
                    text(top.label, x: min(max(p.x, padL + 24), now.x - 40), y: max(p.y - 8, 12), anchor: .center, w: w, h: h, color: Theme.textTertiary, halo: true)
                        .accessibilityIdentifier("now-drive-top")
                }
                text(w >= 260 ? "Left \(model.startTime)" : model.startTime, x: padL, y: h - 8, anchor: .leading, w: w, h: h, color: Theme.textTertiary)
                if labelNow { text("now", x: now.x, y: h - 8, anchor: .center, w: w, h: h, color: Theme.textTertiary) }
            }
        }
    }

    private var legend: some View {
        FlowLayout(spacing: 16) {
            item(Theme.accent, "Speed (\(model.speedUnit))")
            if !model.soc.isEmpty { item(Theme.estimate, "Battery") }
        }
        .padding(.top, 4)
    }

    private func item(_ color: Color, _ label: String) -> some View {
        HStack(spacing: 6) {
            Capsule().fill(color).frame(width: 14, height: 2)
            Text(label).themeText(.caption, color: Theme.textSecondary)
        }
    }

    /// A short label with its leading, centre or trailing edge at `x`.
    private func text(_ s: String, x: CGFloat, y: CGFloat, anchor: HorizontalAlignment, w: CGFloat, h: CGFloat, color: Color, halo: Bool = false) -> some View {
        let label = Text(s).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).monospacedDigit().foregroundStyle(color).fixedSize()
            // The top label sits over the battery line at times: a ring of the card's own colour keeps it readable (web draws it with a stroke behind the text).
            .shadow(color: halo ? Theme.card : .clear, radius: 1.5).shadow(color: halo ? Theme.card : .clear, radius: 1.5)
        let align: Alignment = anchor == .leading ? .leading : anchor == .trailing ? .trailing : .center
        let box: CGFloat = anchor == .leading ? max(w - x, 1) : anchor == .trailing ? max(x, 1) : w
        let cx: CGFloat = anchor == .leading ? x + box / 2 : anchor == .trailing ? x / 2 : x
        return label.frame(width: box, alignment: align).position(x: cx, y: y)
            .frame(width: w, height: h, alignment: .topLeading)
    }

    private static func path(_ pts: [CGPoint]) -> Path {
        var p = Path()
        guard let first = pts.first else { return p }
        p.move(to: first)
        if pts.count == 1 { return p }
        for s in NowGraphCurve.segments(pts) { p.addCurve(to: s.end, control1: s.c1, control2: s.c2) }
        return p
    }

    private static func dot(_ ctx: inout GraphicsContext, _ p: CGPoint) {
        var layer = ctx
        layer.addFilter(.blur(radius: 3))
        layer.fill(Path(ellipseIn: CGRect(x: p.x - 10, y: p.y - 10, width: 20, height: 20)), with: .color(Theme.accent.opacity(0.35)))
        ctx.fill(Path(ellipseIn: CGRect(x: p.x - 4.5, y: p.y - 4.5, width: 9, height: 9)), with: .color(Theme.accent))
    }
}
