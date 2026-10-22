import SwiftUI

/// The small subset of SVG path data the tab bar icons use: M L H V C S A Z (absolute and relative) and the token
/// file's own `circle cx cy r` shorthand. Kept here so the icons come from `tokens.json`, not from a second drawing.
enum SVGPath {
    static func path(_ commands: [String]) -> Path {
        var path = Path()
        for c in commands { path.addPath(single(c)) }
        return path
    }

    static func single(_ d: String) -> Path {
        if d.hasPrefix("circle ") {
            let n = numbers(String(d.dropFirst(7)))
            guard n.count == 3 else { return Path() }
            return Path(ellipseIn: CGRect(x: n[0] - n[2], y: n[1] - n[2], width: n[2] * 2, height: n[2] * 2))
        }
        var p = Path()
        var i = d.startIndex
        var cmd: Character = "M"
        var cur = CGPoint.zero, start = CGPoint.zero
        var lastCubicControl: CGPoint?
        func args(_ count: Int) -> [Double]? {
            let (nums, next) = readNumbers(d, from: i, count: count)
            guard let nums else { return nil }
            i = next
            return nums
        }
        while i < d.endIndex {
            while i < d.endIndex, d[i] == " " || d[i] == "," { i = d.index(after: i) }
            guard i < d.endIndex else { break }
            if d[i].isLetter {
                cmd = d[i]
                i = d.index(after: i)
                if cmd == "Z" || cmd == "z" { p.closeSubpath(); cur = start; lastCubicControl = nil; continue }
            }
            let rel = cmd.isLowercase
            switch cmd.uppercased() {
            case "M":
                guard let a = args(2) else { return p }
                cur = CGPoint(x: a[0] + (rel ? cur.x : 0), y: a[1] + (rel ? cur.y : 0))
                start = cur
                p.move(to: cur)
                cmd = rel ? "l" : "L"  // further pairs after a move are lines
                lastCubicControl = nil
            case "L":
                guard let a = args(2) else { return p }
                cur = CGPoint(x: a[0] + (rel ? cur.x : 0), y: a[1] + (rel ? cur.y : 0))
                p.addLine(to: cur)
                lastCubicControl = nil
            case "H":
                guard let a = args(1) else { return p }
                cur = CGPoint(x: a[0] + (rel ? cur.x : 0), y: cur.y)
                p.addLine(to: cur)
                lastCubicControl = nil
            case "V":
                guard let a = args(1) else { return p }
                cur = CGPoint(x: cur.x, y: a[0] + (rel ? cur.y : 0))
                p.addLine(to: cur)
                lastCubicControl = nil
            case "C":
                guard let a = args(6) else { return p }
                let o = rel ? cur : .zero
                let c1 = CGPoint(x: a[0] + o.x, y: a[1] + o.y), c2 = CGPoint(x: a[2] + o.x, y: a[3] + o.y)
                cur = CGPoint(x: a[4] + o.x, y: a[5] + o.y)
                p.addCurve(to: cur, control1: c1, control2: c2)
                lastCubicControl = c2
            case "S":
                guard let a = args(4) else { return p }
                let o = rel ? cur : .zero
                let c1 = lastCubicControl.map { CGPoint(x: 2 * cur.x - $0.x, y: 2 * cur.y - $0.y) } ?? cur
                let c2 = CGPoint(x: a[0] + o.x, y: a[1] + o.y)
                cur = CGPoint(x: a[2] + o.x, y: a[3] + o.y)
                p.addCurve(to: cur, control1: c1, control2: c2)
                lastCubicControl = c2
            case "A":
                guard let a = args(7) else { return p }
                let end = CGPoint(x: a[5] + (rel ? cur.x : 0), y: a[6] + (rel ? cur.y : 0))
                addArc(&p, from: cur, to: end, rx: a[0], ry: a[1], rotation: a[2], large: a[3] != 0, sweep: a[4] != 0)
                cur = end
                lastCubicControl = nil
            default:
                return p
            }
        }
        return p
    }

    // MARK: Numbers

    private static func numbers(_ s: String) -> [Double] {
        var out: [Double] = []
        var i = s.startIndex
        while i < s.endIndex {
            if let (v, next) = readNumber(s, from: i) { out.append(v); i = next } else { i = s.index(after: i) }
        }
        return out
    }

    private static func readNumbers(_ s: String, from: String.Index, count: Int) -> ([Double]?, String.Index) {
        var out: [Double] = []
        var i = from
        for _ in 0..<count {
            while i < s.endIndex, s[i] == " " || s[i] == "," { i = s.index(after: i) }
            guard let (v, next) = readNumber(s, from: i) else { return (nil, i) }
            out.append(v)
            i = next
        }
        return (out, i)
    }

