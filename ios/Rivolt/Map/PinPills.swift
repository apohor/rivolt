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

    static func text(start: String?, end: String?, shared: Bool) -> (start: String?, end: String?, both: String?) {
        let s = start.map { "Start \($0)" }, e = end.map { "End \($0)" }
        if shared, let s, let e { return (nil, nil, "\(s) · \(e)") }
        return (s, e, nil)
    }

    /// Where a pill goes for `pin` in a map of `size`: the side with more room first, then the other, then above, then below; the first
    /// whose rectangle is inside the map (6 pt in) and clear of `avoiding`.
    static func place(pin: CGPoint, width w: CGFloat, in size: CGSize, avoiding: [CGRect]) -> Spot {
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
        let fits = order.map(candidate).first { s in bounds.contains(s.rect) && !avoiding.contains { $0.intersects(s.rect) } }
        // Nothing fits (a tiny map): the roomier side, nudged inside, rather than no pill.
        guard let fits else {
            var s = candidate(order[0])
            let dx = max(0, bounds.minX - s.rect.minX) - max(0, s.rect.maxX - bounds.maxX)
            let dy = max(0, bounds.minY - s.rect.minY) - max(0, s.rect.maxY - bounds.maxY)
            s.rect = s.rect.offsetBy(dx: dx, dy: dy)
            s.anchor = CGPoint(x: s.anchor.x + dx, y: s.anchor.y + dy)
            return s
        }
        return fits
    }

    /// The corners other things sit in: the legend (bottom left) and the credit (bottom right).
    static func keepClear(size: CGSize, legendWidth: CGFloat = 190, legend: Bool) -> [CGRect] {
        var r = [CGRect(x: size.width - 130, y: size.height - 26, width: 130, height: 26)]
        if legend { r.append(CGRect(x: 0, y: size.height - 52, width: legendWidth, height: 52)) }
        return r
    }
}

struct PinPill: View {
    let text: String
    let dot: Color

    var body: some View {
        HStack(spacing: 4) {
            Circle().fill(dot).frame(width: 7, height: 7)
            Text(text).lineLimit(1).fixedSize()
        }
        .mapCaption()
        .fontWeight(.semibold)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .foregroundStyle(Theme.textPrimary)
        .padding(.horizontal, 8).frame(height: PinPills.height)
        .background(Theme.background.opacity(0.85), in: Capsule())
        .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(text)
    }
}

/// The pills over a map of `size`, from the pins' positions in it.
struct PinPillsOverlay: View {
    let size: CGSize
    let start: CGPoint?
    let end: CGPoint?
    let startTime: String?
    let endTime: String?
    let legend: Bool

    struct Item: Identifiable {
        let id: Int
        let text: String
        let dot: Color
        let spot: PinPills.Spot
    }

    /// The pills to draw: one for a round trip, else a Start and an End placed so that they keep clear of the corners and of each other.
    static func items(size: CGSize, start: CGPoint?, end: CGPoint?, startTime: String?, endTime: String?, legend: Bool) -> [Item] {
        let shared = start.flatMap { s in end.map { hypot(s.x - $0.x, s.y - $0.y) < PinPills.sharedWithin } } ?? false
        let t = PinPills.text(start: startTime, end: endTime, shared: shared)
        var taken = PinPills.keepClear(size: size, legend: legend)
        var out: [Item] = []
        func add(_ id: Int, _ text: String?, _ pin: CGPoint?, _ dot: Color) {
            guard let text, let pin else { return }
            let spot = PinPills.place(pin: pin, width: PinPills.estimatedWidth(text), in: size, avoiding: taken)
            taken.append(spot.rect)
            out.append(Item(id: id, text: text, dot: dot, spot: spot))
        }
        if let both = t.both { add(0, both, start, Theme.routeEnd) } else {
            add(1, t.start, start, Theme.routeStart)
            add(2, t.end, end, Theme.routeEnd)
        }
        return out
    }

    var body: some View {
        ZStack(alignment: .topLeading) {
            ForEach(Self.items(size: size, start: start, end: end, startTime: startTime, endTime: endTime, legend: legend)) { item in
                let alignment: Alignment = item.spot.side == .right ? .leading : item.spot.side == .left ? .trailing : .center
                Color.clear.frame(width: 0, height: 0)
                    .overlay(alignment: alignment) { PinPill(text: item.text, dot: item.dot) }
                    .position(x: item.spot.anchor.x, y: item.spot.anchor.y)
            }
        }
        .frame(width: size.width, height: size.height, alignment: .topLeading)
        .allowsHitTesting(false)
    }
}
