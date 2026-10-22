// Generated from ios/design/tokens.json by ios/design/generate_theme.py. Do not edit by hand:
// change the web, regenerate tokens.json, then run the script. ThemeTokensTests checks the two agree.
import Foundation

enum ThemeTokens {
    /// sRGB as hex with an alpha, as tokens.json carries it.
    struct Rgba: Equatable {
        let hex: UInt32
        let alpha: Double
        init(_ hex: UInt32, _ alpha: Double = 1) { self.hex = hex; self.alpha = alpha }
    }

    /// A colour in dark and in light.
    struct Pair: Equatable {
        let dark: Rgba
        let light: Rgba
    }

    // MARK: Roles (semantic colours)
    static let background = Pair(dark: Rgba(0x06080C, 1), light: Rgba(0xF6F7FA, 1))
    static let card = Pair(dark: Rgba(0x0E1115, 1), light: Rgba(0xFCFDFE, 1))
    static let border = Pair(dark: Rgba(0x1D2127, 1), light: Rgba(0xE1E3E7, 1))
    static let borderStrong = Pair(dark: Rgba(0x2E3239, 1), light: Rgba(0xD4D4D4, 1))
    static let textPrimary = Pair(dark: Rgba(0xF5F5F5, 1), light: Rgba(0x171717, 1))
    static let textBody = Pair(dark: Rgba(0xD4D4D4, 1), light: Rgba(0x404040, 1))
    static let textSecondary = Pair(dark: Rgba(0xA1A1A1, 1), light: Rgba(0x525252, 1))
    static let textTertiary = Pair(dark: Rgba(0x929292, 1), light: Rgba(0x636363, 1))
    static let accent = Pair(dark: Rgba(0x5EE9B5, 1), light: Rgba(0x006A49, 1))
    static let accentFill = Pair(dark: Rgba(0x00BC7D, 1), light: Rgba(0x007A55, 1))
    static let accentButton = Pair(dark: Rgba(0x008052, 1), light: Rgba(0x008052, 1))
    static let estimate = Pair(dark: Rgba(0xFFD230, 1), light: Rgba(0x973C00, 1))
    static let estimateFill = Pair(dark: Rgba(0xFE9A00, 1), light: Rgba(0xBB4D00, 1))
    static let critical = Pair(dark: Rgba(0xFFA1AD, 1), light: Rgba(0xC70036, 1))
    static let criticalBorder = Pair(dark: Rgba(0xFF2056, 1), light: Rgba(0xEC003F, 1))
    static let drive = Pair(dark: Rgba(0x74D4FF, 1), light: Rgba(0x0069A8, 1))
    static let home = Pair(dark: Rgba(0x00D492, 1), light: Rgba(0x007A55, 1))
    static let away = Pair(dark: Rgba(0x00BCFF, 1), light: Rgba(0x0069A8, 1))
    static let awayFill = Pair(dark: Rgba(0x00A6F4, 1), light: Rgba(0x0084D1, 1))
    static let fast = Pair(dark: Rgba(0xFFB900, 1), light: Rgba(0x973C00, 1))
    static let fastFill = Pair(dark: Rgba(0xFE9A00, 1), light: Rgba(0xBB4D00, 1))
    static let edge = Pair(dark: Rgba(0xFFFFFF, 0.07), light: Rgba(0xFFFFFF, 0.9))
    static let brandMark = Pair(dark: Rgba(0x00D492, 1), light: Rgba(0x007A55, 1))

