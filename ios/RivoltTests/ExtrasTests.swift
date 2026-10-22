import SwiftUI
import XCTest
@testable import Rivolt

private actor Calls {
    var dismissed: [String] = []
    func record(_ key: String) { dismissed.append(key) }
}

private struct FakeExtras: ExtrasSource {
    let calls = Calls()
    var milestonesList: [Milestone] = []
    var lease: Bool? = false
    var statement: StatementEmailSetting? = StatementEmailSetting(enabled: false, available: true)
    var failDismiss = false

    func milestones() async throws -> [Milestone] { milestonesList }
    func dismissMilestone(vehicleID: String, key: String) async throws {
        if failDismiss { throw APIError.transport("down") }
        await calls.record(key)
    }
    func hasLease(vehicleID: String) async throws -> Bool {
        guard let lease else { throw APIError.transport("down") }
        return lease
    }
    func statementEmail() async throws -> StatementEmailSetting {
        guard let statement else { throw APIError.transport("down") }
        return statement
    }
}

final class ExtrasDecodeTests: XCTestCase {
    func testTheServersFixturesDecode() throws {
        let m = try Fixture.decode(MilestonesResponse.self, "milestones").milestones
        XCTAssertEqual(m.count, 2)
        XCTAssertEqual(m[0].title, "Your most efficient drive yet")
        XCTAssertEqual(m[0].id, "veh-fixture:best_eff:2026-10-20")
        let s = try Fixture.decode(StatementEmailSetting.self, "statement_email")
        XCTAssertEqual(s, StatementEmailSetting(enabled: false, available: true))
    }

    func testTheRowsFollowWhatIsOff() {
        let on = StatementEmailSetting(enabled: true, available: true)
        let off = StatementEmailSetting(enabled: false, available: true)
        XCTAssertEqual(ThreeThingsRow.rows(pushNeeded: true, hasLease: false, statement: off), [.push, .lease, .statement])
        XCTAssertEqual(ThreeThingsRow.rows(pushNeeded: false, hasLease: true, statement: on), [])
        // The email is only offered where the server has it.
        XCTAssertEqual(ThreeThingsRow.rows(pushNeeded: false, hasLease: true, statement: StatementEmailSetting(enabled: false, available: false)), [])
        // A read that failed leaves its row out instead of guessing.
        XCTAssertEqual(ThreeThingsRow.rows(pushNeeded: false, hasLease: nil, statement: nil), [])
    }
}

@MainActor
final class ExtrasModelTests: XCTestCase {
    private func defaults() -> UserDefaults {
        let d = UserDefaults(suiteName: "extras-tests-\(UUID().uuidString)")!
        return d
    }

    private func two() throws -> [Milestone] { try Fixture.decode(MilestonesResponse.self, "milestones").milestones }

    func testOnlyThisTrucksNewestMilestoneShowsAndHidingShowsTheNext() async throws {
        var src = FakeExtras()
        src.milestonesList = try two()
        let model = ExtrasModel(defaults: defaults())
        await model.load(source: src, vehicleID: "veh-fixture")
        XCTAssertNil(model.current(vehicleID: "other-truck"))
        let first = try XCTUnwrap(model.current(vehicleID: "veh-fixture"))
        XCTAssertEqual(first.key, "best_eff:2026-10-20")
        await model.dismiss(first, source: src)
        XCTAssertEqual(model.current(vehicleID: "veh-fixture")?.key, "miles_1000")
        let sent = await src.calls.dismissed
        XCTAssertEqual(sent, ["best_eff:2026-10-20"])
    }

    func testARefusedHideBringsTheMilestoneBack() async throws {
        var src = FakeExtras()
        src.milestonesList = try two()
        src.failDismiss = true
        let model = ExtrasModel(defaults: defaults())
        await model.load(source: src, vehicleID: "veh-fixture")
        let first = try XCTUnwrap(model.current(vehicleID: "veh-fixture"))
        await model.dismiss(first, source: src)
        XCTAssertEqual(model.current(vehicleID: "veh-fixture"), first)
        XCTAssertTrue(model.dismissFailed)
    }

    func testHidingThreeThingsLastsOnThisPhoneOnly() async {
        let d = defaults()
        let src = FakeExtras()
        let model = ExtrasModel(defaults: d)
        await model.load(source: src, vehicleID: "v")
        XCTAssertFalse(model.rows(pushNeeded: true).isEmpty)
        model.hideThreeThings()
        XCTAssertTrue(model.rows(pushNeeded: true).isEmpty)
        XCTAssertTrue(ExtrasModel(defaults: d).threeThingsHidden)
    }

    func testNothingShowsBeforeTheReadsFinish() {
        XCTAssertTrue(ExtrasModel(defaults: defaults()).rows(pushNeeded: true).isEmpty)
    }
}
