import SwiftUI
import XCTest
@testable import Rivolt

/// The charging curve on two sessions: a DC fast charge (a peak, a taper, a warming pack) and a night on a home charger.
@MainActor
final class ChargeCurveSnapshotTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private let start = Date(timeIntervalSince1970: 1_800_000_000)

    private func dcFast() -> (samples: [ChargeSample], end: Date) {
        // 18% to 74% in 41 minutes: a ramp to 150 kW at the start, a long taper, the pack warming from 22 to 41 C.
        var out: [ChargeSample] = []
        for i in 0...82 {
            let t = Double(i) * 30
            let f = t / 2_460
            let kw = f < 0.06 ? 40 + f / 0.06 * 110 : max(25, 150 - (f - 0.06) * 150)
            let pack = 22 + 19 * f
            out.append(ChargeSample(at: start.addingTimeInterval(t), batteryLevelPct: 18 + 56 * f + (i % 3 == 0 ? 0.4 : 0),
                                    chargerPowerKw: kw + Double(i % 5) * 2, packTempAvgC: pack, packTempMinC: pack - 2, packTempMaxC: pack + 3))
        }
        return (out, start.addingTimeInterval(2_460))
    }

    private func home() -> (samples: [ChargeSample], end: Date) {
        // 55% to 85% overnight on a 7.2 kW charger.
        var out: [ChargeSample] = []
        for i in 0...90 {
            let t = Double(i) * 200
            let f = t / 18_000
            out.append(ChargeSample(at: start.addingTimeInterval(t), batteryLevelPct: (55 + 30 * f).rounded(),
                                    chargerPowerKw: f < 0.97 ? 7.2 - 0.6 * f : 1.1, packTempAvgC: nil, packTempMinC: nil, packTempMaxC: nil))
        }
        return (out, start.addingTimeInterval(18_000))
    }

    private func render(_ name: String, _ content: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            let png = try XCTUnwrap(image.pngData())
            let file = "\(name)-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
    }

    func testRenderTheCurves() throws {
        let dc = dcFast()
        let dcSeries = ChargeCurve.series(dc.samples, from: start, to: dc.end)
        let h = home()
        let homeSeries = ChargeCurve.series(h.samples, from: start, to: h.end)
        XCTAssertFalse(dcSeries.soc.isEmpty)
        XCTAssertFalse(dcSeries.packAvgC.isEmpty)
        XCTAssertTrue(homeSeries.packAvgC.isEmpty)
        func card(_ series: ChargeCurve.Series, _ a: Double, _ b: Double, _ kw: Double) -> some View {
            ThemeSection(title: "Charge") {
                ChargeCurveView(series: series, startPct: a, endPct: b, peakKw: kw, calendar: utc, metric: false)
            }
            .padding(16).frame(width: 390).background(ThemePageBackground())
        }
        try render("charge-curve-dc", card(dcSeries, 18, 74, 152))
        try render("charge-curve-home", card(homeSeries, 55, 85, 7.2))
    }
}
