import SwiftUI

/// A sheet over the bottom of a full-height map with three resting heights: a peek (the title and one summary line), half the screen, and the details
/// alone (fully open). A tap on the peek opens the details, and the Map button closes them down to the peek; the grabber steps through the three.
/// An upright drag moves the sheet until it is fully open, and only then does the content scroll (fully open with the content at its top, a drag down
/// moves the sheet again). A sideways drag (the day chips) and a tap never move it, and nothing done on the map moves it. For VoiceOver the grabber is
/// one adjustable control (swipe up or down), and Reduce Motion turns the springs into plain moves.
/// The three resting heights of the itinerary sheet.
enum SheetDetent: Int, CaseIterable, Equatable, Comparable {
    case peek, half, full

    var spoken: String {
        switch self {
        case .peek: return "peek"
        case .half: return "half"
        case .full: return "full"
        }
    }

    static func < (a: SheetDetent, b: SheetDetent) -> Bool { a.rawValue < b.rawValue }
}

/// The sheet's heights and how a drag ends, as plain numbers (heights in points, speeds in points per second, a positive `raised` is up).
enum SheetLayout {
    /// The peek may not take more than this share of the map, whatever its text needs.
    static let peekCap: CGFloat = 0.35
    /// A drag moves the sheet one step further than the last height it passed once it has gone this share of the way to the next one, or at
    /// `flickSpeed` (0.5 pt/ms) or faster.
    static let stepShare: CGFloat = 1.0 / 3.0
    static let flickSpeed: CGFloat = 500

    /// What half needs for `measured` points of the content's top: with the divider and the content's top padding above it, 0 for none.
    static func lead(_ measured: CGFloat) -> CGFloat { measured > 0 ? measured + 1 + 3 * Theme.unit : 0 }

    /// `peekNeed` is what the peek (grabber, title, one summary line) measured, `headerNeed` the whole header (with Edit, the day chips, Networks): the
    /// peek is its own height up to 35 % of the container, half is never shorter than the header (up to 65 %), and full leaves only a sliver of map.
    /// `contentLead` is what of the content's top half must show too (at large text the summary, Edit and the day chips are there, not in the header).
    /// The detent stays the same when the container changes (rotation, a resize); only its height follows.
    static func height(_ d: SheetDetent, in container: CGFloat, peekNeed: CGFloat = 0, headerNeed: CGFloat = 0, contentLead: CGFloat = 0) -> CGFloat {
        let peek = peekNeed > 0 ? min(peekNeed, container * peekCap) : min(150, container * 0.3)
        switch d {
        case .peek: return peek
        case .half: return max(container * 0.46, min(headerNeed + contentLead, container * 0.65), peek + 1)
        case .full: return container - 8
        }
    }

    /// The detent a released drag settles on. The sheet goes to the last height the drag passed, and one step further when it went a third of the way
    /// to the next or was flicked that way: a medium flick from the peek opens half, not the details.
    static func settle(from: SheetDetent, raised: CGFloat, velocity: CGFloat, container: CGFloat, peekNeed: CGFloat = 0, headerNeed: CGFloat = 0, contentLead: CGFloat = 0) -> SheetDetent {
        let all = SheetDetent.allCases
        let heights = all.map { height($0, in: container, peekNeed: peekNeed, headerNeed: headerNeed, contentLead: contentLead) }
        let flick = abs(velocity) >= flickSpeed
        let up = flick ? velocity > 0 : raised >= 0
        guard flick || abs(raised) > 0.5, var at = all.firstIndex(of: from) else { return from }
        let pos = heights[at] + raised
        let step = up ? 1 : -1
        // The heights the drag passed, in the direction it was going.
        while all.indices.contains(at + step), up ? pos >= heights[at + step] : pos <= heights[at + step] { at += step }
        guard all.indices.contains(at + step) else { return all[at] }
        let gap = abs(heights[at + step] - heights[at])
        let past = up ? pos - heights[at] : heights[at] - pos
        let flickedThatWay = flick && (velocity > 0) == up
        return past >= gap * stepShare || flickedThatWay ? all[at + step] : all[at]
    }

    /// Whether a drag that has gone `dx`, `dy` is upright (it may move the sheet) rather than sideways (a day chip scroll): steeper than 45°.
    static func isUpright(dx: CGFloat, dy: CGFloat) -> Bool { abs(dy) > abs(dx) }

    /// Whether an upright drag moves the sheet or scrolls its content: below the detents where the content scrolls it is always the sheet; where it
    /// scrolls, only a drag down with the content at its top (the sheet closes, as the content has nowhere further up to go).
    static func dragMovesSheet(contentScrolls: Bool, contentAtTop: Bool, raised: CGFloat) -> Bool {
        !contentScrolls || (contentAtTop && raised < 0)
    }
}

