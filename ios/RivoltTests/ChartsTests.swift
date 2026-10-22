import SwiftUI
import XCTest
@testable import Rivolt

private func calendar(_ zone: String) -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: zone)!
    return c
}

private func date(_ s: String) -> Date { JSON.parseDate(s)! }

final class ChartMathTests: XCTestCase {
    func testFixturesDecode() throws {
        let p = try Fixture.decode(ChartPoints.self, "chart_points")
        XCTAssertEqual(p.drives.count, 3)
        XCTAssertEqual(p.charges.count, 1)
        let s = try Fixture.decode([DayActivity].self, "sleep_activity")
        XCTAssertEqual(s.count, 7)
        XCTAssertEqual(s[0].idleAwakeH, 0.9)
    }

    func testMilesAreBucketedByTheLocalDayEachDriveStarted() throws {
        let p = try Fixture.decode(ChartPoints.self, "chart_points")
        let now = date("2026-10-20T12:00:00Z")
        // In Chicago the 23:40Z drive is still the 18th (18:40 local).
        let chi = ChartMath.milesPerDay(p.drives, days: 3, now: now, calendar: calendar("America/Chicago"))
        XCTAssertEqual(chi.map(\.value), [14.2 + 31.5, 0, 9.8])
        // In Tokyo it is the 19th (08:40 local), and the first drive (23:00) is still the 18th.
        let tokyo = ChartMath.milesPerDay(p.drives, days: 3, now: now, calendar: calendar("Asia/Tokyo"))
        XCTAssertEqual(tokyo.map(\.value), [14.2, 31.5, 9.8])
    }

    func testTheChargeLevelTrendHasBothEndsOfEverythingOldestFirst() throws {
        let p = try Fixture.decode(ChartPoints.self, "chart_points")
        let t = ChartMath.socTrend(drives: p.drives, charges: p.charges)
        XCTAssertEqual(t.map(\.pct), [88, 84, 84, 75, 66, 85, 61, 58])
        XCTAssertEqual(t, t.sorted { $0.at < $1.at })
    }

    func testAnUnknownChargeLevelIsLeftOut() {
        let d = DrivePoint(startedAt: .now, endedAt: .now.addingTimeInterval(60), distanceMi: 1, startSocPct: 0, endSocPct: 50)
        XCTAssertEqual(ChartMath.socTrend(drives: [d], charges: []).map(\.pct), [50])
    }

    func testIdleDaysLandOnTheSameCalendarDateLocally() throws {
        let s = try Fixture.decode([DayActivity].self, "sleep_activity")
        let cal = calendar("America/Chicago")
        let bars = ChartMath.idleMinutes(s, calendar: cal)
        XCTAssertEqual(bars.count, 7)
        XCTAssertEqual(bars[0].value, 54, accuracy: 0.001)
        let c = cal.dateComponents([.month, .day], from: bars[0].day)
        XCTAssertEqual(c.month, 10)
        XCTAssertEqual(c.day, 15)
    }

    func testTheIdleAverageSkipsToday() throws {
        let s = try Fixture.decode([DayActivity].self, "sleep_activity")
        let cal = calendar("UTC")
        let bars = ChartMath.idleMinutes(s, calendar: cal)
        let avg = try XCTUnwrap(ChartMath.averageIdle(bars, now: date("2026-10-21T15:00:00Z"), calendar: cal))
        let full = s.dropLast().map { $0.idleAwakeH * 60 }
        XCTAssertEqual(avg, full.reduce(0, +) / Double(full.count), accuracy: 0.001)
        XCTAssertNil(ChartMath.averageIdle([], now: .now, calendar: cal))
    }

    func testTheSpokenSummaryNamesTheAverageAndTheBiggestDay() throws {
        let p = try Fixture.decode(ChartPoints.self, "chart_points")
        let cal = calendar("America/Chicago")
        let bars = ChartMath.milesPerDay(p.drives, days: 3, now: date("2026-10-20T12:00:00Z"), calendar: cal)
        let said = ChartMath.spokenDaily(title: "Miles per day", days: 3, bars: bars, calendar: cal) { "\(Int($0.rounded())) miles" }
        XCTAssertEqual(said.label, "Miles per day, last 3 days")
        XCTAssertTrue(said.value.hasPrefix("average 1"), said.value)
        XCTAssertTrue(said.value.hasSuffix("most 46 miles on Sunday"), said.value)
        XCTAssertEqual(ChartMath.spokenMinutes(72), "1 hour 12 minutes")
        let level = ChartMath.spokenLevel(days: 3, trend: ChartMath.socTrend(drives: p.drives, charges: p.charges))
        XCTAssertEqual(level.value, "from 88 percent to 58 percent, lowest 58 percent, highest 88 percent")
        XCTAssertEqual(ChartMath.spokenDaily(title: "x", days: 7, bars: [], calendar: cal) { "\($0)" }.value, "")
    }

    func testTheWindowIsWholeLocalDaysWithAnExclusiveEnd() {
        let cal = calendar("America/Chicago")
        let r = ChartWindow.week.range(now: date("2026-10-21T15:00:00Z"), calendar: cal)
        XCTAssertEqual(r.since, date("2026-10-15T05:00:00Z"))
        XCTAssertEqual(r.until, date("2026-10-22T05:00:00Z"))
    }
}

private actor Calls {
    var points = 0
    var sleep = 0
    func bumpPoints() { points += 1 }
    func bumpSleep() { sleep += 1 }
}

private struct FakeSource: ChartsSource {
    let calls = Calls()
    var failPoints = false
    func chartPoints(since: Date, until: Date, vehicleID: String) async throws -> ChartPoints {
        await calls.bumpPoints()
        if failPoints { throw APIError.transport("down") }
        return ChartPoints(drives: [], charges: [])
    }
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity] {
        await calls.bumpSleep()
        return []
    }
}

@MainActor
final class ChartsModelTests: XCTestCase {
    func testAnAnswerIsReusedForFifteenMinutesPerTruckAndPeriod() async {
        let src = FakeSource()
        let model = ChartsModel()
        let t0 = date("2026-10-21T15:00:00Z")
        await model.load(source: src, vehicleID: "a", window: .week, now: t0)
        await model.load(source: src, vehicleID: "a", window: .week, now: t0.addingTimeInterval(14 * 60))
        var n = await src.calls.points
        XCTAssertEqual(n, 1)
        await model.load(source: src, vehicleID: "a", window: .month, now: t0)
        await model.load(source: src, vehicleID: "b", window: .week, now: t0)
        n = await src.calls.points
        XCTAssertEqual(n, 3)
        await model.load(source: src, vehicleID: "a", window: .week, now: t0.addingTimeInterval(16 * 60))
        n = await src.calls.points
        XCTAssertEqual(n, 4)
    }

    func testAPullReadsAgain() async {
        let src = FakeSource()
        let model = ChartsModel()
        await model.load(source: src, vehicleID: "a", window: .week)
        await model.load(source: src, vehicleID: "a", window: .week, force: true)
        let n = await src.calls.points
        XCTAssertEqual(n, 2)
    }

    func testAFailedReadLeavesItsChartEmptyAndIsRetried() async {
        var src = FakeSource()
        src.failPoints = true
        let model = ChartsModel()
        await model.load(source: src, vehicleID: "a", window: .week)
        XCTAssertNil(model.points)
        XCTAssertNotNil(model.sleep)
        await model.load(source: src, vehicleID: "a", window: .week)
        let n = await src.calls.points
        XCTAssertEqual(n, 2, "a failure isn't cached as an answer")
    }
}
