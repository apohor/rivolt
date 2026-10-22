import SwiftUI
import XCTest
@testable import Rivolt

/// ios/design/tokens.json is generated from the web CSS and ThemeTokens.swift from it. These fail when the two drift
/// (someone edited one by hand, or regenerated only one).
final class ThemeTokensTests: XCTestCase {
    private struct Doc: Decodable {
        struct C: Decodable { let hex: String; let alpha: Double }
        struct P: Decodable { let dark: C?; let light: C?; let both: C? }
        struct S: Decodable { let value: Value }
        let roles: [String: P]
        let literals: [String: P]
        let scalars: [String: S]
    }

    private enum Value: Decodable {
        case number(Double), text(String)
        init(from d: Decoder) throws {
            let c = try d.singleValueContainer()
            if let n = try? c.decode(Double.self) { self = .number(n) } else { self = .text(try c.decode(String.self)) }
        }
    }

    private func doc() throws -> Doc {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "tokens", withExtension: "json"), "tokens.json is not in the test bundle")
        return try JSONDecoder().decode(Doc.self, from: Data(contentsOf: url))
    }

    private func hex(_ c: Doc.C) -> UInt32 { UInt32(c.hex.dropFirst(), radix: 16)! }

    private func check(_ name: String, _ p: Doc.P, _ swift: ThemeTokens.Pair, file: StaticString = #filePath, line: UInt = #line) {
        let dark = p.dark ?? p.both!
        let light = p.light ?? p.both!
        XCTAssertEqual(swift.dark.hex, hex(dark), "\(name) dark", file: file, line: line)
        XCTAssertEqual(swift.dark.alpha, dark.alpha, accuracy: 1e-9, "\(name) dark alpha", file: file, line: line)
        XCTAssertEqual(swift.light.hex, hex(light), "\(name) light", file: file, line: line)
        XCTAssertEqual(swift.light.alpha, light.alpha, accuracy: 1e-9, "\(name) light alpha", file: file, line: line)
    }

    func testEveryRoleAndLiteralMatchesTheJSON() throws {
        let d = try doc()
        let mirror = Mirror(reflecting: ThemeTokens.self)
        _ = mirror
        let swiftPairs: [String: ThemeTokens.Pair] = [
            "background": ThemeTokens.background, "card": ThemeTokens.card, "border": ThemeTokens.border,
            "borderStrong": ThemeTokens.borderStrong, "textPrimary": ThemeTokens.textPrimary, "textBody": ThemeTokens.textBody,
            "textSecondary": ThemeTokens.textSecondary, "textTertiary": ThemeTokens.textTertiary, "accent": ThemeTokens.accent,
            "accentFill": ThemeTokens.accentFill, "accentButton": ThemeTokens.accentButton, "estimate": ThemeTokens.estimate,
            "estimateFill": ThemeTokens.estimateFill, "critical": ThemeTokens.critical, "criticalBorder": ThemeTokens.criticalBorder,
            "drive": ThemeTokens.drive, "edge": ThemeTokens.edge,
            "home": ThemeTokens.home, "away": ThemeTokens.away, "awayFill": ThemeTokens.awayFill, "fast": ThemeTokens.fast,
            "fastFill": ThemeTokens.fastFill,
            "shareCardBackground": ThemeTokens.shareCardBackground, "shareCardMuted": ThemeTokens.shareCardMuted,
            "shareCardTitle": ThemeTokens.shareCardTitle, "shareCardValue": ThemeTokens.shareCardValue,
            "shareCardNote": ThemeTokens.shareCardNote, "shareCardFooter": ThemeTokens.shareCardFooter,
            "routeGlow": ThemeTokens.routeGlow,
            "routeLine": ThemeTokens.routeLine,
            "routeSpeedUnder5": ThemeTokens.routeSpeedUnder5,
            "routeSpeed5to25": ThemeTokens.routeSpeed5to25,
            "routeSpeed25to50": ThemeTokens.routeSpeed25to50,
            "routeSpeed50to65": ThemeTokens.routeSpeed50to65,
            "routeSpeed65Plus": ThemeTokens.routeSpeed65Plus,
            "routeGap": ThemeTokens.routeGap,
            "routeStart": ThemeTokens.routeStart,
            "routeEnd": ThemeTokens.routeEnd,
            "chargePin": ThemeTokens.chargePin,
            "chargeRecorded": ThemeTokens.chargeRecorded,
            "chargePinInk": ThemeTokens.chargePinInk,
            "bar": ThemeTokens.bar, "heroTint": ThemeTokens.heroTint, "heroTintDriving": ThemeTokens.heroTintDriving, "pageGlow": ThemeTokens.pageGlow,
            "glowDrivingRing": ThemeTokens.glowDrivingRing, "glowSparkDot": ThemeTokens.glowSparkDot, "cardSheen": ThemeTokens.cardSheen,
            "cardShadowNear": ThemeTokens.cardShadowNear, "cardShadowFar": ThemeTokens.cardShadowFar,
            "glowChargingBorder": ThemeTokens.glowChargingBorder, "glowChargingRing": ThemeTokens.glowChargingRing,
            "glowChargingHalo": ThemeTokens.glowChargingHalo, "glowDrivingBorder": ThemeTokens.glowDrivingBorder,
            "glowDrivingHalo": ThemeTokens.glowDrivingHalo, "glowGauge": ThemeTokens.glowGauge, "glowSparkLine": ThemeTokens.glowSparkLine,
            "gaugeOkFrom": ThemeTokens.gaugeOkFrom, "gaugeOkTo": ThemeTokens.gaugeOkTo,
            "gaugeCriticalFrom": ThemeTokens.gaugeCriticalFrom, "gaugeCriticalTo": ThemeTokens.gaugeCriticalTo,
            "gaugeIdleFrom": ThemeTokens.gaugeIdleFrom, "gaugeIdleTo": ThemeTokens.gaugeIdleTo,
            "chartMilesBar": ThemeTokens.chartMilesBar, "chartSocCharge": ThemeTokens.chartSocCharge,
            "chartSocDrive": ThemeTokens.chartSocDrive, "chartSocLimit": ThemeTokens.chartSocLimit,
            "chartIdleAwakeBar": ThemeTokens.chartIdleAwakeBar, "chartIdleAwakeTile": ThemeTokens.chartIdleAwakeTile,
            "brandMark": ThemeTokens.brandMark,
        ]
        for (name, p) in d.roles.merging(d.literals, uniquingKeysWith: { a, _ in a }) {
            let swift = try XCTUnwrap(swiftPairs[name], "\(name) is in tokens.json but not in ThemeTokens: run ios/design/generate_theme.py")
            check(name, p, swift)
        }
        XCTAssertEqual(swiftPairs.count, d.roles.count + d.literals.count, "ThemeTokens has a colour tokens.json doesn't")
    }

    func testScalarsMatchTheJSON() throws {
        let d = try doc()
        func n(_ key: String) -> Double? {
            if case .number(let v)? = d.scalars[key]?.value { return v }
            return nil
        }
        XCTAssertEqual(ThemeTokens.typeCaption, n("type.caption"))
        XCTAssertEqual(ThemeTokens.typeSubline, n("type.subline"))
        XCTAssertEqual(ThemeTokens.typeValue, n("type.value"))
        XCTAssertEqual(ThemeTokens.typeTitle, n("type.title"))
        XCTAssertEqual(ThemeTokens.typeHero, n("type.hero"))
        XCTAssertEqual(ThemeTokens.typeLabelSize, n("type.labelSize"))
        XCTAssertEqual(ThemeTokens.typeLabelTracking, n("type.labelTracking"))
        XCTAssertEqual(ThemeTokens.typeNumberTracking, n("type.numberTracking"))
        XCTAssertEqual(ThemeTokens.spaceUnit, n("space.unit"))
        XCTAssertEqual(ThemeTokens.radiusCard, n("radius.card"))
        XCTAssertEqual(ThemeTokens.radiusInner, n("radius.inner"))
        XCTAssertEqual(ThemeTokens.motionMs, n("motion.ms"))
        XCTAssertEqual(ThemeTokens.gaugeSweep, n("gauge.sweep"))
        XCTAssertEqual(ThemeTokens.gaugeRadius, n("gauge.radius"))
        XCTAssertEqual(ThemeTokens.gaugeStroke, n("gauge.stroke"))
        XCTAssertEqual(ThemeTokens.gaugeTrackOpacity, n("gauge.trackOpacity"))
        XCTAssertEqual(ThemeTokens.gaugeSize, n("gauge.size"))
        XCTAssertEqual(ThemeTokens.tabBarMinHeight, n("tabBar.minHeight"))
        XCTAssertEqual(ThemeTokens.tabBarIcon, n("tabBar.icon"))
        XCTAssertEqual(ThemeTokens.tabBarIconStroke, n("tabBar.iconStroke"))
        XCTAssertEqual(ThemeTokens.logoSize, n("logo.size"))
        XCTAssertEqual(ThemeTokens.logoViewBox, n("logo.viewBox"))
        XCTAssertEqual(ThemeTokens.logoFrameRadius, n("logo.frameRadius"))
        XCTAssertEqual(ThemeTokens.logoFrameStroke, n("logo.frameStroke"))
        XCTAssertEqual(ThemeTokens.logoFrameOpacity, n("logo.frameOpacity"))
        XCTAssertEqual(ThemeTokens.logoWordmarkGap, n("logo.wordmarkGap"))
        XCTAssertEqual(ThemeTokens.logoWordmarkTracking, n("logo.wordmarkTracking"))
    }

    func testTheTabBarIconsMatchTheJSON() throws {
        struct Icons: Decodable {
            struct I: Decodable { let stroke: Double; let paths: [String: [String]] }
            let icons: I
        }
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "tokens", withExtension: "json"))
        let icons = try JSONDecoder().decode(Icons.self, from: Data(contentsOf: url)).icons
        XCTAssertEqual(ThemeTokens.iconStroke, icons.stroke)
        XCTAssertEqual(ThemeTokens.iconPaths, icons.paths)
        // Every path draws something, whatever commands it uses.
        for (name, paths) in icons.paths {
            XCTAssertFalse(SVGPath.path(paths).boundingRect.isEmpty, "\(name) icon drew nothing")
        }
    }

    func testTheBundledFontsRegisterAndResolve() {
        Theme.registerFonts()
        for name in ["Geist-Regular", "Geist-Medium", "Geist-SemiBold", "GeistMono-Medium", "GeistMono-SemiBold"] {
            XCTAssertNotNil(UIFont(name: name, size: 14), "\(name) is not available: is it in the app bundle?")
        }
    }

    func testTheLicenceShipsInTheApp() {
        let found = Bundle.main.url(forResource: "OFL", withExtension: "txt") != nil
            || Bundle(for: Self.self).url(forResource: "OFL", withExtension: "txt") != nil
        XCTAssertTrue(found, "OFL.txt must be in the app bundle")
    }

    func testTheAcknowledgementsCarryTheGeistLicence() {
        let text = AcknowledgementsView.licenceText()
        XCTAssertTrue(text.contains("Geist Project Authors"), "the copyright line is missing")
        XCTAssertTrue(text.contains("SIL OPEN FONT LICENSE Version 1.1"), "the licence text is missing")
    }
}