struct DetentSheet<Peek: View, Header: View, Above: View, Content: View>: View {
    @Binding var detent: SheetDetent
    /// The height the sheet's detents are fractions of (the map area).
    let container: CGFloat
    /// Whether the content scrolls (false renders it flat, for a snapshot).
    var scrolls = true
    /// What the peek and the whole header need (see `SheetLayout.height`), measured here and told to the owner, which passes them back.
    var peekNeed: CGFloat = 0
    var headerNeed: CGFloat = 0
    /// What of the content's top half must show (see `SheetLayout.height`): its height as the owner measured it, 0 for none.
    var contentLead: CGFloat = 0
    /// What covers the bottom of the sheet (the tab bar, the home indicator): the peek's summary sits above it.
    var bottomSafe: CGFloat = 0
    /// Large text: the header is only a title and the rest is in the content, so the content scrolls at every height, not only fully open.
    var scrollsAtEveryDetent = false
    var onNeeds: (_ peek: CGFloat, _ header: CGFloat) -> Void = { _, _ in }
    /// What rides on the sheet's top edge, over the map (the radar strip, the map credit): measured and told to the owner; hidden fully open.
    var onAboveHeight: (CGFloat) -> Void = { _ in }
    /// The sheet's title and one-line summary (with Edit beside them): all the peek shows.
    @ViewBuilder var peek: () -> Peek
    /// The rest of the header under the peek (the day chips, Networks).
    @ViewBuilder var header: () -> Header
    @ViewBuilder var above: () -> Above
    @ViewBuilder var content: () -> Content

    @GestureState private var drag: CGFloat = 0
    /// True while a finger is down on the sheet. A gesture state, so it falls back to false when the drag ends and also when it is cancelled
    /// (a call coming in, the app going to the background), which onEnded never hears about: that clears dragMoves.
    @GestureState private var dragging = false
    /// What the drag under way does, decided once it has gone past the slop: nil before, then whether it moves the sheet.
    @State private var dragMoves: Bool?
    @State private var contentAtTop = true
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var contentScrolls: Bool { detent == .full || scrollsAtEveryDetent }
    private func height(_ d: SheetDetent) -> CGFloat {
        SheetLayout.height(d, in: container, peekNeed: peekNeed, headerNeed: headerNeed, contentLead: SheetLayout.lead(contentLead))
    }

    private func move(_ d: SheetDetent) {
        if reduceMotion { detent = d } else { withAnimation(.spring(response: 0.32, dampingFraction: 0.9)) { detent = d } }
    }

    var body: some View {
        let shown = min(height(.full), max(height(.peek) * 0.8, height(detent) + drag))
        VStack(spacing: 0) {
            if detent != .full {
                above()
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { onAboveHeight($0) }
            }
            card.frame(height: shown, alignment: .top)
        }
        .frame(maxWidth: .infinity)
        .frame(maxHeight: .infinity, alignment: .bottom)
        .onChange(of: dragging) { _, now in if !now { dragMoves = nil } }
    }

