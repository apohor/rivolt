import SwiftUI

/// The brand mark: a lightning bolt in a rounded square (the web's `Logo`, drawn in its 64 x 64 box).
struct ThemeLogo: View {
    var size: CGFloat = 22

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .inset(by: 4 + 1.75)
                .stroke(Theme.accent.opacity(0.9), lineWidth: 3.5)
            BoltShape().fill(Theme.accent)
        }
        .frame(width: 64, height: 64)
        .scaleEffect(size / 64)
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
        HStack(spacing: 8) {
            ThemeLogo(size: 22)
            Text(title).font(Theme.sans(16, .semibold, relativeTo: .headline)).foregroundStyle(Theme.textPrimary)
            Spacer(minLength: 8)
            trailing
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 3 * Theme.unit)
        .background(Theme.bar)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
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
                .accessibilityLabel(item.label)
                .accessibilityAddTraits(active ? [.isSelected, .isButton] : .isButton)
            }
        }
        .background(Theme.bar.ignoresSafeArea(edges: .bottom))
        .overlay(alignment: .top) { Rectangle().fill(Theme.border).frame(height: 1) }
    }
}
