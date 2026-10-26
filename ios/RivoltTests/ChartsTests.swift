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
        func days(_ cal: Calendar) -> [Date] {
            ["2026-10-18T12:00:00Z", "2026-10-19T12:00:00Z", "2026-10-20T12:00:00Z"].map { cal.startOfDay(for: date($0)) }
        }
        // In Chicago the 23:40Z drive is still the 18th (18:40 local).
        let chicago = calendar("America/Chicago")
        let chi = ChartMath.milesPerDay(p.drives, over: days(chicago), calendar: chicago)
        XCTAssertEqual(chi.map(\.value), [14.2 + 31.5, 0, 9.8])
        // In Tokyo it is the 19th (08:40 local), and the first drive (23:00) is still the 18th.
        let tokyo = calendar("Asia/Tokyo")
        let tok = ChartMath.milesPerDay(p.drives, over: days(tokyo), calendar: tokyo)
        XCTAssertEqual(tok.map(\.value), [14.2, 31.5, 9.8])
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
        let days = ["2026-10-18T12:00:00Z", "2026-10-19T12:00:00Z", "2026-10-20T12:00:00Z"].map { cal.startOfDay(for: date($0)) }
        let bars = ChartMath.milesPerDay(p.drives, over: days, calendar: cal)
        let said = ChartMath.spokenDaily(title: "Miles per day", period: "October 2026", bars: bars, calendar: cal) { "\(Int($0.rounded())) miles" }
        XCTAssertEqual(said.label, "Miles per day, October 2026")
        XCTAssertTrue(said.value.hasPrefix("average 1"), said.value)
        XCTAssertTrue(said.value.hasSuffix("most 46 miles on Sunday"), said.value)
        XCTAssertEqual(ChartMath.spokenMinutes(72), "1 hour 12 minutes")
        XCTAssertEqual(ChartMath.spokenDaily(title: "x", period: "p", bars: [], calendar: cal) { "\($0)" }.value, "")
    }

    func testAMonthIsWholeLocalDaysWithAnExclusiveEnd() {
        let cal = calendar("America/Chicago")
        let m = PeriodMonth(containing: date("2026-10-21T15:00:00Z"), calendar: cal)
        let r = m.range(calendar: cal)
        XCTAssertEqual(r.since, date("2026-10-01T05:00:00Z"))
        XCTAssertEqual(r.until, date("2026-11-01T05:00:00Z"))
        XCTAssertEqual(m.title(calendar: cal), "Oct 2026")
        XCTAssertTrue(m.isCurrent(now: date("2026-10-31T23:00:00Z"), calendar: cal))
        XCTAssertFalse(m.shifted(by: -1, calendar: cal).isCurrent(now: date("2026-10-21T15:00:00Z"), calendar: cal))
        XCTAssertEqual(m.shifted(by: -1, calendar: cal).range(calendar: cal).until, r.since)
    }

    func testAChartCoversTheWholeMonthOrThroughTodayInTheCurrentOne() {
        let cal = calendar("UTC")
        let now = date("2026-10-21T15:00:00Z")
        let thisMonth = PeriodMonth(containing: now, calendar: cal)
        XCTAssertEqual(thisMonth.days(now: now, calendar: cal).count, 21)
        XCTAssertEqual(thisMonth.shifted(by: -1, calendar: cal).days(now: now, calendar: cal).count, 30, "September")
        XCTAssertEqual(thisMonth.days(now: now, calendar: cal).first, date("2026-10-01T00:00:00Z"))
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
    private let m1 = PeriodMonth(containing: date("2026-10-15T12:00:00Z"), calendar: calendar("UTC"))
    private let m2 = PeriodMonth(containing: date("2026-09-15T12:00:00Z"), calendar: calendar("UTC"))

    func testAnAnswerIsReusedForFifteenMinutesPerTruckAndPeriod() async {
        let src = FakeSource()
        let model = ChartsModel()
        let t0 = date("2026-10-21T15:00:00Z")
        await model.load(source: src, vehicleID: "a", month: m1, now: t0)
        await model.load(source: src, vehicleID: "a", month: m1, now: t0.addingTimeInterval(14 * 60))
        var n = await src.calls.points
        XCTAssertEqual(n, 1)
        await model.load(source: src, vehicleID: "a", month: m2, now: t0)
        await model.load(source: src, vehicleID: "b", month: m1, now: t0)
        n = await src.calls.points
        XCTAssertEqual(n, 3)
        await model.load(source: src, vehicleID: "a", month: m1, now: t0.addingTimeInterval(16 * 60))
        n = await src.calls.points
        XCTAssertEqual(n, 4)
    }

    func testAPullReadsAgain() async {
        let src = FakeSource()
        let model = ChartsModel()
        await model.load(source: src, vehicleID: "a", month: m1)
        await model.load(source: src, vehicleID: "a", month: m1, force: true)
        let n = await src.calls.points
        XCTAssertEqual(n, 2)
    }

    func testAFailedReadLeavesItsChartEmptyAndIsRetried() async {
        var src = FakeSource()
        src.failPoints = true
        let model = ChartsModel()
        await model.load(source: src, vehicleID: "a", month: m1)
        XCTAssertNil(model.points)
        XCTAssertNotNil(model.sleep)
        await model.load(source: src, vehicleID: "a", month: m1)
        let n = await src.calls.points
        XCTAssertEqual(n, 2, "a failure isn't cached as an answer")
    }
}

