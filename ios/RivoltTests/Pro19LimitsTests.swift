import XCTest
@testable import Rivolt

final class Pro19LimitsTests: XCTestCase {
    private func vehicles(_ json: String) throws -> [Vehicle] {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode([Vehicle].self, from: Data(json.utf8))
    }

    private let list = """
    [{"id":"v-a","vin":"AAA","name":"Alpha","model":"R1T","pro_locked":false},
     {"id":"v-b","vin":"BBB","name":"Bravo","model":"R1S","pro_locked":true},
     {"id":"v-c","vin":"CCC","name":"Charlie","model":"R1S"}]
    """

    func testProLockedDecodesAndIsAbsentWithBillingOff() throws {
        let v = try vehicles(list)
        XCTAssertEqual(v.map(\.proLocked), [false, true, nil])
    }

    func testALockedVehicleIsNeverTheDefault() throws {
        let v = try vehicles(list)
        XCTAssertEqual(VehicleChoice.id(v, chosen: "v-b"), "v-a")
        XCTAssertEqual(VehicleChoice.id(v, chosen: ""), "v-a")
        XCTAssertEqual(VehicleChoice.id(v, chosen: "v-b", pickedLocked: true), "v-b")
        XCTAssertNil(VehicleChoice.id([], chosen: "v-a"))
    }

    func testLabelMarksLockedVehicles() throws {
        let v = try vehicles(list)
        XCTAssertEqual(VehicleChoice.label(v[0]), "Alpha")
        XCTAssertEqual(VehicleChoice.label(v[1]), "Bravo · Pro")
        XCTAssertEqual(VehicleChoice.label(v[2]), "Charlie")
    }

    func testPrimaryVehicleRowOnlyOnAFreePlan() {
        func rows(_ plan: Bool, _ free: Bool) -> [SettingsLogic.Row] {
            SettingsLogic.groups(notificationsOffered: false, planOffered: plan, primaryVehicleOffered: free)
                .first { $0.title == "Vehicle" }!.rows
        }
        XCTAssertTrue(rows(true, true).contains(.primaryVehicle))
        XCTAssertFalse(rows(true, false).contains(.primaryVehicle))
        XCTAssertTrue(rows(false, true).contains(.primaryVehicle), "the server limits a free account whether or not this build bills")
    }

    /// The exact shape prod sends the iPhone today: plan free, billing_enabled false (RIVOLT_BILLING_IOS unset), no manage_subscription.
    func testFreeAccountOnABuildThatDoesNotBillStillGetsThePrimaryVehicleRowAndNoPurchaseUI() throws {
        let me = try Fixture.decode(AuthUser.self, "me_plan_free_bearer_billing_off")
        XCTAssertEqual(PlanLogic.state(me), .off, "billing off: no plan screen, no prices, no See Pro")
        XCTAssertTrue(PlanLogic.primaryVehicleLimited(me))
        let rows = SettingsLogic.groups(notificationsOffered: false, planOffered: PlanLogic.state(me) != .off, primaryVehicleOffered: PlanLogic.primaryVehicleLimited(me)).flatMap(\.rows)
        XCTAssertTrue(rows.contains(.primaryVehicle))
        XCTAssertFalse(rows.contains(.plan), "no Rivolt Pro row, so no paywall")
        XCTAssertNil(ProCopy.card(PlanLogic.state(me)))
    }

    func testPrimaryVehicleLimitedIsFalseForProDemoAndBillingOff() throws {
        XCTAssertFalse(PlanLogic.primaryVehicleLimited(nil))
        XCTAssertFalse(PlanLogic.primaryVehicleLimited(try Fixture.decode(AuthUser.self, "me_plan_subscriber")))
        XCTAssertFalse(PlanLogic.primaryVehicleLimited(try Fixture.decode(AuthUser.self, "me_plan_billing_off_manage")))
        XCTAssertTrue(PlanLogic.primaryVehicleLimited(try Fixture.decode(AuthUser.self, "me_plan_lapsed")))
        XCTAssertTrue(PlanLogic.primaryVehicleLimited(try Fixture.decode(AuthUser.self, "me_plan_free")))
    }

