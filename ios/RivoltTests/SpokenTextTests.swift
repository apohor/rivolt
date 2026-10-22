import XCTest
@testable import Rivolt

/// What VoiceOver reads: no raw units, symbols or separators in a card's sentence.
final class SpokenTextTests: XCTestCase {
    private let forbidden = ["·", "~", "→", "kWh", "kW ", " mph", "mi/", "km/", "%", "—", " est."]

    private func assertPlain(_ s: String, file: StaticString = #filePath, line: UInt = #line) {
        for f in forbidden { XCTAssertFalse(s.contains(f), "\"\(f)\" is read out raw in: \(s)", file: file, line: line) }
    }

    func testUnitsSymbolsAndSeparatorsBecomeWords() {
        XCTAssertEqual(SpokenText.words("3 drives · avg 40.8 mi"), "3 drives, avg 40.8 miles")
        XCTAssertEqual(SpokenText.words("pack-side · 122 mi / 47 kWh"), "pack-side, 122 miles / 47 kilowatt hours")
        XCTAssertEqual(SpokenText.words("2.61 mi/kWh"), "2.61 miles per kilowatt hour")
        XCTAssertEqual(SpokenText.words("0.290 USD/mi · 4 sessions"), "0.290 USD per mile, 4 sessions")
        XCTAssertEqual(SpokenText.words("85% → 80%"), "85 percent to 80 percent")
        XCTAssertEqual(SpokenText.words("at ~0.130 USD/kWh from your most recent charge"), "at about 0.130 USD per kilowatt hour from your most recent charge")
        XCTAssertEqual(SpokenText.words("Updated 5 min ago · since 8:28 PM (−3%)"), "Updated 5 minutes ago, since 8:28 PM (minus 3 percent)")
        XCTAssertEqual(SpokenText.words("1h 5m"), "1 hours 5 minutes")
        XCTAssertEqual(SpokenText.words("55 mph / 88 km/h"), "55 miles per hour / 88 kilometres per hour")
        XCTAssertEqual(SpokenText.words("198.0 kW"), "198.0 kilowatts")
        XCTAssertEqual(SpokenText.words("+22% this charge"), "plus 22 percent this charge")
    }

    func testEveryKpiTileReadsAsWords() throws {
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        let ledger = try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly")
        for metric in [false, true] {
            let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: SinceCharge(miles: 9.8, drives: 1, since: .now),
                                       sinceKnown: false, ledger: ledger, ledgerPending: false, metric: metric)
            for t in tiles { assertPlain(t.spoken) }
        }
        for d in a.drives { for t in ActivityLogic.tiles(d, metric: false) { assertPlain(t.spoken) } }
        for c in a.charges { for t in ActivityLogic.tiles(c) { assertPlain(t.spoken) } }
    }

    func testTheNowCardReadsAsWordsInEveryState() throws {
        let v = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        func summary(soc: Double, gear: String = "P", power: String? = nil, kw: Double? = nil, sleep: Bool = false, kph: Double? = nil, age: TimeInterval = 20) -> NowSummary {
            let input = NowSummary.Input(socPct: soc, rangeKm: 325, chargeTargetPct: 80, gear: gear, powerState: sleep ? "sleep" : "ready", chargerState: power,
                                         chargerPowerKw: kw, at: at.addingTimeInterval(-age), packKwh: 131, speedKph: kph)
            return NowSummary(input: input, now: nil, clock: at)
        }
        let live = try Fixture.decode(LiveSession.self, "live_session_home")
        let drive = try Fixture.decode(LiveDrive.self, "live_drive")
        let states: [NowSummary] = [
            summary(soc: 52, power: "charging_active", kw: 7.2), summary(soc: 31, power: "charging_active", kw: 142), summary(soc: 58, gear: "D", kph: 88),
            summary(soc: 67), summary(soc: 67, sleep: true, age: 60), summary(soc: 80, power: "charger_connected", kw: 0), summary(soc: 7),
            summary(soc: 67, age: 47 * 3600), summary(soc: 41, age: 300),
        ]
        for s in states {
            assertPlain(NowCard(vehicle: v, summary: s, clock: at, place: "Home", session: live, liveDrive: drive).spoken)
        }
    }

    func testTheFeedRowsReadAsWords() throws {
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        for d in a.drives { assertPlain(ActivityLogic.spoken(d, calendar: utc)) }
        for c in a.charges { assertPlain(ActivityLogic.spoken(c, calendar: utc)) }
    }
}
