import CoreText
import SwiftUI
import UIKit

/// The iPhone app's look: the web's new look (`ui_theme_next`), from `ios/design/tokens.json` through the generated
/// `ThemeTokens`. Colours follow the system appearance; nothing outside this folder types a colour or a size.
enum Theme {
    // MARK: Colour

    private static func ui(_ c: ThemeTokens.Rgba) -> UIColor {
        UIColor(red: CGFloat((c.hex >> 16) & 0xFF) / 255, green: CGFloat((c.hex >> 8) & 0xFF) / 255,
                blue: CGFloat(c.hex & 0xFF) / 255, alpha: CGFloat(c.alpha))
    }

    /// A colour that is the dark or the light value as the appearance changes.
    static func color(_ p: ThemeTokens.Pair) -> Color {
        Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? ui(p.dark) : ui(p.light) })
    }

    // Roles
    static let background = color(ThemeTokens.background)
    static let card = color(ThemeTokens.card)
    static let border = color(ThemeTokens.border)
    static let borderStrong = color(ThemeTokens.borderStrong)
    static let textPrimary = color(ThemeTokens.textPrimary)
    static let textBody = color(ThemeTokens.textBody)
    static let textSecondary = color(ThemeTokens.textSecondary)
    static let textTertiary = color(ThemeTokens.textTertiary)
    static let accent = color(ThemeTokens.accent)
    static let brandMark = color(ThemeTokens.brandMark)
    static let accentFill = color(ThemeTokens.accentFill)
    static let accentButton = color(ThemeTokens.accentButton)
    static let estimate = color(ThemeTokens.estimate)
    static let estimateFill = color(ThemeTokens.estimateFill)
    static let critical = color(ThemeTokens.critical)
    static let criticalBorder = color(ThemeTokens.criticalBorder)
    static let drive = color(ThemeTokens.drive)
    /// Charging places, as the web draws them: the dot or icon, and the bar fill (Money, Activity).
    static let home = color(ThemeTokens.home)
    static let away = color(ThemeTokens.away)
    static let awayFill = color(ThemeTokens.awayFill)
    static let fast = color(ThemeTokens.fast)
    static let fastFill = color(ThemeTokens.fastFill)
    /// The share card: a fixed dark image in both themes (the web's paintShareCard).
    static let shareCardBackground = color(ThemeTokens.shareCardBackground)
    static let shareCardMuted = color(ThemeTokens.shareCardMuted)
    static let shareCardTitle = color(ThemeTokens.shareCardTitle)
    static let shareCardValue = color(ThemeTokens.shareCardValue)
    static let shareCardNote = color(ThemeTokens.shareCardNote)
    static let shareCardFooter = color(ThemeTokens.shareCardFooter)
    /// The drive route on a map (the web's DriveMap), the same in both themes: glow, speed buckets, gaps and pins.
    static let routeGlow = color(ThemeTokens.routeGlow)
    static let routeLine = color(ThemeTokens.routeLine)
    static let routeSpeedUnder5 = color(ThemeTokens.routeSpeedUnder5)
    static let routeSpeed5to25 = color(ThemeTokens.routeSpeed5to25)
    static let routeSpeed25to50 = color(ThemeTokens.routeSpeed25to50)
    static let routeSpeed50to65 = color(ThemeTokens.routeSpeed50to65)
    static let routeSpeed65Plus = color(ThemeTokens.routeSpeed65Plus)
    static let routeGap = color(ThemeTokens.routeGap)
    static let routeStart = color(ThemeTokens.routeStart)
    static let routeEnd = color(ThemeTokens.routeEnd)
    static let chargePin = color(ThemeTokens.chargePin)
    static let chargeRecorded = color(ThemeTokens.chargeRecorded)
    static let chargePinInk = color(ThemeTokens.chargePinInk)
    static let mapDrive = color(ThemeTokens.mapDrive)
    static let chargeFast = color(ThemeTokens.chargeFast)
    static let chargeL2 = color(ThemeTokens.chargeL2)
    static let chargeL1 = color(ThemeTokens.chargeL1)
    static let edge = color(ThemeTokens.edge)

    // Literals
    static let bar = color(ThemeTokens.bar)
    static let heroTint = color(ThemeTokens.heroTint)
    static let cardSheen = color(ThemeTokens.cardSheen)
    static let cardShadowNear = color(ThemeTokens.cardShadowNear)
    static let cardShadowFar = color(ThemeTokens.cardShadowFar)
    static let glowChargingBorder = color(ThemeTokens.glowChargingBorder)
    static let glowChargingRing = color(ThemeTokens.glowChargingRing)
    static let glowChargingHalo = color(ThemeTokens.glowChargingHalo)
    static let glowDrivingBorder = color(ThemeTokens.glowDrivingBorder)
    static let glowDrivingRing = color(ThemeTokens.glowDrivingRing)
    static let glowDrivingHalo = color(ThemeTokens.glowDrivingHalo)
    static let glowSparkDot = color(ThemeTokens.glowSparkDot)
    static let heroTintDriving = color(ThemeTokens.heroTintDriving)
    static let pageGlow = color(ThemeTokens.pageGlow)
    static let glowGauge = color(ThemeTokens.glowGauge)
    static let glowSparkLine = color(ThemeTokens.glowSparkLine)
    static let gaugeOk = [color(ThemeTokens.gaugeOkFrom), color(ThemeTokens.gaugeOkTo)]
    static let gaugeCritical = [color(ThemeTokens.gaugeCriticalFrom), color(ThemeTokens.gaugeCriticalTo)]
    static let gaugeIdle = [color(ThemeTokens.gaugeIdleFrom), color(ThemeTokens.gaugeIdleTo)]
    static let chartMilesBar = color(ThemeTokens.chartMilesBar)
    static let chartSocCharge = color(ThemeTokens.chartSocCharge)
    static let chartSocDrive = color(ThemeTokens.chartSocDrive)
    static let chartSocLimit = color(ThemeTokens.chartSocLimit)
    static let chartIdleAwakeBar = color(ThemeTokens.chartIdleAwakeBar)
    static let chartIdleAwakeTile = color(ThemeTokens.chartIdleAwakeTile)

    // MARK: Scalars

    static let radiusCard = CGFloat(ThemeTokens.radiusCard)
    static let radiusInner = CGFloat(ThemeTokens.radiusInner)
    static let unit = CGFloat(ThemeTokens.spaceUnit)
    static let motion = ThemeTokens.motionMs / 1000

    // MARK: Fonts

    enum Weight { case regular, medium, semibold }

    /// Geist, or the system font where it is not registered. Scales with Dynamic Type relative to `style`.
    static func sans(_ size: Double, _ weight: Weight = .regular, relativeTo style: Font.TextStyle = .body) -> Font {
        let name: String
        switch weight {
        case .regular: name = "Geist-Regular"
        case .medium: name = "Geist-Medium"
        case .semibold: name = "Geist-SemiBold"
        }
        return .custom(name, size: size, relativeTo: style)
    }

    /// Geist Mono: the hero readouts only (time left, the kW value).
    static func mono(_ size: Double, _ weight: Weight = .medium, relativeTo style: Font.TextStyle = .body) -> Font {
        .custom(weight == .semibold ? "GeistMono-SemiBold" : "GeistMono-Medium", size: size, relativeTo: style)
    }

    private static let registered: Void = {
        for name in ["Geist-Regular", "Geist-Medium", "Geist-SemiBold", "GeistMono-Medium", "GeistMono-SemiBold"] {
            guard let url = Bundle.main.url(forResource: name, withExtension: "ttf")
                ?? Bundle(for: BundleToken.self).url(forResource: name, withExtension: "ttf") else { continue }
            CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil)
        }
    }()

    /// Registers the bundled fonts once; safe to call again.
    static func registerFonts() { _ = registered }

    private final class BundleToken {}
}