    /// A number as SVG writes them: "-6", "5.3", ".5", "1e2"; two can run together ("-6-5.3", "1.5.5").
    private static func readNumber(_ s: String, from: String.Index) -> (Double, String.Index)? {
        var i = from
        var text = ""
        if i < s.endIndex, s[i] == "-" || s[i] == "+" { text.append(s[i]); i = s.index(after: i) }
        var sawDot = false, sawDigit = false
        while i < s.endIndex {
            let c = s[i]
            if c.isNumber { sawDigit = true; text.append(c) }
            else if c == ".", !sawDot { sawDot = true; text.append(c) }
            else { break }
            i = s.index(after: i)
        }
        guard sawDigit, let v = Double(text) else { return nil }
        return (v, i)
    }

    // MARK: Arcs (SVG endpoint form to a path)

    private static func addArc(_ p: inout Path, from a: CGPoint, to b: CGPoint, rx rx0: Double, ry ry0: Double,
                               rotation: Double, large: Bool, sweep: Bool) {
        var rx = abs(rx0), ry = abs(ry0)
        guard rx > 0, ry > 0, a != b else { p.addLine(to: b); return }
        let phi = rotation * .pi / 180
        let cosP = cos(phi), sinP = sin(phi)
        let dx = (a.x - b.x) / 2, dy = (a.y - b.y) / 2
        let x1 = cosP * dx + sinP * dy, y1 = -sinP * dx + cosP * dy
        let lambda = (x1 * x1) / (rx * rx) + (y1 * y1) / (ry * ry)
        if lambda > 1 { rx *= lambda.squareRoot(); ry *= lambda.squareRoot() }
        let num = rx * rx * ry * ry - rx * rx * y1 * y1 - ry * ry * x1 * x1
        let den = rx * rx * y1 * y1 + ry * ry * x1 * x1
        var coef = den == 0 ? 0 : (max(0, num / den)).squareRoot()
        if large == sweep { coef = -coef }
        let cxp = coef * (rx * y1 / ry), cyp = coef * -(ry * x1 / rx)
        let cx = cosP * cxp - sinP * cyp + (a.x + b.x) / 2
        let cy = sinP * cxp + cosP * cyp + (a.y + b.y) / 2
        func angle(_ ux: Double, _ uy: Double, _ vx: Double, _ vy: Double) -> Double {
            let dot = ux * vx + uy * vy
            let len = (ux * ux + uy * uy).squareRoot() * (vx * vx + vy * vy).squareRoot()
            var ang = acos(max(-1, min(1, dot / len)))
            if ux * vy - uy * vx < 0 { ang = -ang }
            return ang
        }
        let theta1 = angle(1, 0, (x1 - cxp) / rx, (y1 - cyp) / ry)
        var delta = angle((x1 - cxp) / rx, (y1 - cyp) / ry, (-x1 - cxp) / rx, (-y1 - cyp) / ry)
        if !sweep, delta > 0 { delta -= 2 * .pi }
        if sweep, delta < 0 { delta += 2 * .pi }
        // Split into quarter turns at most and draw each as a cubic.
        let segments = max(1, Int(ceil(abs(delta) / (.pi / 2))))
        let step = delta / Double(segments)
        let t = 4.0 / 3.0 * tan(step / 4)
        var th = theta1
        for _ in 0..<segments {
            let c1 = point(th, rx, ry, cosP, sinP, cx, cy, dTheta: t, sign: 1)
            let thEnd = th + step
            let c2 = point(thEnd, rx, ry, cosP, sinP, cx, cy, dTheta: t, sign: -1)
            let end = point(thEnd, rx, ry, cosP, sinP, cx, cy, dTheta: 0, sign: 0)
            p.addCurve(to: end, control1: c1, control2: c2)
            th = thEnd
        }
    }

    /// The point on the rotated ellipse at angle `th`, optionally moved along its tangent (for a cubic's control).
    private static func point(_ th: Double, _ rx: Double, _ ry: Double, _ cosP: Double, _ sinP: Double,
                              _ cx: Double, _ cy: Double, dTheta: Double, sign: Double) -> CGPoint {
        let x = rx * cos(th) - sign * dTheta * rx * sin(th)
        let y = ry * sin(th) + sign * dTheta * ry * cos(th)
        return CGPoint(x: cosP * x - sinP * y + cx, y: sinP * x + cosP * y + cy)
    }
}

/// A tab bar icon: the token paths in a 24 x 24 box, scaled to the frame. Stroke it (round caps and joins).
struct ThemeIcon: Shape {
    let paths: [String]

    func path(in rect: CGRect) -> Path {
        let s = min(rect.width, rect.height) / 24
        return SVGPath.path(paths).applying(CGAffineTransform(scaleX: s, y: s))
    }
}