    // MARK: Literals (glows, gauge gradients, shadows, chart colours)
    static let bar = Pair(dark: Rgba(0x06080C, 1), light: Rgba(0xFCFCFC, 1))
    static let heroTint = Pair(dark: Rgba(0x29D5A5, 0.13), light: Rgba(0x64D6AF, 0.2))
    static let pageGlow = Pair(dark: Rgba(0x334A62, 0.18), light: Rgba(0xBFE9D8, 0.35))
    static let heroTintDriving = Pair(dark: Rgba(0x5ABDF2, 0.13), light: Rgba(0x7AC8F5, 0.2))
    static let cardSheen = Pair(dark: Rgba(0xFFFFFF, 0.03), light: Rgba(0xFFFFFF, 0.7))
    static let cardShadowNear = Pair(dark: Rgba(0x000000, 0.45), light: Rgba(0x282E38, 0.07))
    static let cardShadowFar = Pair(dark: Rgba(0x000000, 0.5), light: Rgba(0x262E3D, 0.22))
    static let glowChargingBorder = Pair(dark: Rgba(0x29D5A5, 0.42), light: Rgba(0x009A70, 0.5))
    static let glowChargingRing = Pair(dark: Rgba(0x29D5A5, 0.16), light: Rgba(0x009A70, 0.18))
    static let glowChargingHalo = Pair(dark: Rgba(0x29D5A5, 0.4), light: Rgba(0x009A70, 0.45))
    static let glowDrivingBorder = Pair(dark: Rgba(0x5ABDF2, 0.42), light: Rgba(0x0E84B7, 0.5))
    static let glowDrivingRing = Pair(dark: Rgba(0x5ABDF2, 0.16), light: Rgba(0x0E84B7, 0.18))
    static let glowDrivingHalo = Pair(dark: Rgba(0x5ABDF2, 0.4), light: Rgba(0x0E84B7, 0.45))
    static let glowGauge = Pair(dark: Rgba(0x29D5A5, 0.6), light: Rgba(0x009A70, 0.5))
    static let glowSparkLine = Pair(dark: Rgba(0x29D5A5, 0.7), light: Rgba(0x009A70, 0.45))
    static let glowSparkDot = Pair(dark: Rgba(0x59E7B9, 0.9), light: Rgba(0x009A70, 0.7))
    static let gaugeOkFrom = Pair(dark: Rgba(0x29D5A5, 1), light: Rgba(0x29D5A5, 1))
    static let gaugeOkTo = Pair(dark: Rgba(0x00B7B0, 1), light: Rgba(0x00B7B0, 1))
    static let gaugeCriticalFrom = Pair(dark: Rgba(0xFF6A65, 1), light: Rgba(0xFF6A65, 1))
    static let gaugeCriticalTo = Pair(dark: Rgba(0xF34E6A, 1), light: Rgba(0xF34E6A, 1))
    static let gaugeIdleFrom = Pair(dark: Rgba(0x7F8793, 1), light: Rgba(0x7F8793, 1))
    static let gaugeIdleTo = Pair(dark: Rgba(0x6B727E, 1), light: Rgba(0x6B727E, 1))
    static let chartMilesBar = Pair(dark: Rgba(0x10B981, 1), light: Rgba(0x10B981, 1))
    static let chartSocCharge = Pair(dark: Rgba(0x10B981, 1), light: Rgba(0x10B981, 1))
    static let chartSocDrive = Pair(dark: Rgba(0x38BDF8, 1), light: Rgba(0x38BDF8, 1))
    static let chartSocLimit = Pair(dark: Rgba(0xF59E0B, 1), light: Rgba(0xF59E0B, 1))
    static let chartIdleAwakeBar = Pair(dark: Rgba(0x6366F1, 1), light: Rgba(0x6366F1, 1))
    static let chartIdleAwakeTile = Pair(dark: Rgba(0x818CF8, 1), light: Rgba(0x818CF8, 1))
    static let shareCardBackground = Pair(dark: Rgba(0x0A0A0A, 1), light: Rgba(0x0A0A0A, 1))
    static let shareCardMuted = Pair(dark: Rgba(0xA3A3A3, 1), light: Rgba(0xA3A3A3, 1))
    static let shareCardTitle = Pair(dark: Rgba(0xFAFAFA, 1), light: Rgba(0xFAFAFA, 1))
    static let shareCardValue = Pair(dark: Rgba(0xE5E5E5, 1), light: Rgba(0xE5E5E5, 1))
    static let shareCardNote = Pair(dark: Rgba(0xFBBF24, 1), light: Rgba(0xFBBF24, 1))
    static let shareCardFooter = Pair(dark: Rgba(0x737373, 1), light: Rgba(0x737373, 1))
    static let routeGlow = Pair(dark: Rgba(0x10B981, 1), light: Rgba(0x10B981, 1))
    static let routeLine = Pair(dark: Rgba(0x34D399, 1), light: Rgba(0x34D399, 1))
    static let routeSpeedUnder5 = Pair(dark: Rgba(0x6B7280, 1), light: Rgba(0x6B7280, 1))
    static let routeSpeed5to25 = Pair(dark: Rgba(0x06B6D4, 1), light: Rgba(0x06B6D4, 1))
    static let routeSpeed25to50 = Pair(dark: Rgba(0x34D399, 1), light: Rgba(0x34D399, 1))
    static let routeSpeed50to65 = Pair(dark: Rgba(0xF59E0B, 1), light: Rgba(0xF59E0B, 1))
    static let routeSpeed65Plus = Pair(dark: Rgba(0xF43F5E, 1), light: Rgba(0xF43F5E, 1))
    static let routeGap = Pair(dark: Rgba(0x737373, 1), light: Rgba(0x737373, 1))
    static let routeStart = Pair(dark: Rgba(0x10B981, 1), light: Rgba(0x10B981, 1))
    static let routeEnd = Pair(dark: Rgba(0xF43F5E, 1), light: Rgba(0xF43F5E, 1))
    static let chargePin = Pair(dark: Rgba(0xF59E0B, 1), light: Rgba(0xF59E0B, 1))
    static let chargeRecorded = Pair(dark: Rgba(0x737373, 1), light: Rgba(0x737373, 1))
    static let chargePinInk = Pair(dark: Rgba(0x0A0A0A, 1), light: Rgba(0x0A0A0A, 1))
    static let activityRoute = Pair(dark: Rgba(0x34D399, 1), light: Rgba(0x34D399, 1))
    static let chargeFast = Pair(dark: Rgba(0xF43F5E, 1), light: Rgba(0xF43F5E, 1))
    static let chargeL2 = Pair(dark: Rgba(0x06B6D4, 1), light: Rgba(0x06B6D4, 1))
    static let chargeL1 = Pair(dark: Rgba(0xA3A3A3, 1), light: Rgba(0xA3A3A3, 1))

