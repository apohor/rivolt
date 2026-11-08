import XCTest
@testable import Rivolt

/// ios/design/charger-labels.json is generated from the web's strings and ChargerLabels.swift from it. These fail when the two drift (someone edited
/// one by hand, or regenerated only one), and check the rule that says which type a power is.
final class ChargerLabelsTests: XCTestCase {
    private struct Doc: Decodable {
        struct T: Decodable { let label: String; let sentence: String }
        let types: [String: T]
        let all: String
        let infoLabel: String
    }

    func testTheSwiftMatchesTheJSON() throws {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "charger-labels", withExtension: "json"), "charger-labels.json is not in the test bundle")
        let doc = try JSONDecoder().decode(Doc.self, from: Data(contentsOf: url))
        XCTAssertEqual(Set(doc.types.keys), Set(ChargerKind.allCases.map(\.rawValue)))
        for kind in ChargerKind.allCases {
            let t = try XCTUnwrap(doc.types[kind.rawValue])
            XCTAssertEqual(ChargerLabels.label(kind), t.label, "\(kind) label")
            XCTAssertEqual(ChargerLabels.sentence(kind), t.sentence, "\(kind) sentence")
        }
        XCTAssertEqual(ChargerLabels.all, doc.all)
        XCTAssertEqual(ChargerLabels.infoLabel, doc.infoLabel)
    }

    func testPowerAloneOutletMediumThenFastAboveTheACCeiling() {
        XCTAssertNil(ChargerLabels.kind(kw: nil))
        XCTAssertNil(ChargerLabels.kind(kw: 0))
        XCTAssertEqual(ChargerLabels.kind(kw: 1.4), .outlet)
        XCTAssertEqual(ChargerLabels.kind(kw: 2), .outlet)
        XCTAssertEqual(ChargerLabels.kind(kw: 2.1), .medium)
        XCTAssertEqual(ChargerLabels.kind(kw: 11.9), .medium)
        XCTAssertEqual(ChargerLabels.kind(kw: 12), .medium)
        XCTAssertEqual(ChargerLabels.kind(kw: 12.1), .fast)
        XCTAssertEqual(ChargerLabels.kind(kw: 24), .fast)
        XCTAssertEqual(ChargerLabels.kind(kw: 49.9), .fast)
        XCTAssertEqual(ChargerLabels.kind(kw: 50), .fast)
        XCTAssertEqual(ChargerLabels.kind(kw: 250), .fast)
    }

    func testAStationsConnectorsDecideACOrDCWhenKnown() {
        XCTAssertEqual(ChargerLabels.kind(kw: 22, dc: false), .medium, "a 22 kW AC station is Level 2")
        XCTAssertEqual(ChargerLabels.kind(kw: 24, dc: true), .fast)
        XCTAssertEqual(ChargerLabels.kind(kw: 6, dc: true), .fast)
    }

    func testTheKWIsSaidAsTheStationsOrTheVehicles() {
        XCTAssertEqual(ChargerLabels.stationLine(.fast, ratedKw: 24), "Fast · station up to 24 kW")
        XCTAssertEqual(ChargerLabels.stationLine(.hotel, ratedKw: nil), "Hotel")
        XCTAssertEqual(ChargerLabels.sessionLine(peakKw: 40), "Fast · peak 40 kW")
        XCTAssertNil(ChargerLabels.sessionLine(peakKw: 0))
    }

    func testMoneyCountsASessionUnderFiftyKWAtHomeOrAway() {
        XCTAssertEqual(ChargerLabels.moneyNote(peakKw: 40, ledgerIsDC: false, place: "Away"), "Counted as Away in Money (Fast there means 50 kW and up).")
        XCTAssertEqual(ChargerLabels.moneyNote(peakKw: 40, ledgerIsDC: false, place: "Home"), "Counted as Home in Money (Fast there means 50 kW and up).")
        XCTAssertNil(ChargerLabels.moneyNote(peakKw: 40, ledgerIsDC: true, place: "Fast"), "the ledger already counts it Fast")
        XCTAssertNil(ChargerLabels.moneyNote(peakKw: 60, ledgerIsDC: false, place: "Away"))
        XCTAssertNil(ChargerLabels.moneyNote(peakKw: 9, ledgerIsDC: false, place: "Home"), "Medium is Level 2, nothing differs")
        XCTAssertEqual(ChargerLabels.moneyFastNote, "DC sessions peaking at 50 kW and up.")
    }
}