// MARK: Text styles

/// The type scale: caption 11, sub-line 12, value 14, title 24, hero 36, with the web's tracking on numbers.
enum ThemeText {
    case label, caption, subline, value, title, hero

    var font: Font {
        switch self {
        case .label: return Theme.sans(ThemeTokens.typeLabelSize, .medium, relativeTo: .caption)
        case .caption: return Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)
        case .subline: return Theme.sans(ThemeTokens.typeSubline, .regular, relativeTo: .footnote)
        case .value: return Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)
        case .title: return Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)
        case .hero: return Theme.sans(ThemeTokens.typeHero, .semibold, relativeTo: .largeTitle)
        }
    }

    var size: Double {
        switch self {
        case .label: return ThemeTokens.typeLabelSize
        case .caption: return ThemeTokens.typeCaption
        case .subline: return ThemeTokens.typeSubline
        case .value: return ThemeTokens.typeValue
        case .title: return ThemeTokens.typeTitle
        case .hero: return ThemeTokens.typeHero
        }
    }

    var color: Color {
        switch self {
        case .label: return Theme.textTertiary
        case .caption, .subline: return Theme.textSecondary
        case .value: return Theme.textBody
        case .title, .hero: return Theme.textPrimary
        }
    }
}

extension View {
    /// A style from the type scale. Small-caps labels are uppercase with the web's tracking; numbers are tabular.
    @ViewBuilder
    func themeText(_ style: ThemeText, color: Color? = nil) -> some View {
        switch style {
        case .label:
            self.font(style.font).textCase(.uppercase).tracking(ThemeTokens.typeLabelTracking * style.size)
                .foregroundStyle(color ?? style.color)
        case .hero, .title:
            self.font(style.font).monospacedDigit().tracking(ThemeTokens.typeNumberTracking * style.size)
                .foregroundStyle(color ?? style.color)
        default:
            self.font(style.font).monospacedDigit().foregroundStyle(color ?? style.color)
        }
    }
}