    private var card: some View {
        VStack(spacing: 0) {
            VStack(spacing: 0) {
                VStack(spacing: 0) {
                    handle
                    peek().padding(.horizontal, 4 * Theme.unit).padding(.bottom, 8)
                }
                .background(GeometryReader { g in Color.clear.preference(key: PeekNeedKey.self, value: g.size.height + max(bottomSafe, 12)) })
                // Under the peek's edge at the peek (the tab bar is see-through): out of sight, and out of reach.
                header().padding(.horizontal, 4 * Theme.unit).padding(.bottom, 8)
                    .opacity(detent == .peek ? 0 : 1).allowsHitTesting(detent != .peek)
                    .accessibilityHidden(detent == .peek)
            }
            // Plus what covers the sheet's bottom, as for the peek: at half the tab bar sat over the header's last row (#1886).
            .background(GeometryReader { g in Color.clear.preference(key: HeaderNeedKey.self, value: g.size.height + 1 + max(bottomSafe, 12)) })
            .onPreferenceChange(PeekNeedKey.self) { onNeeds($0, headerNeed) }
            .onPreferenceChange(HeaderNeedKey.self) { onNeeds(peekNeed, $0) }
            .contentShape(Rectangle())
            // A tap on the peek (its title or summary, not Edit or a chip, which are buttons of their own) opens the details.
            .onTapGesture { if detent != .full { move(.full) } }
            .simultaneousGesture(dragGesture(overContent: false))
            Divider().overlay(Theme.border)
            Group {
                if scrolls {
                    ScrollView {
                        content().padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit)
                            // Not a preference from a GeometryReader: that one is not sent again as the content scrolls, so the sheet took a
                            // drag down in the middle of the details as its own.
                            .onGeometryChange(for: Bool.self) { $0.frame(in: .named(Self.scrollSpace)).minY >= -1 } action: { contentAtTop = $0 }
                    }
                    .coordinateSpace(name: Self.scrollSpace)
                    // Not while the sheet follows the finger: a drag down from the top would also pull the content past its top.
                    .scrollDisabled(!contentScrolls || dragMoves == true)
                } else {
                    content().padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit)
                }
            }
            // Out of sight under the tab bar at the peek, so out of VoiceOver's reach too.
            .accessibilityHidden(detent == .peek)
            .simultaneousGesture(dragGesture(overContent: true))
        }
        .background(Theme.card, in: UnevenRoundedRectangle(topLeadingRadius: 20, topTrailingRadius: 20, style: .continuous))
        .overlay(UnevenRoundedRectangle(topLeadingRadius: 20, topTrailingRadius: 20, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 12, y: -2)
    }

    private static var scrollSpace: String { "detent-sheet-scroll" }

    /// The grabber (a 44 pt row, the whole width, a tap steps peek → half → full → peek) and, above the peek, the Map button that closes the details.
    private func stepGrabber() { move(SheetDetent(rawValue: (detent.rawValue + 1) % SheetDetent.allCases.count) ?? .half) }

    private var handle: some View {
        ZStack {
            // A tap, not a Button: the sheet grows under the finger, so a Button would still be under it when a drag up lets go, and step the sheet
            // on from where the drag left it.
            Capsule().fill(Theme.borderStrong).frame(width: 40, height: 5).frame(maxWidth: .infinity, minHeight: 44)
                .contentShape(Rectangle())
                .onTapGesture { stepGrabber() }
                .accessibilityElement()
                .accessibilityAddTraits(.isButton)
                .accessibilityAction { stepGrabber() }
                .accessibilityIdentifier("plan-sheet-grabber")
            .accessibilityLabel("Itinerary")
            .accessibilityValue(detent.spoken)
            .accessibilityHint("Swipe up or down to open or close")
            .accessibilityAdjustableAction { direction in
                switch direction {
                case .increment: if let d = SheetDetent(rawValue: detent.rawValue + 1) { move(d) }
                case .decrement: if let d = SheetDetent(rawValue: detent.rawValue - 1) { move(d) }
                @unknown default: break
                }
            }
            if detent != .peek {
                Button { move(.peek) } label: {
                    Label("Map", systemImage: "map").themeText(.subline, color: Theme.textPrimary)
                        .padding(.horizontal, 12).frame(minHeight: 36)
                        .background(Theme.background, in: Capsule())
                        .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
                        .frame(minHeight: 44)
                }
                .buttonStyle(.plain)
                .frame(maxWidth: .infinity, alignment: .trailing)
                .padding(.trailing, 4 * Theme.unit)
                .accessibilityIdentifier("plan-sheet-map")
                .accessibilityHint("Shows the map, with the trip's title below it")
            }
        }
        .padding(.top, 2)
    }

    /// A drag on the header always may move the sheet; one on the content only where the content does not scroll, or down from its top.
    private func dragGesture(overContent: Bool) -> some Gesture {
        // In the window's space: the sheet grows under the finger, so its own space would move with it and halve the drag.
        DragGesture(minimumDistance: 10, coordinateSpace: .global)
            .updating($drag) { v, state, _ in
                guard dragMoves == true else { return }
                state = -v.translation.height
            }
            .updating($dragging) { _, state, _ in state = true }
            .onChanged { v in
                guard dragMoves == nil else { return }
                let raised = -v.translation.height
                dragMoves = SheetLayout.isUpright(dx: v.translation.width, dy: v.translation.height)
                    && SheetLayout.dragMovesSheet(contentScrolls: overContent && contentScrolls, contentAtTop: contentAtTop, raised: raised)
            }
            .onEnded { v in
                defer { dragMoves = nil }
                guard dragMoves == true else { return }
                move(SheetLayout.settle(from: detent, raised: -v.translation.height, velocity: -v.velocity.height,
                                        container: container, peekNeed: peekNeed, headerNeed: headerNeed,
                                        contentLead: SheetLayout.lead(contentLead)))
            }
    }
}

private struct PeekNeedKey: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

private struct HeaderNeedKey: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}
