import SwiftUI

/// What the Now hero is doing: a glow only while it is live (charging, emerald; driving, sky), never at rest.
enum ThemeLive: Equatable {
    case none, charging, driving
}

/// The web's card: the card colour at half strength over the page, a 1 px border, a faint top-down sheen, a 1 px
/// highlight along the top edge and two soft shadows. The hero is the same card at 60% with a light pool in its
/// top-left and, while live, a tinted border, ring and halo.
struct ThemeCard: ViewModifier {
    var hero = false
    var live: ThemeLive = .none
    var critical = false
    var padding: CGFloat = 4 * Theme.unit

    private var shape: RoundedRectangle { RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous) }

    private var borderColor: Color {
        if critical { return Theme.criticalBorder.opacity(0.5) }
        switch live {
        case .charging: return Theme.glowChargingBorder
        case .driving: return Theme.glowDrivingBorder
        case .none: return Theme.border
        }
    }

    private var tint: Color { live == .driving ? Theme.glowDrivingHalo.opacity(0.325) : Theme.heroTint }

    func body(content: Content) -> some View {
        content
            .padding(hero ? 5 * Theme.unit : padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background {
                ZStack {
                    shape.fill(Theme.card.opacity(hero ? 0.6 : 0.5))
                    shape.fill(LinearGradient(colors: [Theme.cardSheen, .clear], startPoint: .top, endPoint: UnitPoint(x: 0.5, y: 0.45)))
                    if hero {
                        shape.fill(RadialGradient(colors: [tint, .clear], center: UnitPoint(x: 0.14, y: 0), startRadius: 0, endRadius: 260))
                    }
                    VStack(spacing: 0) {
                        Rectangle().fill(Theme.edge).frame(height: 1)
                        Spacer(minLength: 0)
                    }
                    .clipShape(shape)
                    shape.strokeBorder(borderColor, lineWidth: 1)
                }
            }
            .overlay {
                if live == .charging { shape.inset(by: -1).stroke(Theme.glowChargingRing, lineWidth: 1) }
            }
            .shadow(color: live == .charging ? Theme.glowChargingHalo : live == .driving ? Theme.glowDrivingHalo : .clear, radius: 16)
            .shadow(color: Theme.cardShadowNear, radius: 1, x: 0, y: 1)
            .shadow(color: Theme.cardShadowFar, radius: 8, x: 0, y: 8)
    }
}

extension View {
    func themeCard(padding: CGFloat = 4 * Theme.unit) -> some View {
        modifier(ThemeCard(padding: padding))
    }

    func themeHero(live: ThemeLive = .none, critical: Bool = false) -> some View {
        modifier(ThemeCard(hero: true, live: live, critical: critical))
    }

    /// The page: the web's page colour behind everything.
    func themePage() -> some View {
        background(Theme.background.ignoresSafeArea())
    }
}

/// The status pill (Live, Last seen, Asleep): a capsule with a 1 px border and caption text.
struct ThemePill: View {
    enum Tone { case live, stale, neutral }
    let text: String
    var tone: Tone = .neutral

    private var textColor: Color {
        switch tone {
        case .live: return Theme.accent
        case .stale: return Theme.estimate
        case .neutral: return Theme.textSecondary
        }
    }

    private var border: Color {
        switch tone {
        case .live: return Theme.accentFill.opacity(0.3)
        case .stale: return Theme.estimateFill.opacity(0.4)
        case .neutral: return Theme.borderStrong
        }
    }

    var body: some View {
        Text(text)
            .themeText(.caption, color: textColor)
            .padding(.horizontal, 2 * Theme.unit)
            .padding(.vertical, Theme.unit / 2)
            .overlay(Capsule().strokeBorder(border, lineWidth: 1))
    }
}

/// A solid button: the accent button colour with white text and the inner radius.
struct ThemeButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(Theme.sans(ThemeTokens.typeValue, .medium))
            .foregroundStyle(.white)
            .padding(.horizontal, 4 * Theme.unit)
            .padding(.vertical, 2.5 * Theme.unit)
            .background(Theme.accentButton.opacity(configuration.isPressed ? 0.85 : 1), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
    }
}
