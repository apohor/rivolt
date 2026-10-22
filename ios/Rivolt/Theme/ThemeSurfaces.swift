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

    private var tint: Color { live == .driving ? Theme.heroTintDriving : Theme.heroTint }

    func body(content: Content) -> some View {
        content
            .padding(hero ? 5 * Theme.unit : padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background {
                ZStack {
                    // Shadows first and outside the card only, like CSS box-shadow: under a translucent card they
                    // would show through it, and on the content they would put a shadow behind every word.
                    shape.outerShadow(Theme.cardShadowFar, blur: 8, y: 8)
                    shape.outerShadow(Theme.cardShadowNear, blur: 1, y: 1)
                    if live != .none {
                        shape.outerShadow(live == .charging ? Theme.glowChargingHalo : Theme.glowDrivingHalo, blur: 16, y: 0)
                    }
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
                if live == .driving { shape.inset(by: -1).stroke(Theme.glowDrivingRing, lineWidth: 1) }
            }
    }
}

extension Shape {
    /// A blurred copy of the shape in `color`, offset down by `y`, drawn only outside the shape itself.
    func outerShadow(_ color: Color, blur: CGFloat, y: CGFloat) -> some View {
        fill(color)
            .blur(radius: blur)
            .offset(y: y)
            .mask {
                Rectangle().fill(.black).padding(-60)
                    .overlay { fill(.black).blendMode(.destinationOut) }
                    .compositingGroup()
            }
    }
}

extension View {
    func themeCard(padding: CGFloat = 4 * Theme.unit) -> some View {
        modifier(ThemeCard(padding: padding))
    }

    func themeHero(live: ThemeLive = .none, critical: Bool = false) -> some View {
        modifier(ThemeCard(hero: true, live: live, critical: critical))
    }

    /// The page: the web's page colour with its soft glow at the top, behind everything.
    func themePage() -> some View {
        background(ThemePageBackground().ignoresSafeArea())
    }
}

/// The page colour and the glow pooled at the top centre (`radial-gradient(120% 60% at 50% -10%, glow, transparent 70%)`
/// on the web): fixed, it doesn't scroll with the content.
struct ThemePageBackground: View {
    var body: some View {
        ZStack {
            Theme.background
            Canvas { ctx, size in
                let center = CGPoint(x: size.width * 0.5, y: size.height * -0.1)
                let rx = size.width * 1.2, ry = size.height * 0.6
                var c = ctx
                c.translateBy(x: center.x, y: center.y)
                c.scaleBy(x: 1, y: ry / rx)
                let gradient = Gradient(colors: [Theme.pageGlow, .clear])
                c.fill(Path(CGRect(x: -rx, y: -rx, width: rx * 2, height: rx * 2)),
                       with: .radialGradient(gradient, center: .zero, startRadius: 0, endRadius: rx * 0.7))
            }
        }
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
            // The warning pill carries a faint amber fill, like the web's.
            .background(tone == .stale ? Theme.estimateFill.opacity(0.1) : .clear, in: Capsule())
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
