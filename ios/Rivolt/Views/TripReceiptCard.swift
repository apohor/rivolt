import SwiftUI

/// Told how tall the receipt came out, so the PNG is cut to exactly that (the web's `paintReceipt` measures the same way).
final class ReceiptProbe {
    var height: CGFloat?
}

/// The trip receipt (the web's receiptCard.ts): a till receipt on a dark ground, as tall as it needs to be. System fonts only, and the PNG
/// carries no metadata text.
struct TripReceiptCanvas: View {
    let model: TripReceiptModel
    /// The canvas height. The first pass is tall and a probe learns the paper's height; the second is cut to it.
    var height: CGFloat = 8000
    var probe: ReceiptProbe?

    static let width: CGFloat = 1080
    private static let pad: CGFloat = 80
    private static let edge: CGFloat = 36
    private static let ground = Color(red: 10 / 255, green: 10 / 255, blue: 10 / 255)
    private static let paper = Color(red: 245 / 255, green: 242 / 255, blue: 234 / 255)
    private static let ink = Color(red: 23 / 255, green: 23 / 255, blue: 23 / 255)
    private static let muted = Color(red: 107 / 255, green: 107 / 255, blue: 98 / 255)
    private static let rule = Color(red: 168 / 255, green: 163 / 255, blue: 149 / 255)

    var body: some View {
        Canvas { ctx, _ in
            let paperH = Self.draw(ctx, model, canvasHeight: height)
            probe?.height = paperH
            // The bottom edge: ground below the paper, with a scallop of ground bites into it.
            ctx.fill(Path(CGRect(x: 0, y: paperH, width: Self.width, height: Self.edge + 20)), with: .color(Self.ground))
            var x = Self.edge + 18
            while x < Self.width - Self.edge {
                ctx.fill(Path(ellipseIn: CGRect(x: x - 14, y: paperH - 14, width: 28, height: 28)), with: .color(Self.ground))
                x += 36
            }
        }
        .frame(width: Self.width, height: height)
        .environment(\.colorScheme, .light)
        .accessibilityHidden(true)
    }

    private enum Face { case mono, sans }

    private static func resolved(_ ctx: GraphicsContext, _ s: String, _ size: CGFloat, _ weight: Font.Weight, _ face: Face, _ color: Color) -> GraphicsContext.ResolvedText {
        ctx.resolve(Text(s).font(.system(size: size, weight: weight, design: face == .mono ? .monospaced : .default)).foregroundColor(color))
    }

    private static func measure(_ ctx: GraphicsContext, _ s: String, _ size: CGFloat, _ weight: Font.Weight, _ face: Face) -> CGFloat {
        resolved(ctx, s, size, weight, face, .black).measure(in: CGSize(width: CGFloat.infinity, height: CGFloat.infinity)).width
    }

    private enum Align { case leading, trailing, center }

    private static func text(_ ctx: GraphicsContext, _ s: String, _ size: CGFloat, _ weight: Font.Weight, _ face: Face, _ color: Color,
                             x: CGFloat, y: CGFloat, align: Align = .leading) {
        let r = resolved(ctx, s, size, weight, face, color)
        let m = r.measure(in: CGSize(width: CGFloat.infinity, height: CGFloat.infinity))
        let left = align == .leading ? x : align == .trailing ? x - m.width : x - m.width / 2
        ctx.draw(r, at: CGPoint(x: left, y: y - r.firstBaseline(in: m)), anchor: .topLeading)
    }

    private static func clip(_ ctx: GraphicsContext, _ s: String, _ size: CGFloat, _ weight: Font.Weight, _ face: Face, max: CGFloat) -> String {
        if measure(ctx, s, size, weight, face) <= max { return s }
        var t = s
        while t.count > 1 && measure(ctx, t + "…", size, weight, face) > max { t.removeLast() }
        return t + "…"
    }

    /// Breaks text at spaces into lines no wider than max, so a long place name moves to the next line instead of being cut off.
    private static func wrap(_ ctx: GraphicsContext, _ s: String, _ size: CGFloat, _ weight: Font.Weight, _ face: Face, max: CGFloat) -> [String] {
        var out: [String] = []
        var line = ""
        for word in s.split(separator: " ", omittingEmptySubsequences: true).map(String.init) {
            let next = line.isEmpty ? word : "\(line) \(word)"
            if !line.isEmpty && measure(ctx, next, size, weight, face) > max {
                out.append(line)
                line = word
            } else {
                line = next
            }
        }
        if !line.isEmpty { out.append(line) }
        return out.isEmpty ? [""] : out
    }

    private static func rule(_ ctx: GraphicsContext, y: CGFloat, from: CGFloat, to: CGFloat, dotted: Bool = false) {
        var p = Path()
        p.move(to: CGPoint(x: from, y: y))
        p.addLine(to: CGPoint(x: to, y: y))
        ctx.stroke(p, with: .color(rule), style: StrokeStyle(lineWidth: 3, dash: dotted ? [2, 8] : [14, 10]))
    }

