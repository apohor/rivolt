import UIKit
import XCTest
@testable import Rivolt

/// The trip receipt (design_preview key trip_receipt): what the card says from the server's receipt, and that it paints.
final class TripReceiptTests: XCTestCase {
    private let la = TimeZone(identifier: "America/Los_Angeles")!
    private let us = Locale(identifier: "en_US")

    private func receipt(_ extra: String = "", rows: String? = nil, sketch: String? = nil) throws -> TripReceipt {
        let rows = rows ?? """
        [{"kind":"drive","from":"Trinidad area","to":"Pueblo area","dist_m":32187,"dur_s":1800,"from_soc":90,"to_soc":70,"rounded":true},
         {"kind":"stop","name":"Stop · Pueblo area","dur_s":300,"from_soc":70,"to_soc":70},
         {"kind":"charge","name":"Example Charger","dur_s":1080,"from_soc":40,"to_soc":80,"max_kw":149.6},
         {"kind":"drive","from":"Example Charger","to":"Walsenburg area","dist_m":48280,"dur_s":2700,"from_soc":80,"to_soc":30,"rounded":true}]
        """
        let json = """
        {"depart_at":"2026-11-21T15:00:00Z","tz":"America/Los_Angeles","start_soc":90,"start":"Trinidad area","end":"Walsenburg area","rows":\(rows),
         "totals":{"dist_m":120701,"drive_s":6300,"charge_s":1080,"stops":1,"arrive_at":"2026-11-21T17:05:00Z","arrival_soc":31.5}\(sketch.map { ",\"sketch\":[\"\($0)\"]" } ?? "")\(extra)}
        """
        return try JSON.decoder().decode(TripReceipt.self, from: Data(json.utf8))
    }

    private func cost(_ low: Double?, _ high: Double?) throws -> TripCost {
        let parts = [low.map { "\"dcfc_spend_low\":\($0)" }, high.map { "\"dcfc_spend_high\":\($0)" }, "\"currency\":\"USD\""].compactMap { $0 }
        return try JSON.decoder().decode(TripCost.self, from: Data("{\(parts.joined(separator: ","))}".utf8))
    }

    /// The clock text puts a narrow no-break space before AM/PM; the words are compared with a plain one.
    private func said(_ s: String?) -> String? { s?.replacingOccurrences(of: "\u{202F}", with: " ") }

    private func model(_ r: TripReceipt, metric: Bool = false, sketch: Bool = false, cost: TripCost? = nil, includeCost: Bool = false) -> TripReceiptModel {
        TripReceiptLogic.model(r, vehicle: "R1T", metric: metric, includeSketch: sketch, includeCost: includeCost, cost: cost, viewerZone: la, locale: us)
    }

    func testTheCardSaysTheRowsAndTotalsInPlainWords() throws {
        let m = model(try receipt())
        XCTAssertEqual(m.head.0, "RIVOLT · TRIP PLAN")
        XCTAssertEqual(m.head.1, "R1T")
        XCTAssertEqual(m.title, "Trinidad area → Walsenburg area")
        XCTAssertEqual(said(m.sub), "Leave Sat, Nov 21 · 7:00 AM PST · start with 90%")
        XCTAssertEqual(m.lines.map(\.title), ["DRIVE · ~20 mi", "Stop · Pueblo area", "CHARGE · 150 kW", "DRIVE · ~30 mi"])
        XCTAssertEqual(m.lines[0].right, "~30 min")
        XCTAssertEqual(m.lines[0].sub, "Trinidad area → Pueblo area")
        XCTAssertEqual(m.lines[0].subRight, "90% → 70%")
        XCTAssertEqual(m.lines[1].right, "5 min")
        XCTAssertEqual(m.lines[1].subRight, "", "an added stop shows the battery only when it changes")
        XCTAssertEqual(m.lines[2].right, "18 min")
        XCTAssertEqual(m.totals.map(\.label), ["Driving", "Charging · 1 stop", "Arrive"])
        XCTAssertEqual(m.totals.map { said($0.value)! }, ["~1 h 45 min", "18 min", "~9:05 AM PST · 32%"])
        XCTAssertEqual(m.big.0, "~75 mi")
        XCTAssertEqual(m.big.1, "~2 h 03 min")
        XCTAssertEqual(m.footer, TripReceiptLogic.footer)
    }

    func testTheArrivalClockIsInTheDestinationsZone() throws {
        let ny = TripReceiptLogic.model(try receipt(), vehicle: "R1T", metric: false, includeSketch: false, includeCost: false, cost: nil,
                                        viewerZone: TimeZone(identifier: "America/New_York")!, locale: us)
        XCTAssertTrue(said(ny.sub)!.contains("10:00 AM EST"), "the leave time is the viewer's: \(ny.sub)")
        XCTAssertEqual(said(ny.totals.last?.value), "~9:05 AM PST · 32%", "the arrival is the destination's")
    }

