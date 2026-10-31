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
        XCTAssertFalse(rows(false, true).contains(.primaryVehicle), "billing off: no plan wording anywhere")
    }

    func testLimitSentences() {
        XCTAssertTrue(ProCopy.plannerLimit.contains("single-day plan stays free"))
        XCTAssertTrue(ProCopy.primaryVehicleIntro.contains("Nothing is deleted"))
        XCTAssertEqual(ProCopy.primaryVehicleSaved("Alpha"), "Done. The free plan now shows Alpha.")
    }
}
