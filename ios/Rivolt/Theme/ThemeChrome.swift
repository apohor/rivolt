import SwiftUI

/// The brand mark: a lightning bolt in a rounded square (the web's `Logo`, drawn in its 64 x 64 box).
struct ThemeLogo: View {
    var size: CGFloat = 22

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: ThemeTokens.logoFrameRadius, style: .continuous)
                .inset(by: 4 + ThemeTokens.logoFrameStroke / 2)
                .stroke(Theme.brandMark.opacity(ThemeTokens.logoFrameOpacity), lineWidth: ThemeTokens.logoFrameStroke)
            BoltShape().fill(Theme.brandMark)
        }
        .frame(width: ThemeTokens.logoViewBox, height: ThemeTokens.logoViewBox)
        .scaleEffect(size / ThemeTokens.logoViewBox)
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }

    private struct BoltShape: Shape {
        func path(in rect: CGRect) -> Path {
            var p = Path()
            let s = rect.width / 64
            let pts: [(Double, Double)] = [(36, 10), (18, 36), (30, 36), (26, 54), (46, 26), (34, 26)]
            for (i, pt) in pts.enumerated() {
                let q = CGPoint(x: pt.0 * s + rect.minX, y: pt.1 * s + rect.minY)
                if i == 0 { p.move(to: q) } else { p.addLine(to: q) }
            }
            p.closeSubpath()
            return p
        }
    }
}

/// The top bar: the mark and "Rivolt" on the solid bar colour with a hairline below, and the screen's own actions at the
/// trailing end (the account menu).
struct ThemeTopBar<Trailing: View>: View {
    var title = "Rivolt"
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(spacing: ThemeTokens.logoWordmarkGap) {
            ThemeLogo(size: ThemeTokens.logoSize)
            Text(title).font(Theme.sans(16, .semibold, relativeTo: .headline)).tracking(ThemeTokens.logoWordmarkTracking * 16)
                .foregroundStyle(Theme.textPrimary)
            Spacer(minLength: 8)
            trailing
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 3 * Theme.unit)
        .background(Theme.bar)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
    }
}

/// The page title row pinned under the top bar: the title and its controls (the month stepper, the truck switcher) side
/// by side when both fit whole; otherwise the controls go under a one-line title that shrinks rather than breaking a word
/// (the largest text sizes on a small phone).
struct ThemeTitleRow<Trailing: View>: View {
    let title: String
    @ViewBuilder var trailing: Trailing

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .center, spacing: 8) {
                heading.fixedSize()
                Spacer(minLength: 8)
                HStack(spacing: 8) { trailing }.fixedSize()
            }
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                heading.lineLimit(1).minimumScaleFactor(0.5)
                HStack(spacing: 8) { trailing }
            }
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 2 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.background)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
    }

    private var heading: some View {
        Text(title).themeText(.hero).accessibilityAddTraits(.isHeader)
    }
}

/// One tab of the bottom bar: its icon (from the tokens), label and what to show.
struct ThemeTabItem: Identifiable, Equatable {
    let id: String
    let label: String
    let icon: String
}

/// The bottom tab bar: the solid bar colour with a hairline above, an icon over a caption-size medium label, the accent
/// colour for the selected tab and the tertiary text colour for the rest. At least the token's minimum height plus the
/// home-indicator inset (which the safe area adds).
struct ThemeTabBar: View {
    let items: [ThemeTabItem]
    @Binding var selection: String

    var body: some View {
        HStack(spacing: 0) {
            ForEach(items) { item in
                let active = item.id == selection
                Button { selection = item.id } label: {
                    VStack(spacing: 2) {
                        ThemeIcon(paths: ThemeTokens.iconPaths[item.icon] ?? [])
                            .stroke(style: StrokeStyle(lineWidth: ThemeTokens.iconStroke * ThemeTokens.tabBarIcon / 24, lineCap: .round, lineJoin: .round))
                            .frame(width: ThemeTokens.tabBarIcon, height: ThemeTokens.tabBarIcon)
                        Text(item.label).font(Theme.sans(ThemeTokens.typeCaption, .medium, relativeTo: .caption2))
                    }
                    .foregroundStyle(active ? Theme.accent : Theme.textTertiary)
                    .frame(maxWidth: .infinity, minHeight: ThemeTokens.tabBarMinHeight)
                }
                .buttonStyle(.plain)
                // Capped below: a long press shows the label large, as the system tab bar does.
                .accessibilityShowsLargeContentViewer()
                .accessibilityLabel(item.label)
                .accessibilityAddTraits(active ? [.isSelected, .isButton] : .isButton)
            }
        }
        .background(Theme.bar.ignoresSafeArea(edges: .bottom))
        .overlay(alignment: .top) { Rectangle().fill(Theme.border).frame(height: 1) }
        // At accessibility sizes five labels can't fit whole: they broke mid-word and the bar ate the screen.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }
}

/// The month stepper beside the page title: previous and next arrows around the month, in a bordered pill. Next is
/// disabled in the current month.
struct ThemeStepper: View {
    let title: String
    /// What VoiceOver says for the month ("October 2026"); the printed title is the short form.
    var spokenTitle: String?
    let canGoForward: Bool
    let previous: () -> Void
    let next: () -> Void

    var body: some View {
        HStack(spacing: 0) {
            Button(action: previous) { Image(systemName: "arrowtriangle.left.fill").font(.system(size: 11)) }
                .accessibilityLabel("Previous month")
                .frame(width: 40, height: 36)
            Text(title).font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundStyle(Theme.textPrimary)
                .frame(minWidth: 84)
                .lineLimit(1).minimumScaleFactor(0.6)
                .accessibilityLabel(spokenTitle ?? title)
                .accessibilityAddTraits(.isHeader)
            Button(action: next) { Image(systemName: "arrowtriangle.right.fill").font(.system(size: 11)) }
                .disabled(!canGoForward)
                .accessibilityLabel("Next month")
                .frame(width: 40, height: 36)
        }
        .foregroundStyle(Theme.textTertiary)
        .background(Theme.card.opacity(0.5), in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }
}