final class ChartAxisTests: XCTestCase {
    func testTheMilesAxisHasARoundTopAndRoundTicksLikeTheWebs() {
        func axis(_ m: Double) -> ([Double], Double) { let a = ChartMath.niceAxis(max: m); return (a.ticks, a.top) }
        XCTAssertEqual(axis(509).0, [0, 200, 400, 600])
        XCTAssertEqual(axis(509).1, 600)
        XCTAssertEqual(axis(500).0, [0, 250, 500])
        XCTAssertEqual(axis(97).0, [0, 50, 100])
        XCTAssertEqual(axis(42).0, [0, 25, 50])
        XCTAssertEqual(axis(7).0, [0, 5, 10])
        XCTAssertEqual(axis(1).0, [0, 1])
        XCTAssertEqual(axis(0).0, axis(1).0, "no data: the smallest axis")
        XCTAssertEqual(axis(.nan).0, axis(1).0)
    }

    func testTheTopNeverCutsTheDataOffAndTicksAreWholeNumbers() {
        for m in [0.4, 1, 3, 9, 23, 77, 136, 509, 999, 1500, 12345] as [Double] {
            let a = ChartMath.niceAxis(max: m)
            XCTAssertGreaterThanOrEqual(a.top, m, "\(m)")
            XCTAssertEqual(a.ticks.last, a.top)
            XCTAssertLessThanOrEqual(a.ticks.count, 5)
            XCTAssertTrue(a.ticks.allSatisfy { $0 == $0.rounded() }, "\(m)")
        }
    }

    func testTheIdleAxisRoundsToHoursLikeTheWebs() {
        func ticks(_ m: Double) -> [Double] { ChartMath.niceMinutesAxis(max: m).ticks }
        XCTAssertEqual(ticks(1500), [0, 720, 1440, 2160])
        XCTAssertEqual(ticks(1380), [0, 480, 960, 1440], "8h · 16h · 24h, not 8h 20m · 16h 40m · 25h")
        XCTAssertEqual(ticks(1000), [0, 360, 720, 1080])
        XCTAssertEqual(ticks(95), [0, 60, 120])
        XCTAssertEqual(ticks(40), [0, 15, 30, 45])
        for m in [1, 20, 59, 100, 500, 1500, 3000, 20000] as [Double] {
            let a = ChartMath.niceMinutesAxis(max: m)
            XCTAssertGreaterThanOrEqual(a.top, m)
            XCTAssertEqual(a.ticks.last, a.top)
            XCTAssertLessThanOrEqual(a.ticks.count, 5)
        }
        XCTAssertEqual(TruckLogic.minutesShort(480), "8h")
    }

