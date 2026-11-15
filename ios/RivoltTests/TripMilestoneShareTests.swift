import UIKit
import XCTest
@testable import Rivolt

private func chicago() -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "America/Chicago")!
    c.locale = Locale(identifier: "en_US")
    return c
}

/// The trip and milestone share cards: what is on them, what is not, and that they paint (renders for review land in
/// SNAPSHOT_DIR on CI).
final class TripMilestoneShareTests: XCTestCase {
    /// A polyline6 encoder for the fixtures.
    private static func encode6(_ pts: [(Double, Double)]) -> String {
        var out = "", pl = 0, pn = 0
        func put(_ v: Int) {
            var x = v < 0 ? ~(v << 1) : v << 1
            while x >= 0x20 { out.append(Character(UnicodeScalar(UInt8((0x20 | (x & 0x1f)) + 63)))); x >>= 5 }
            out.append(Character(UnicodeScalar(UInt8(x + 63))))
        }
        for (a, o) in pts {
            let la = Int((a * 1e6).rounded()), lo = Int((o * 1e6).rounded())
            put(la - pl); put(lo - pn); pl = la; pn = lo
        }
        return out
    }

    private func drive() throws -> ActivityDrive {
        try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-3" })
    }

    private func milestone() throws -> Milestone {
        try XCTUnwrap(Fixture.decode(MilestonesResponse.self, "milestones").milestones.first)
    }

    private func text(_ c: MoneyShareCard) -> String {
        ([c.eyebrow, c.title, c.subtitle] + c.stats.flatMap { [$0.label, $0.value] } + c.notes).joined(separator: " ")
    }

    func testTheMinutesAreCutLikeTheDetailRowNeverRoundedUp() {
        XCTAssertEqual(MoneyShareCard.tripDuration(minutes: 40 + 50.0 / 60), "40 min")
        XCTAssertEqual(ActivityLogic.shortDuration(40 * 60 + 50), "40m")
        XCTAssertEqual(MoneyShareCard.tripDuration(minutes: 65.9), "1 h 5 min")
    }

    func testARoundTripCardCountsDrivingTimeNotTheParkedGaps() throws {
        let d = try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity_round_trip").drives.first)
        XCTAssertEqual(d.drivingSeconds, 3480)
        XCTAssertGreaterThan(d.endedAt.timeIntervalSince(d.startedAt), 3480 + 60, "the wall clock holds the parked gaps")
        let c = MoneyShareCard.trip(d, includeCost: false, calendar: chicago())
        XCTAssertTrue(c.title.hasSuffix("· 58 min"), c.title)
        XCTAssertEqual(ActivityLogic.shortDuration(ActivityLogic.duration(d) ?? 0), "58m")
    }

    func testTheTripCardHoldsTheDayTheDistanceAndTheDurationOnly() throws {
        let d = try drive()
        let c = MoneyShareCard.trip(d, includeCost: false, calendar: chicago())
        XCTAssertEqual(c.eyebrow, "Trip")
        XCTAssertEqual(c.title, "19 mi · 1 h 5 min")
        // The day, never the time of day.
        XCTAssertEqual(c.subtitle, "Sunday, September 20")
        XCTAssertFalse(text(c).contains("$"))
        XCTAssertFalse(c.stats.contains { $0.label.contains("cost") })
        XCTAssertEqual(c.stats.first?.label, "Efficiency")
        XCTAssertEqual(c.stats.first?.value, "\(NumberText.fixed(d.distanceMi / d.energyUsedKwh, 2)) mi/kWh")
        // The model has nothing that could carry a place, an id or a truck; the only route is the opt-in map's trimmed segments (empty here).
        XCTAssertEqual(Mirror(reflecting: c).children.compactMap(\.label), ["eyebrow", "title", "subtitle", "stats", "notes", "routeSegments"])
        XCTAssertEqual(c.routeSegments, [])
    }

