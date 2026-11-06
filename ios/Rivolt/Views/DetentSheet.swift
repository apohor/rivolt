import SwiftUI

/// A sheet over the bottom of a full-height map with three resting heights: a peek (just its header), half the screen, and almost all of it.
/// Drag the handle or the header to move between them; the content scrolls only when it is fully open (below that, a drag moves the sheet).
/// For VoiceOver the handle is one adjustable control (swipe up or down), and Reduce Motion turns the springs into plain moves.
/// The three resting heights of the itinerary sheet.
enum SheetDetent: Int, CaseIterable, Equatable {
    case peek, half, full

    var spoken: String {
        switch self {
        case .peek: return "collapsed"
        case .half: return "half open"
        case .full: return "fully open"
        }
    }
}

struct DetentSheet<Header: View, Content: View>: View {
    @Binding var detent: SheetDetent
    /// The height the sheet's detents are fractions of (the map area).
    let container: CGFloat
    /// Whether the content scrolls (false renders it flat, for a snapshot).
    var scrolls = true
    @ViewBuilder var header: () -> Header
    @ViewBuilder var content: () -> Content

    @GestureState private var drag: CGFloat = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    static func height(_ d: SheetDetent, in container: CGFloat) -> CGFloat {
        switch d {
        case .peek: return min(150, container * 0.3)
        case .half: return container * 0.46
        case .full: return container - 8
        }
    }

    /// The detent a released drag settles on: the nearest height to where the drag was heading (the release point plus a share of its velocity).
    /// `raised` is how far up the drag went (negative: down); `velocity` the upward speed at release (points per second).
    static func settle(from: SheetDetent, raised: CGFloat, velocity: CGFloat, container: CGFloat) -> SheetDetent {
        let start = height(from, in: container)
        let aimed = start + raised + velocity * 0.12
        return SheetDetent.allCases.min { abs(height($0, in: container) - aimed) < abs(height($1, in: container) - aimed) } ?? from
    }

    private func move(_ d: SheetDetent) {
        if reduceMotion { detent = d } else { withAnimation(.spring(response: 0.32, dampingFraction: 0.86)) { detent = d } }
    }

    var body: some View {
        let full = Self.height(.full, in: container)
        let shown = min(full, max(Self.height(.peek, in: container) * 0.8, Self.height(detent, in: container) + drag))
        VStack(spacing: 0) {
            VStack(spacing: 0) {
                Button { move(detent == .full ? .half : .full) } label: {
                    Capsule().fill(Theme.borderStrong).frame(width: 36, height: 5).frame(maxWidth: .infinity, minHeight: 28)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Itinerary")
                .accessibilityValue(detent.spoken)
                .accessibilityHint("Swipe up or down to open or close")
                .accessibilityAdjustableAction { direction in
                    let all = SheetDetent.allCases
                    guard let i = all.firstIndex(of: detent) else { return }
                    switch direction {
                    case .increment: if i + 1 < all.count { move(all[i + 1]) }
                    case .decrement: if i > 0 { move(all[i - 1]) }
                    @unknown default: break
                    }
                }
                header().padding(.horizontal, 4 * Theme.unit).padding(.bottom, 8)
            }
            .contentShape(Rectangle())
            .gesture(dragGesture)
            Divider().overlay(Theme.border)
            Group {
                if scrolls {
                    ScrollView { content().padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit) }
                        .scrollDisabled(detent != .full)
                } else {
                    content().padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit)
                }
            }
            // Below fully open a drag anywhere on the sheet moves it; fully open, the content scrolls.
            .gesture(dragGesture, including: detent == .full ? .subviews : .all)
        }
        .frame(maxWidth: .infinity)
        .frame(height: full, alignment: .top)
        .background(Theme.card, in: UnevenRoundedRectangle(topLeadingRadius: 20, topTrailingRadius: 20, style: .continuous))
        .overlay(UnevenRoundedRectangle(topLeadingRadius: 20, topTrailingRadius: 20, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 12, y: -2)
        .offset(y: full - shown)
        .frame(height: shown, alignment: .top)
        .frame(maxHeight: .infinity, alignment: .bottom)
    }

    private var dragGesture: some Gesture {
        DragGesture(minimumDistance: 6)
            .updating($drag) { v, state, _ in state = -v.translation.height }
            .onEnded { v in
                move(Self.settle(from: detent, raised: -v.translation.height, velocity: -(v.predictedEndTranslation.height - v.translation.height), container: container))
            }
    }
}
