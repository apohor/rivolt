import SwiftUI

/// The Start and End pills over the drive map, as the web's: "Start 9:15 PM" and "End 11:28 PM" beside the pins, on the side with more room,
/// clear of the legend, the credit and each other; one pill for a round trip (the two pins on the same spot). Placement is plain geometry
/// so it can be tested; the pins' screen positions come from the map after it has fitted the route.
enum PinPills {
    static let height: CGFloat = 22
    /// The gap between a pin's centre and the pill's near edge (the pin is 6 pt in radius, 2 pt of ring).
    static let gap: CGFloat = 12
    static let margin: CGFloat = 6
    /// Pins closer than this share one pill.
    static let sharedWithin: CGFloat = 36

    enum Side: Equatable { case right, left, above, below }

    struct Spot: Equatable {
        var side: Side
        /// The pill's rectangle, from an estimated width (the real pill is anchored on its pin side, so a small error never touches the pin).
        var rect: CGRect
        /// The point the pill is anchored on, and how: the pill grows from it.
        var anchor: CGPoint
    }

    /// A rough width for a one-line pill at the map caption size: enough to test collisions, never used to draw.
    static func estimatedWidth(_ text: String) -> CGFloat { CGFloat(text.count) * 6.4 + 22 }

    /// One end of the drive in a pill: its name ("Start", "End") and its time.
    struct Part: Equatable {
        let name: String
        let time: String
    }

    /// "Start 9:15 PM · End 11:28 PM" as its parts (each end's name is the first word).
    static func parts(of text: String) -> [Part] {
        text.components(separatedBy: " · ").compactMap { piece in
            guard let space = piece.firstIndex(of: " ") else { return nil }
            return Part(name: String(piece[..<space]), time: String(piece[piece.index(after: space)...]))
        }
    }

    static func text(start: String?, end: String?, shared: Bool) -> (start: String?, end: String?, both: String?) {
        let s = start.map { "Start \($0)" }, e = end.map { "End \($0)" }
        if shared, let s, let e { return (nil, nil, "\(s) · \(e)") }
        return (s, e, nil)
    }

    /// Where a pill goes for `pin` in a map of `size`: the side with more room first, then the other, then above, then below; the first
    /// whose rectangle is inside the map (6 pt in) and clear of `avoiding`.
    static func place(pin: CGPoint, width w: CGFloat, in size: CGSize, avoiding: [CGRect], line: [CGPoint] = []) -> Spot {
        let bounds = CGRect(origin: .zero, size: size).insetBy(dx: margin, dy: margin)
        let h = height
        let rightRoom = size.width - pin.x, leftRoom = pin.x
        func candidate(_ side: Side) -> Spot {
            switch side {
            case .right:
                let r = CGRect(x: pin.x + gap, y: pin.y - h / 2, width: w, height: h)
                return Spot(side: side, rect: r, anchor: CGPoint(x: r.minX, y: r.midY))
            case .left:
                let r = CGRect(x: pin.x - gap - w, y: pin.y - h / 2, width: w, height: h)
                return Spot(side: side, rect: r, anchor: CGPoint(x: r.maxX, y: r.midY))
            case .above:
                let r = CGRect(x: pin.x - w / 2, y: pin.y - gap - h, width: w, height: h)
                return Spot(side: side, rect: r, anchor: CGPoint(x: r.midX, y: r.midY))
            case .below:
                let r = CGRect(x: pin.x - w / 2, y: pin.y + gap, width: w, height: h)
                return Spot(side: side, rect: r, anchor: CGPoint(x: r.midX, y: r.midY))
            }
        }
        let order: [Side] = (rightRoom >= leftRoom ? [.right, .left] : [.left, .right]) + [.above, .below]
        // A side that fits as it is, and is clear of `avoiding`, wins outright.
        func clear(_ r: CGRect) -> Bool { !avoiding.contains { $0.intersects(r) } && !touches(line, r) }
        if let fits = order.map(candidate).first(where: { s in bounds.contains(s.rect) && clear(s.rect) }) { return fits }
        // Else the first side that is clear once nudged inside the map (a pin near an edge), else the roomier side nudged, rather than no pill.
        func nudged(_ side: Side) -> Spot {
            var s = candidate(side)
            var dx = -max(0, s.rect.maxX - bounds.maxX)
            dx += max(0, bounds.minX - (s.rect.minX + dx))
            var dy = -max(0, s.rect.maxY - bounds.maxY)
            dy += max(0, bounds.minY - (s.rect.minY + dy))
            s.rect = s.rect.offsetBy(dx: dx, dy: dy)
            s.anchor = CGPoint(x: s.anchor.x + dx, y: s.anchor.y + dy)
            return s
        }
        return order.map(nudged).first { clear($0.rect) } ?? order.map(nudged).first { s in !avoiding.contains { $0.intersects(s.rect) } } ?? nudged(order[0])
    }

    /// Whether the polyline `line` (the route's first stretch from a pin, in screen points) runs through `rect` (a 3 pt cushion), so a pill
    /// is never laid along the line.
    static func touches(_ line: [CGPoint], _ rect: CGRect) -> Bool {
        let r = rect.insetBy(dx: -3, dy: -3)
        for (a, b) in zip(line, line.dropFirst()) {
            let steps = max(1, Int(hypot(b.x - a.x, b.y - a.y) / 4))
            for k in 0...steps {
                let t = CGFloat(k) / CGFloat(steps)
                if r.contains(CGPoint(x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t)) { return true }
            }
        }
        return false
    }

