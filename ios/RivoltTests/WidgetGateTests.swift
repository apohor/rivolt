import SwiftUI
import WidgetKit
import XCTest
@testable import Rivolt

/// The free plan keeps the small Now widget; every other widget is Rivolt Pro (lib/proCopy.ts, support.md, the store listing).
final class WidgetGateTests: XCTestCase {
    private func me(_ name: String) throws -> AuthUser { try Fixture.decode(AuthUser.self, name) }
    private func store() -> WidgetStore { WidgetStore(defaults: UserDefaults(suiteName: "rivolt.tests.widgetgate.\(UUID().uuidString)")!) }

    private let families: [WidgetFamily] = [.systemSmall, .systemMedium, .accessoryCircular, .accessoryRectangular, .accessoryInline]

    func testTheFreePlanLocksEveryFamilyButTheSmallNowWidget() {
        for f in families { XCTAssertEqual(WidgetGate.locks(f, proLocked: true), f != .systemSmall, "\(f)") }
        for f in families { XCTAssertFalse(WidgetGate.locks(f, proLocked: false), "\(f)") }
    }

    func testOnlyAFreeOrLapsedAccountIsLocked() throws {
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_free")), true)
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_lapsed")), true)
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_subscriber")), false)
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_cancelled")), false)
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_grace")), false)
        XCTAssertEqual(WidgetGate.lockedBy(try me("me_plan_founding")), false)
        // Billing off (self-hosted) and an account the server sent no plan for are never locked.
        XCTAssertEqual(WidgetGate.lockedBy(try me("me")), false)
    }

    func testAnAbsentPlanLeavesTheStoredAnswerAlone() throws {
        let noPlan = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username": "a", "user_id": "u"}"#.utf8))
        XCTAssertNil(WidgetGate.lockedBy(noPlan))
        let s = store()
        XCTAssertTrue(WidgetGate.record(try me("me_plan_free"), store: s))
        XCTAssertFalse(WidgetGate.record(noPlan, store: s))
        XCTAssertTrue(s.proLocked)
    }

    func testTheStoreStartsUnlockedAndOnlyReportsARealChange() throws {
        let s = store()
        XCTAssertFalse(s.proLocked)
        XCTAssertTrue(WidgetGate.record(try me("me_plan_free"), store: s))
        XCTAssertTrue(s.proLocked)
        XCTAssertFalse(WidgetGate.record(try me("me_plan_free"), store: s))
        XCTAssertTrue(WidgetGate.record(try me("me_plan_subscriber"), store: s))
        XCTAssertFalse(s.proLocked)
    }

    func testSigningOutClearsTheLock() throws {
        let s = store()
        WidgetGate.record(try me("me_plan_free"), store: s)
        s.clear()
        XCTAssertFalse(s.proLocked)
    }

    func testTheTimelineCarriesTheLockOnEveryEntry() {
        let now = Date()
        let snap = WidgetSnapshot.sample
        for locked in [true, false] {
            let entries = WidgetTimelineBuilder.entries(.data(snap), at: now, proLocked: locked)
            XCTAssertFalse(entries.isEmpty)
            for e in entries { XCTAssertEqual(e.proLocked, locked) }
        }
    }

    func testThePaywallNamesTheSameFreeWidget() {
        let all = ProCopy.included().joined(separator: "\n")
        XCTAssertTrue(all.contains("every widget except the small Now widget"))
        XCTAssertTrue(all.contains("the small Now widget"))
        XCTAssertFalse(all.contains("Lock Screen and Home Screen widgets"))
        XCTAssertEqual(ProCopy.subtitle, "Full history, multi-day trips and more widgets, from $3.99 a month.")
    }

    /// The view itself: a locked free account sees the same small widget, and a different (Pro) card in every other family.
    @MainActor
    func testTheViewLocksEveryFamilyButTheSmallOne() throws {
        let sizes: [(WidgetFamily, CGSize)] = [(.systemSmall, CGSize(width: 170, height: 170)), (.systemMedium, CGSize(width: 364, height: 170)),
                                               (.accessoryRectangular, CGSize(width: 172, height: 76)), (.accessoryCircular, CGSize(width: 76, height: 76))]
        func png(_ f: WidgetFamily, _ size: CGSize, locked: Bool) throws -> Data {
            let entry = NowEntry(date: Date(timeIntervalSince1970: 1_800_000_000), content: .data(.sample), proLocked: locked)
            let r = ImageRenderer(content: NowWidgetView(entry: entry, family: f).frame(width: size.width, height: size.height).environment(\.colorScheme, .dark))
            r.scale = 1
            return try XCTUnwrap(r.uiImage?.pngData())
        }
        for (f, size) in sizes {
            let same = try png(f, size, locked: true) == png(f, size, locked: false)
            XCTAssertEqual(same, f == .systemSmall, "\(f)")
        }
    }
}

private final class MeStub: URLProtocol {
    nonisolated(unsafe) static var fixture = "me_plan_free"
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: (try? Fixture.data(Self.fixture)) ?? Data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private struct StoredToken: TokenStore {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// The plan reaches the widgets on every read of /me, not only at launch: buying Pro unlocks them without a restart.
@MainActor
final class WidgetPlanReadTests: XCTestCase {
    func testAProAnswerAfterALockedOneUnlocksTheWidgetsAndReloadsThem() async throws {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [MeStub.self]
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "widget-plan-\(UUID().uuidString)"))
        let s = AppState(tokens: StoredToken(), defaults: defaults, session: URLSession(configuration: c))
        let store = WidgetStore()
        let before = store.proLocked
        defer { store.setProLocked(before) }
        MeStub.fixture = "me_plan_free"
        await s.bootstrap()
        var reloads = 0
        s.reloadWidgets = { reloads += 1 }
        store.setProLocked(false)
        _ = try await s.readMe()
        XCTAssertTrue(store.proLocked)
        XCTAssertEqual(reloads, 1)
        _ = try await s.readMe()
        XCTAssertEqual(reloads, 1, "no change, no reload")
        MeStub.fixture = "me_plan_subscriber"
        _ = try await s.readMe()
        XCTAssertFalse(store.proLocked)
        XCTAssertEqual(reloads, 2)
    }
}