    func testTripCostIsOptIn() throws {
        let d = try drive()
        XCTAssertGreaterThan(d.estimatedCost ?? 0, 0)
        let on = MoneyShareCard.trip(d, includeCost: true, calendar: chicago())
        let stat = try XCTUnwrap(on.stats.first { $0.label == "Charging cost (estimate)" })
        XCTAssertTrue(stat.value.hasPrefix("~$"), stat.value)
        XCTAssertEqual(MoneyShareCard.trip(d, includeCost: false, calendar: chicago()).stats.count, on.stats.count - 1)
    }

    func testTheMapAddsOnlyTheServersSegmentsAndNoWords() throws {
        let d = try drive()
        let segs = ["_p~iF~ps|U_ulLnnqC", "_p~iF~ps|U"]
        let off = MoneyShareCard.trip(d, includeCost: false, calendar: chicago())
        let on = MoneyShareCard.trip(d, includeCost: false, routeSegments: segs, calendar: chicago())
        XCTAssertEqual(on.routeSegments, segs)
        XCTAssertEqual(text(on), text(off))
        XCTAssertEqual(on.stats, off.stats)
    }

    func testTheFileNamesAreGeneric() throws {
        let d = try drive()
        XCTAssertEqual(MoneyShareCard.fileName(kind: "trip", date: d.startedAt, calendar: chicago()), "rivolt-trip-2026-09.png")
        XCTAssertEqual(MoneyShareCard.fileName(kind: "milestone", date: try milestone().achievedAt, calendar: chicago()), "rivolt-milestone-2026-10.png")
    }

    func testTheMilestoneCardIsTheServersWordsAndTheMonth() throws {
        let m = try milestone()
        let c = MoneyShareCard.milestone(m, calendar: chicago())
        XCTAssertEqual(c.eyebrow, "Milestone · October 2026")
        XCTAssertEqual(c.title, m.title)
        XCTAssertEqual(c.subtitle, m.body)
        XCTAssertEqual(c.stats, [])
    }

    func testAMilestoneNeverCarriesADollarFigure() throws {
        XCTAssertTrue(MoneyShareCard.hasMoney("About $142 at your home rate."))
        XCTAssertTrue(MoneyShareCard.hasMoney("saved 12 USD"))
        XCTAssertFalse(MoneyShareCard.hasMoney("2.85 mi/kWh over 120 mi"))
        let m = Milestone(vehicleId: "v", key: "k", kind: "kwh_1000", achievedAt: try milestone().achievedAt, title: "1,000 kWh charged at home",
                          body: "About $142 at your home rate.")
        let c = MoneyShareCard.milestone(m, calendar: chicago())
        XCTAssertEqual(c.eyebrow, "Milestone · October 2026")
        XCTAssertEqual(c.subtitle, "")
        XCTAssertFalse(text(c).contains("$"))
    }

    @MainActor
    func testBothCardsPaintA1080SquarePNG() throws {
        Theme.registerFonts()
        // A made-up winding line (polyline6), nowhere real, in two stretches.
        let line = (0...120).map { i -> (Double, Double) in
            let t = Double(i) / 120
            return (10 + t * 0.4 + 0.05 * sin(t * 9), 20 + t * 0.7 + 0.06 * cos(t * 7))
        }
        let route = [Self.encode6(Array(line[5..<70])), Self.encode6(Array(line[80..<118]))]
        let cards = [("trip-share-card", MoneyShareCard.trip(try drive(), includeCost: true, calendar: chicago())),
                     ("trip-share-card-map", MoneyShareCard.trip(try drive(), includeCost: true, routeSegments: route, calendar: chicago())),
                     ("milestone-share-card", MoneyShareCard.milestone(try milestone(), calendar: chicago()))]
        for (name, card) in cards {
            let png = try XCTUnwrap(MoneyShareCardCanvas.png(card))
            XCTAssertEqual(try XCTUnwrap(UIImage(data: png)).size, CGSize(width: 1080, height: 1080))
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = "\(name).png"
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
            }
        }
    }
}