    func testMetricAndTheUnderOneStepLeg() throws {
        let rows = """
        [{"kind":"drive","from":"A area","to":"B area","dist_m":300,"dur_s":120,"from_soc":90,"to_soc":89,"rounded":true},
         {"kind":"drive","from":"B area","to":"C area","dist_m":40000,"dur_s":1800,"from_soc":89,"to_soc":60}]
        """
        let m = model(try receipt(rows: rows), metric: true)
        XCTAssertEqual(m.lines[0].title, "DRIVE · < 5 km")
        XCTAssertEqual(m.lines[0].right, "~2 min")
        XCTAssertEqual(m.lines[1].title, "DRIVE · 40 km", "an exact leg carries no tilde")
        XCTAssertEqual(m.big.0, "~121 km")
    }

    func testTheCostLineIsOnlyThereWhenAskedForAndEstimated() throws {
        let r = try receipt()
        XCTAssertNil(model(r, cost: try cost(12, 15), includeCost: false).cost, "off unless asked")
        XCTAssertNil(model(r, cost: nil, includeCost: true).cost, "nothing to add")
        XCTAssertNil(model(r, cost: try cost(0, 0), includeCost: true).cost)
        XCTAssertEqual(model(r, cost: try cost(12, 15), includeCost: true).cost, .init(label: "Charging cost (estimate)", value: "$12–$15"))
        XCTAssertEqual(model(r, cost: try cost(14.2, 14.4), includeCost: true).cost?.value, "$14")
        XCTAssertEqual(model(r, cost: try cost(nil, 9), includeCost: true).cost?.value, "$9")
    }

    func testTheSketchIsInTheCardOnlyWhenAskedFor() throws {
        let r = try receipt(sketch: "_p~iF~ps|U_ulLnnqC_mqNvxq`@")
        XCTAssertNil(model(r, sketch: false).sketch)
        XCTAssertEqual(model(r, sketch: true).sketch?.count, 1)
        XCTAssertNil(model(try receipt(), sketch: true).sketch, "the server sent none")
    }

    func testNoDepartureTimeStartsTheLineWithTheCharge() throws {
        var r = try receipt()
        r.departAt = nil
        XCTAssertEqual(model(r).sub, "Start with 90%")
    }

    func testTheCardNamesNoPlaceButTheServersAreas() throws {
        let m = model(try receipt())
        XCTAssertFalse(m.text.contains("Seattle"))
        XCTAssertEqual(TripReceiptLogic.fileName, "rivolt-trip-plan.png", "no place, date or name in the file name")
    }

    func testFailureWordsNeverEchoTheServer() {
        XCTAssertEqual(TripReceiptLogic.failure(APIError.server(status: 422, message: "secret")), "This trip is too short for a receipt.")
        XCTAssertTrue(TripReceiptLogic.failure(APIError.server(status: 409, message: "secret")).hasPrefix("A receipt isn't available"))
        XCTAssertEqual(TripReceiptLogic.failure(APIError.server(status: 500, message: "secret")), "Couldn't make the receipt. Try again.")
    }

    @MainActor
    func testItPaintsAPngAsTallAsTheReceiptNeeds() throws {
        let r = try receipt(sketch: "_p~iF~ps|U_ulLnnqC_mqNvxq`@")
        let plain = try XCTUnwrap(TripReceiptCanvas.png(model(r)).flatMap(UIImage.init(data:)))
        let sketch = try XCTUnwrap(TripReceiptCanvas.png(model(r, sketch: true)).flatMap(UIImage.init(data:)))
        XCTAssertEqual(plain.size.width, 1080)
        XCTAssertTrue((900...2400).contains(plain.size.height), "a receipt as tall as it needs to be: \(plain.size.height)")
        XCTAssertEqual(sketch.size.height - plain.size.height, 220, accuracy: 1, "the sketch adds its band")
        let long = try receipt(rows: "[" + (0..<12).map { _ in #"{"kind":"drive","from":"A area","to":"B area","dist_m":80000,"dur_s":3600,"from_soc":90,"to_soc":60,"rounded":false}"# }.joined(separator: ",") + "]")
        let tall = try XCTUnwrap(TripReceiptCanvas.png(model(long)).flatMap(UIImage.init(data:)))
        XCTAssertGreaterThan(tall.size.height, plain.size.height + 700, "every row is on the paper")
    }

    /// Writes the receipt PNGs when RECEIPT_DUMP names a directory and RECEIPT_JSON the server's receipt (not part of the suite otherwise).
    @MainActor
    func testDumpReceiptPNGs() throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["RECEIPT_DUMP"], let file = env["RECEIPT_JSON"] else { throw XCTSkip("RECEIPT_DUMP / RECEIPT_JSON not set") }
        let r = try JSON.decoder().decode(TripReceipt.self, from: Data(contentsOf: URL(fileURLWithPath: file)))
        for (name, sketch, withCost) in [("off", false, false), ("sketch", true, false), ("sketch-cost", true, true)] {
            let m = model(r, sketch: sketch, cost: try cost(24, 31), includeCost: withCost)
            try XCTUnwrap(TripReceiptCanvas.png(m)).write(to: URL(fileURLWithPath: dir).appendingPathComponent("ios-receipt-\(name).png"))
        }
    }
}