    /// The paths fitted into the box (equirectangular, so a mile north is as long as a mile east), each its own line so the cut-off stretches are
    /// never joined. Bare: no marker or label.
    private static func drawRoute(_ ctx: GraphicsContext, _ paths: [[DriveRoute.Point]], x: CGFloat, y: CGFloat, w: CGFloat, h: CGFloat) {
        let all = paths.flatMap { $0 }
        guard let minLat = all.map(\.lat).min(), let maxLat = all.map(\.lat).max(),
              let minLon = all.map(\.lon).min(), let maxLon = all.map(\.lon).max() else { return }
        let kx = cos((minLat + maxLat) / 2 * .pi / 180)
        let spanX = max((maxLon - minLon) * kx, 1e-6)
        let spanY = max(maxLat - minLat, 1e-6)
        let k = min(w / spanX, h / spanY)
        let ox = x + (w - spanX * k) / 2
        let oy = y + (h - spanY * k) / 2
        for p in paths {
            var line = Path()
            for (i, pt) in p.enumerated() {
                let q = CGPoint(x: ox + (pt.lon - minLon) * kx * k, y: oy + (maxLat - pt.lat) * k)
                if i == 0 { line.move(to: q) } else { line.addLine(to: q) }
            }
            ctx.stroke(line, with: .color(ink), style: StrokeStyle(lineWidth: 7, lineCap: .round, lineJoin: .round))
        }
    }

    /// Lays the receipt out from the top and returns where the paper ends.
    private static func draw(_ ctx: GraphicsContext, _ m: TripReceiptModel, canvasHeight: CGFloat) -> CGFloat {
        let l = edge + pad
        let r = width - edge - pad
        let inner = r - l
        ctx.fill(Path(CGRect(x: 0, y: 0, width: width, height: canvasHeight)), with: .color(ground))
        ctx.fill(Path(CGRect(x: edge, y: edge, width: width - 2 * edge, height: canvasHeight)), with: .color(paper))
        var y = edge + 100
        text(ctx, m.head.0, 30, .bold, .mono, ink, x: l, y: y)
        text(ctx, m.head.1, 30, .bold, .mono, ink, x: r, y: y, align: .trailing)
        y += 90
        var size: CGFloat = 64
        while size > 34 && measure(ctx, m.title, size, .heavy, .sans) > inner { size -= 4 }
        text(ctx, m.title, size, .heavy, .sans, ink, x: l, y: y)
        y += 50
        text(ctx, clip(ctx, m.sub, 28, .regular, .mono, max: inner), 28, .regular, .mono, muted, x: l, y: y)
        y += 40
        let paths = (m.sketch ?? []).map { DriveRoute.decode($0, precision: 6) }.filter { $0.count >= 2 }
        if !paths.isEmpty {
            drawRoute(ctx, paths, x: l, y: y + 10, w: inner, h: 200)
            y += 240
        } else {
            y += 20
        }
        rule(ctx, y: y, from: l, to: r)
        y += 60
        for line in m.lines {
            text(ctx, line.right, 32, .bold, .mono, ink, x: r, y: y, align: .trailing)
            let rw = measure(ctx, line.right, 32, .bold, .mono)
            text(ctx, clip(ctx, line.title, 32, .bold, .mono, max: inner - rw - 24), 32, .bold, .mono, ink, x: l, y: y)
            y += 40
            if !line.sub.isEmpty || !line.subRight.isEmpty {
                text(ctx, line.subRight, 27, .regular, .mono, muted, x: r, y: y, align: .trailing)
                let sw = measure(ctx, line.subRight, 27, .regular, .mono)
                let wrapped = wrap(ctx, line.sub, 27, .regular, .mono, max: inner - sw - 24)
                for (i, t) in wrapped.enumerated() {
                    text(ctx, clip(ctx, t, 27, .regular, .mono, max: inner - sw - 24), 27, .regular, .mono, muted, x: l, y: y + CGFloat(i) * 36)
                }
                y += 40 + CGFloat(wrapped.count - 1) * 36
            }
            y += 22
        }
        rule(ctx, y: y - 6, from: l, to: r)
        y += 50
        for t in m.totals {
            let lw = measure(ctx, t.label, 32, .regular, .mono)
            let vw = measure(ctx, t.value, 32, .bold, .mono)
            // The dotted leader sits between the label and the value.
            rule(ctx, y: y - 8, from: l + lw + 12, to: r - vw - 12, dotted: true)
            text(ctx, t.label, 32, .regular, .mono, ink, x: l, y: y)
            text(ctx, t.value, 32, .bold, .mono, ink, x: r, y: y, align: .trailing)
            y += 52
        }
        if let c = m.cost {
            text(ctx, c.label, 32, .regular, .mono, ink, x: l, y: y)
            text(ctx, c.value, 32, .bold, .mono, ink, x: r, y: y, align: .trailing)
            y += 52
        }
        y += 4
        rule(ctx, y: y, from: l, to: r)
        y += 90
        text(ctx, m.big.0, 68, .heavy, .sans, ink, x: l, y: y)
        text(ctx, m.big.1, 68, .heavy, .sans, ink, x: r, y: y, align: .trailing)
        y += 24
        rule(ctx, y: y + 10, from: l, to: r)
        y += 80
        for line in m.footer {
            text(ctx, line, 26, .regular, .mono, muted, x: width / 2, y: y, align: .center)
            y += 38
        }
        return y + 20
    }

    /// The receipt as a PNG, made on the phone: one tall pass to find where the paper ends, then one cut to it.
    @MainActor
    static func png(_ model: TripReceiptModel) -> Data? {
        let probe = ReceiptProbe()
        let first = ImageRenderer(content: TripReceiptCanvas(model: model, probe: probe))
        first.scale = 1
        _ = first.uiImage
        guard let paperH = probe.height else { return nil }
        let r = ImageRenderer(content: TripReceiptCanvas(model: model, height: paperH + edge))
        r.scale = 1
        return r.uiImage?.pngData()
    }
}
