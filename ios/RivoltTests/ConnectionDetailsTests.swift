import SwiftUI
import XCTest
@testable import Rivolt

/// The Connection details screen: what it says, and what it never carries.
@MainActor
final class ConnectionDetailsTests: XCTestCase {
    private let t0 = JSON.parseDate("2026-10-23T15:00:00Z")!

    private func summary(frameAgo: TimeInterval, gear: String = "park") -> NowSummary {
        let input = NowSummary.Input(socPct: 62, rangeKm: 230, chargeTargetPct: 80, gear: gear, powerState: "ready", chargerState: nil,
                                     chargerPowerKw: nil, at: t0.addingTimeInterval(-frameAgo))
        return NowSummary(input: input, now: nil, clock: t0)
    }

    func testTheServerRowIsTheHostAndPortOnly() {
        XCTAssertEqual(ConnectionDetails.host(of: URL(string: "https://rivolt.dev")!), "rivolt.dev")
        XCTAssertEqual(ConnectionDetails.host(of: URL(string: "https://rivolt.dev:8443/some/path?token=abc")!), "rivolt.dev:8443")
        XCTAssertEqual(ConnectionDetails.host(of: URL(string: "https://me:secret@home.example:9000/x")!), "home.example:9000")
        XCTAssertFalse(ConnectionDetails.host(of: URL(string: "https://me:secret@home.example/")!).contains("secret"))
    }

    func testAnOutcomeIsAFixedClassNeverTheErrorsText() {
        let leaky = "https://rivolt.dev/api/vehicles/01-000000001/state?token=abc"
        let cases: [(Error, ConnectionDiagnostics.Outcome)] = [
            (APIError.server(status: 503, message: leaky), .http(503)),
            (APIError.transport(leaky), .unreachable),
            (APIError.decoding(NSError(domain: leaky, code: 1)), .unreadable),
            (URLError(.timedOut, userInfo: [NSURLErrorFailingURLStringErrorKey: leaky]), .timedOut),
            (URLError(.notConnectedToInternet), .noNetwork),
            (URLError(.cannotFindHost), .unreachable),
            (NSError(domain: leaky, code: 7), .failed),
        ]
        for (error, want) in cases {
            let got = ConnectionDiagnostics.Outcome(error)
            XCTAssertEqual(got, want)
            XCTAssertFalse(got.text.contains("rivolt.dev") || got.text.contains("token") || got.text.contains("01-0"), got.text)
        }
        XCTAssertEqual(ConnectionDiagnostics.Outcome.http(503).text, "HTTP 503")
    }

    func testTheReadsAreRecordedThroughTheLoggedWrapper() async {
        let d = ConnectionDiagnostics()
        let ok: Int? = await NowReads.logged("/now", record: { d.record(.now, $0, at: self.t0) }) { 1 }
        XCTAssertEqual(ok, 1)
        let failed: Int? = await NowReads.logged("/state", record: { d.record(.state, $0, at: self.t0) }) { throw URLError(.timedOut) }
        XCTAssertNil(failed)
        XCTAssertEqual(d.now, .init(at: t0, outcome: .ok))
        XCTAssertEqual(d.state, .init(at: t0, outcome: .timedOut))
        XCTAssertEqual(ConnectionDetails.readLine(d.state, clock: t0.addingTimeInterval(125)), "Timed out, 2 min ago")
        XCTAssertEqual(ConnectionDetails.readLine(nil, clock: t0), "Not read yet")
    }

    func testTheLimitsAreTheOnesPresenceUses() {
        XCTAssertEqual(summary(frameAgo: 10, gear: "park").limits.offline, 900)
        XCTAssertEqual(summary(frameAgo: 10, gear: "D").limits.stale, 30)
        // The presence follows the same numbers: parked, a frame 20 minutes old is offline, 10 minutes old is stale.
        XCTAssertEqual(summary(frameAgo: 20 * 60).presence(holding: false), .offline)
        XCTAssertEqual(summary(frameAgo: 10 * 60).presence(holding: false), .stale)
        XCTAssertEqual(ConnectionDetails.span(900), "15 min")
        XCTAssertEqual(ConnectionDetails.span(30), "30 s")
    }

    func testAgoIsReadable() {
        XCTAssertEqual(ConnectionDetails.ago(t0, clock: t0.addingTimeInterval(12)), "12 s ago")
        XCTAssertEqual(ConnectionDetails.ago(t0, clock: t0.addingTimeInterval(190)), "3 min ago")
        XCTAssertEqual(ConnectionDetails.ago(t0, clock: t0.addingTimeInterval(7500)), "2 h 5 min ago")
    }

    /// The screen for the owner's "truck shows Offline", in the three ways it happens, rendered for review.
    func testRendersExplainWhyTheTruckSaysOffline() throws {
        Theme.registerFonts()
        let url = URL(string: "https://rivolt.dev")!
        let scenarios: [(String, ConnectionDiagnostics)] = {
            let a = ConnectionDiagnostics()      // the server answers; the truck's frame is old
            a.record(.now, .ok, at: t0.addingTimeInterval(-8)); a.record(.state, .ok, at: t0.addingTimeInterval(-8))
            a.update(summary: summary(frameAgo: 42 * 60), freshness: { var f = NowFreshness(); f.fetched(at: t0.addingTimeInterval(-8)); return f }())
            let b = ConnectionDiagnostics()      // the server does not answer
            b.record(.now, .http(502), at: t0.addingTimeInterval(-20)); b.record(.state, .timedOut, at: t0.addingTimeInterval(-20))
            b.update(summary: summary(frameAgo: 30 * 60), freshness: { var f = NowFreshness(); f.fetched(at: t0.addingTimeInterval(-600)); return f }())
            let c = ConnectionDiagnostics()      // a recent frame: live
            c.record(.now, .ok, at: t0.addingTimeInterval(-5)); c.record(.state, .ok, at: t0.addingTimeInterval(-5))
            c.update(summary: summary(frameAgo: 45), freshness: { var f = NowFreshness(); f.fetched(at: t0.addingTimeInterval(-5)); return f }())
            return [("conn-frame-old", a), ("conn-server-down", b), ("conn-live", c)]
        }()
        for (name, d) in scenarios {
            for scheme in [ColorScheme.light, .dark] {
                let view = ConnectionDetailsContent(diagnostics: d, serverURL: url, clock: t0).padding(16).frame(width: 390)
                    .background(ThemePageBackground()).environment(\.colorScheme, scheme)
                let r = ImageRenderer(content: view.fixedSize(horizontal: false, vertical: true))
                r.scale = 3
                let png = try XCTUnwrap(r.uiImage?.pngData(), name)
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
    }
}