    func testLimitSentences() {
        XCTAssertTrue(ProCopy.plannerLimit.contains("single-day plan stays free"))
        // A refused single-day plan means all planning is Pro for the account: facts only (D7), nothing to buy, no price, no link.
        let refused = APIError.server(status: 402, message: "pro_required", retryAfter: nil)
        XCTAssertEqual(TripPlanLogic.failure(refused, singleDay: true), ProCopy.plannerAllPro)
        XCTAssertEqual(TripPlanLogic.failure(refused), ProCopy.plannerLimit)
        XCTAssertEqual(ProCopy.plannerGated(hasSavedTrips: false), "Trip planning is part of the Pro tier.")
        XCTAssertTrue(ProCopy.plannerGated(hasSavedTrips: true).hasSuffix("you can open and delete them."))
        for word in ["$", "rivolt.dev", "http", "Upgrade", "Buy"] { XCTAssertFalse(ProCopy.plannerGated(hasSavedTrips: true).contains(word), word) }
        XCTAssertTrue(DemoLinks.forNote(limit: .proRequired, message: ProCopy.plannerAllPro).isEmpty)
        for word in ["$", "rivolt.dev", "http", "Upgrade", "Buy"] { XCTAssertFalse(ProCopy.plannerAllPro.contains(word), word) }
        XCTAssertTrue(ProCopy.primaryVehicleIntro.contains("Nothing is deleted"))
        XCTAssertEqual(ProCopy.primaryVehicleSaved("Alpha"), "Done. The free tier now shows Alpha.")
    }
}

final class VehicleScopeTests: XCTestCase {
    private func v(_ id: String, _ model: String, _ name: String) throws -> Vehicle {
        let json = #"{"id":"\#(id)","vin":"\#(id)","name":"\#(name)","model":"\#(model)"}"#
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode(Vehicle.self, from: Data(json.utf8))
    }

    func testNamesTheModelAndTheNicknameOnlyWhenTwoShareAModel() throws {
        let mixed = [try v("a", "R1T", "Rex"), try v("b", "R1S", "Sue")]
        XCTAssertEqual(VehicleScope.name(mixed, id: "a"), "R1T")
        let same = [try v("a", "R1S", "Rex"), try v("b", "R1S", "Sue")]
        XCTAssertEqual(VehicleScope.name(same, id: "b"), "Sue")
        XCTAssertNil(VehicleScope.name(mixed, id: "zzz"))
        XCTAssertEqual(VehicleScope.sentence("R1T"), "Showing R1T. Pro includes all vehicles.")
    }

    func testOnlyTheLimitedRoutesCount() {
        XCTAssertTrue(VehicleScope.isScoped("/api/ledger/monthly?month=2026-10"))
        XCTAssertTrue(VehicleScope.isScoped("/api/drives?limit=5"))
        XCTAssertTrue(VehicleScope.isScoped("/api/ledger/monthly.csv"))
        XCTAssertFalse(VehicleScope.isScoped("/api/vehicles"))
        XCTAssertFalse(VehicleScope.isScoped("/api/year-in-rivolt"))
    }

    @MainActor func testHeaderSetsAndAbsenceClears() {
        let s = VehicleScope()
        s.note(path: "/api/vehicles", header: "a")
        XCTAssertNil(s.id)
        s.note(path: "/api/drives?limit=5", header: "a")
        XCTAssertEqual(s.id, "a")
        s.note(path: "/api/activity?month=x", header: nil)
        XCTAssertNil(s.id)
    }
}

final class DeleteHistoryLinkTests: XCTestCase {
    func testOpensTheWebSettingsAnchor() {
        let u = DeleteHistoryLink.url(URL(string: "https://rivolt.dev")!)
        XCTAssertEqual(u.absoluteString, "https://rivolt.dev/settings#delete-history")
        XCTAssertEqual(DeleteHistoryLink.caption(URL(string: "https://rivolt.dev")!), "Opens rivolt.dev, where you confirm. You may need to sign in.")
        XCTAssertEqual(DeleteHistoryLink.caption(URL(string: "https://ev.example.org:8443")!), "Opens ev.example.org, where you confirm. You may need to sign in.")
    }

    func testSettingsDataGroupListsIt() {
        let data = SettingsLogic.groups(notificationsOffered: false).first { $0.title == "Data" }!.rows
        XCTAssertEqual(data, [.export, .deleteHistory, .deleteAccount])
    }

    func testOnlyDataNotesOfferTheDelete() {
        XCTAssertTrue(ProLimitNote(limit: .outsideFreeWindow).offersDelete)
        XCTAssertTrue(ProLimitNote(limit: .proRequired, message: ProCopy.vehicleLimit).offersDelete)
        XCTAssertFalse(ProLimitNote(limit: .proRequired, message: ProCopy.plannerLimit).offersDelete)
        XCTAssertFalse(ProLimitNote(limit: .proRequired).offersDelete)
    }
}