    /// The corners other things sit in: the legend (bottom left) and the credit (bottom right).
    static func keepClear(size: CGSize, legendWidth: CGFloat = 190, legendHeight: CGFloat = 52, legend: Bool) -> [CGRect] {
        var r = [CGRect(x: size.width - 130, y: size.height - 26, width: 130, height: 26)]
        if legend { r.append(CGRect(x: 0, y: size.height - legendHeight, width: legendWidth, height: legendHeight)) }
        return r
    }
}

/// A pill as quiet as the map's other captions: the end's name in the secondary colour, its time in the primary one with digits that keep
/// their width, medium weight, no dot (the pin beside it already has the colour). It stops growing with Dynamic Type at xxxLarge.
struct PinPill: View {
    let parts: [PinPills.Part]
    let label: String

    var body: some View {
        HStack(spacing: 6) {
            ForEach(Array(parts.enumerated()), id: \.offset) { i, part in
                if i > 0 { Text("·").foregroundStyle(Theme.textTertiary) }
                HStack(spacing: 3) {
                    Text(part.name).foregroundStyle(Theme.textSecondary)
                    Text(part.time).monospacedDigit().foregroundStyle(Theme.textPrimary)
                }
            }
        }
        .lineLimit(1).fixedSize()
        .font(Theme.sans(ThemeTokens.typeMapCaption, .medium, relativeTo: .caption2))
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .padding(.horizontal, 8).frame(height: PinPills.height)
        .background(Theme.background.opacity(0.72), in: Capsule())
        .overlay(Capsule().strokeBorder(Theme.border.opacity(0.7), lineWidth: 1))
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
    }
}

/// The pills over a map of `size`, from the pins' positions in it (and the first stretch of the route from each, so no pill lies along it).
struct PinPillsOverlay: View {
    let size: CGSize
    let start: CGPoint?
    let end: CGPoint?
    let startTime: String?
    let endTime: String?
    let legend: Bool
    var legendHeight: CGFloat = 52
    var startLine: [CGPoint] = []
    var endLine: [CGPoint] = []
    /// The pills' real widths once drawn (the estimate stands until then, and at any text size): placement uses them.
    @State private var widths: [Int: CGFloat] = [:]

    struct Item: Identifiable {
        let id: Int
        let text: String
        let spot: PinPills.Spot
        /// The pill's words: "Start" or "End" (quiet) and its time (the loud part), one pair per end shown.
        var parts: [PinPills.Part] { PinPills.parts(of: text) }
    }

    /// The pills to draw: one for a round trip, else a Start and an End placed so that they keep clear of the corners, the line and each other.
    static func items(size: CGSize, start: CGPoint?, end: CGPoint?, startTime: String?, endTime: String?, legend: Bool, legendHeight: CGFloat = 52,
                      startLine: [CGPoint] = [], endLine: [CGPoint] = [], widths: [Int: CGFloat] = [:]) -> [Item] {
        let shared = start.flatMap { s in end.map { hypot(s.x - $0.x, s.y - $0.y) < PinPills.sharedWithin } } ?? false
        let t = PinPills.text(start: startTime, end: endTime, shared: shared)
        var taken = PinPills.keepClear(size: size, legendHeight: legendHeight, legend: legend)
        var out: [Item] = []
        func add(_ id: Int, _ text: String?, _ pin: CGPoint?, _ line: [CGPoint]) {
            guard let text, let pin else { return }
            let spot = PinPills.place(pin: pin, width: widths[id] ?? PinPills.estimatedWidth(text), in: size, avoiding: taken, line: line)
            taken.append(spot.rect)
            out.append(Item(id: id, text: text, spot: spot))
        }
        if let both = t.both { add(0, both, start, startLine) } else {
            add(1, t.start, start, startLine)
            add(2, t.end, end, endLine)
        }
        return out
    }

    var body: some View {
        let items = Self.items(size: size, start: start, end: end, startTime: startTime, endTime: endTime, legend: legend, legendHeight: legendHeight,
                               startLine: startLine, endLine: endLine, widths: widths)
        ZStack(alignment: .topLeading) {
            ForEach(items) { item in
                let alignment: Alignment = item.spot.side == .right ? .leading : item.spot.side == .left ? .trailing : .center
                Color.clear.frame(width: 0, height: 0)
                    .overlay(alignment: alignment) {
                        PinPill(parts: item.parts, label: item.text)
                            .background(GeometryReader { g in Color.clear.preference(key: PillWidthKey.self, value: [item.id: g.size.width]) })
                    }
                    .position(x: item.spot.anchor.x, y: item.spot.anchor.y)
            }
        }
        .frame(width: size.width, height: size.height, alignment: .topLeading)
        .onPreferenceChange(PillWidthKey.self) { new in
            // Only a real change (a width off by a point or more) re-places the pills, so this settles after one pass.
            if new.contains(where: { abs((widths[$0.key] ?? 0) - $0.value) >= 1 }) { widths.merge(new) { $1 } }
        }
        .allowsHitTesting(false)
    }
}

private struct PillWidthKey: PreferenceKey {
    static let defaultValue: [Int: CGFloat] = [:]
    static func reduce(value: inout [Int: CGFloat], nextValue: () -> [Int: CGFloat]) { value.merge(nextValue()) { $1 } }
}
