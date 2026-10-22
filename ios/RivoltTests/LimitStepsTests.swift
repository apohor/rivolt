import XCTest
@testable import Rivolt

/// The charge limit as steps, the web's `limitSteps` cases (SocTrendChart.steps.test.tsx) on the same data.
final class LimitStepsTests: XCTestCase {
    private let oct = Date(timeIntervalSince1970: 1_790_812_800)   // 2026-10-01T00:00:00Z
    private var nov: Date { oct.addingTimeInterval(31 * 86_400) }
    private func at(_ day: Double, _ hour: Double = 0) -> Date { oct.addingTimeInterval((day - 1) * 86_400 + hour * 3_600) }
    private var now: Date { at(22, 12) }

    private func charge(_ day: Double, _ limit: Double?, hours: Double = 2) -> ChargePoint {
        ChargePoint(startedAt: at(day, 8), endedAt: at(day, 8 + hours), startSocPct: 40, endSocPct: 80, chargeLimitPct: limit)
    }

    private func step(_ a: Date, _ b: Date, _ pct: Double) -> ChartMath.LimitStep { .init(start: a, end: b, pct: pct) }

    func testTheOriginIsOctober1st() {
        let utc = ISO8601DateFormatter().date(from: "2026-10-01T00:00:00Z")
        XCTAssertEqual(oct, utc)
    }

    func testStepsAtEachChargeWithALimitCarryOverUnknownsThenTheLiveTargetToNow() {
        let steps = ChartMath.limitSteps([charge(3, 80), charge(8, nil), charge(12, 90), charge(20, 0)], from: oct, to: nov, now: now, live: 70)
        XCTAssertEqual(steps, [
            step(at(3, 8), at(12, 8), 80),
            step(at(12, 8), at(12, 10), 90),
            step(at(12, 10), now, 70),
        ])
    }

    func testStepsAtTheSameValueAreJoinedAndTheLastLimitStaysWithoutALiveTarget() {
        XCTAssertEqual(ChartMath.limitSteps([charge(3, 80), charge(9, 80)], from: oct, to: nov, now: now, live: nil), [step(at(3, 8), now, 80)])
        XCTAssertEqual(ChartMath.limitSteps([charge(3, 80), charge(9, 80)], from: oct, to: nov, now: now, live: 80), [step(at(3, 8), now, 80)])
    }

    func testA100PercentLimitIsDrawnNothingBeforeTheFirstOneAndNothingWhenNoneHasOne() {
        XCTAssertEqual(ChartMath.limitSteps([charge(5, 100)], from: oct, to: nov, now: now, live: nil), [step(at(5, 8), now, 100)])
        XCTAssertEqual(ChartMath.limitSteps([charge(5, nil), charge(6, 0)], from: oct, to: nov, now: now, live: 70), [])
        XCTAssertEqual(ChartMath.limitSteps([charge(5, 101)], from: oct, to: nov, now: now, live: 70), [])
    }

    func testAPastMonthEndsAtItsEndWithItsOwnChargesLimitsAndNoLiveTarget() {
        let sep = oct.addingTimeInterval(-30 * 86_400)
        let c = ChargePoint(startedAt: sep.addingTimeInterval(2 * 86_400), endedAt: sep.addingTimeInterval(2 * 86_400 + 3_600), startSocPct: 40, endSocPct: 80, chargeLimitPct: 80)
        XCTAssertEqual(ChartMath.limitSteps([c], from: sep, to: oct, now: now, live: 70), [step(c.startedAt, oct, 80)])
    }

    func testChargesOutsideThePeriodAreIgnored() {
        let before = ChargePoint(startedAt: oct.addingTimeInterval(-86_400), endedAt: oct.addingTimeInterval(-82_800), startSocPct: 40, endSocPct: 80, chargeLimitPct: 60)
        XCTAssertEqual(ChartMath.limitSteps([before, charge(10, 80)], from: oct, to: nov, now: now, live: nil), [step(at(10, 8), now, 80)])
    }

    func testTheLimitDecodesFromTheFixtureAndAbsentOrNullIsUnknown() throws {
        let p = try Fixture.decode(ChartPoints.self, "chart_points")
        XCTAssertEqual(p.charges[0].chargeLimitPct, 80)
        let json = """
        {"drives":[],"charges":[
          {"started_at":"2026-10-19T21:00:00Z","ended_at":"2026-10-20T04:00:00Z","start_soc_pct":66,"end_soc_pct":85,"charge_limit_pct":null},
          {"started_at":"2026-10-21T21:00:00Z","ended_at":"2026-10-22T04:00:00Z","start_soc_pct":66,"end_soc_pct":85}
        ]}
        """
        let q = try JSON.decoder().decode(ChartPoints.self, from: Data(json.utf8))
        XCTAssertNil(q.charges[0].chargeLimitPct, "null: unknown")
        XCTAssertNil(q.charges[1].chargeLimitPct, "absent: an older server")
        XCTAssertEqual(ChartMath.limitSteps(q.charges, from: oct, to: nov, now: now, live: 70), [])
    }
}