    // MARK: Scalars (points; milliseconds for motion; em for tracking)
    static let typeCaption: Double = 11
    static let typeSubline: Double = 12
    static let typeValue: Double = 14
    static let typeTitle: Double = 24
    static let typeHero: Double = 36
    static let typeLabelSize: Double = 11
    static let typeLabelTracking: Double = 0.09
    static let typeLabelWeight: Double = 500
    static let typeNumberTracking: Double = -0.012
    static let spaceUnit: Double = 4
    static let radiusCard: Double = 12
    static let radiusInner: Double = 8
    static let motionMs: Double = 200
    static let gaugeSweep: Double = 240
    static let gaugeRadius: Double = 52
    static let gaugeStroke: Double = 7
    static let gaugeTrackOpacity: Double = 0.14
    static let gaugeSize: Double = 132
    static let logoSize: Double = 22
    static let logoViewBox: Double = 64
    static let logoFrameRadius: Double = 12
    static let logoFrameStroke: Double = 3.5
    static let logoFrameOpacity: Double = 0.9
    static let logoWordmarkGap: Double = 8
    static let logoWordmarkTracking: Double = -0.025
    static let tabBarMinHeight: Double = 56
    static let tabBarIcon: Double = 22
    static let tabBarIconStroke: Double = 1.8
    static let mapRouteWidth: Double = 1.5
    static let mapRouteOpacity: Double = 0.5
    static let mapDot: Double = 10
    static let mapDotRing: Double = 1.5

    // MARK: Tab bar icons (SVG path data in a 24 x 24 box, stroked)
    static let iconStroke: Double = 1.8
    static let iconPaths: [String: [String]] = [
        "now": ["M4 15a8 8 0 1 1 16 0", "M12 15l4-5", "circle 12 15 1"],
        "activity": ["circle 5 6 1.5", "circle 5 12 1.5", "circle 5 18 1.5", "M9 6h11M9 12h11M9 18h11"],
        "money": ["M6 3h12v18l-3-2-3 2-3-2-3 2z", "M9 8h6M9 12h6M9 16h3"],
        "plan": ["M12 21s-6-5.3-6-10a6 6 0 0 1 12 0c0 4.7-6 10-6 10z", "circle 12 11 2"],
        "truck": ["M3 16V8h11v8M14 11h4l3 3v2h-7", "circle 7 17 2", "circle 17 17 2"],
    ]
}
