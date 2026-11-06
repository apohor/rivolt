import XCTest
@testable import Rivolt

final class PlannerNetworksTests: XCTestCase {
    private func net(_ name: String, slug: String?, preferred: Bool?) -> ChargingNetwork {
        ChargingNetwork(name: name, pricePerKwh: 0, currency: "USD", slug: slug, preferred: preferred)
    }

    func testOnlyTheBuiltInNetworksAreListedPreferredFirstThenByName() {
        let rows = [net("Zeta", slug: "z", preferred: nil), net("My own", slug: nil, preferred: nil), net("Alpha", slug: "a", preferred: nil), net("Mid", slug: "m", preferred: true)]
        XCTAssertEqual(PlannerNetworksSheet.listed(rows).map { rows[$0].name }, ["Mid", "Alpha", "Zeta"])
    }

    func testOnlyAChangedFlagIsAChange() {
        let a = [net("A", slug: "a", preferred: false), net("B", slug: "b", preferred: nil)]
        XCTAssertFalse(PlannerNetworksSheet.changed(a, from: a))
        var b = a
        b[1].preferred = false
        XCTAssertFalse(PlannerNetworksSheet.changed(b, from: a), "nil and false are both not preferred")
        b[0].preferred = true
        XCTAssertTrue(PlannerNetworksSheet.changed(b, from: a))
    }

    func testTheCopyPromisesNothing() {
        XCTAssertTrue(PlannerNetworksSheet.honesty.contains("not guaranteed"))
        XCTAssertNil(PlannerNetworksSheet.honesty.range(of: "will use|always|guarantee[ds]? (to|you)", options: .regularExpression))
    }
}