    func testDaysAreNamedInTheReadersLanguageNotAsTenDashOne() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        cal.locale = Locale(identifier: "en_US")
        let oct1 = ISO8601DateFormatter().date(from: "2026-10-01T00:00:00Z")!
        XCTAssertEqual(ChartsSection.dayLabel(oct1, calendar: cal), "Oct 1")
        XCTAssertNotEqual(ChartsSection.dayLabel(oct1, calendar: cal), "10-01")
    }

    func testEveryChartsAxisTextIsOneSizeTheCaptionScale() {
        XCTAssertEqual(ThemeTokens.typeChartAxis, 11)
        XCTAssertEqual(ThemeTokens.typeChartAxis, ThemeTokens.typeCaption)
    }
}

final class ChartInspectTests: XCTestCase {
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        c.locale = Locale(identifier: "en_US")
        return c
    }
    private func day(_ d: Int) -> Date { cal.date(from: DateComponents(year: 2026, month: 10, day: d))! }
    private var miles: [DayValue] { [DayValue(day: day(8), value: 0), DayValue(day: day(9), value: 12.34), DayValue(day: day(10), value: 31.0)] }

    func testAPressOnTheMilesChartReadsTheDayItIsOverWithTheWebsFigure() {
        let hit = ChartMath.inspectedDay(day(9).addingTimeInterval(15 * 3600), among: miles, calendar: cal)
        XCTAssertEqual(hit?.value, 12.34)
        XCTAssertEqual(ChartMath.milesReadout(hit!, unit: "mi", calendar: cal), "Oct 9 · 12.3 mi", "one decimal, as the web's tooltip")
        XCTAssertNil(ChartMath.inspectedDay(day(20), among: miles, calendar: cal), "a press outside the data reads nothing")
        XCTAssertNil(ChartMath.inspectedDay(nil, among: miles, calendar: cal))
    }

    func testVoiceOverStepsDayByDayAndStopsAtTheEnds() {
        XCTAssertEqual(ChartMath.steppedDay(from: nil, step: 1, among: miles), miles.first)
        XCTAssertEqual(ChartMath.steppedDay(from: nil, step: -1, among: miles), miles.last)
        XCTAssertEqual(ChartMath.steppedDay(from: miles[0], step: 1, among: miles), miles[1])
        XCTAssertEqual(ChartMath.steppedDay(from: miles[2], step: 1, among: miles), miles[2], "no wrapping past the last day")
        XCTAssertEqual(ChartMath.steppedDay(from: miles[0], step: -1, among: miles), miles[0])
        XCTAssertNil(ChartMath.steppedDay(from: nil, step: 1, among: []))
    }

    func testTheBatteryReadoutIsTheWebsTooltip() {
        let days = [SocDaily.Day(day: day(8), low: 52, high: 97, end: 97, charged: true),
                    SocDaily.Day(day: day(9), low: 22.4, high: 96.6, end: 69.6, charged: false)]
        XCTAssertEqual(SocDaily.readout(days[0], calendar: cal), "Oct 8 · 52–97% · ended 97% · charged")
        XCTAssertEqual(SocDaily.readout(days[1], calendar: cal), "Oct 9 · 22–97% · ended 70%", "rounded as the web's Math.round")
        // A press between two days reads the nearer one; the middle of a day is where its marks sit.
        XCTAssertEqual(SocDaily.inspectedDay(day(9).addingTimeInterval(2 * 3600), among: days)?.day, day(9))
        XCTAssertEqual(SocDaily.inspectedDay(day(8).addingTimeInterval(20 * 3600), among: days)?.day, day(8))
        XCTAssertNil(SocDaily.inspectedDay(nil, among: days))
        XCTAssertEqual(SocDaily.steppedDay(from: days[0], step: 1, among: days), days[1])
    }
}
