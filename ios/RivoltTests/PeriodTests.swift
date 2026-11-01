import XCTest
@testable import Rivolt

/// The phone's period picker, checked against the web's `period.ts`: the same ranges and, character for character, the same labels.
final class PeriodTests: XCTestCase {
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        c.locale = Locale(identifier: "en_US")
        c.firstWeekday = 1 // a Sunday-first phone; the week is still Monday to Sunday
        return c
    }
    private func d(_ y: Int, _ m: Int, _ day: Int, _ h: Int = 0) -> Date { cal.date(from: DateComponents(year: y, month: m, day: day, hour: h))! }
    // Sun 2026-10-25 14:00, the day this was written.
    private var now: Date { d(2026, 10, 25, 14) }

    func testEachKindIsTheCalendarSpanContainingNowAndEndsExclusive() {
        let day = Period(kind: .day, containing: now, calendar: cal)
        XCTAssertEqual(day.start, d(2026, 10, 25)); XCTAssertEqual(day.end, d(2026, 10, 26))
        let week = Period(kind: .week, containing: now, calendar: cal)
        XCTAssertEqual(week.start, d(2026, 10, 19), "a Sunday belongs to the week that started the Monday before")
        XCTAssertEqual(week.end, d(2026, 10, 26))
        let month = Period(containing: now, calendar: cal)
        XCTAssertEqual(month.start, d(2026, 10, 1)); XCTAssertEqual(month.end, d(2026, 11, 1))
        let year = Period(kind: .year, containing: now, calendar: cal)
        XCTAssertEqual(year.start, d(2026, 1, 1)); XCTAssertEqual(year.end, d(2027, 1, 1))
        let custom = Period.custom(from: d(2026, 10, 17), to: d(2026, 10, 3), calendar: cal)
        XCTAssertEqual(custom.start, d(2026, 10, 3), "swapped ends are put right")
        XCTAssertEqual(custom.end, d(2026, 10, 18), "both days are included")
        XCTAssertEqual(Period.allTime(now: now, calendar: cal).end, d(2026, 10, 26))
    }

    func testSteppingGoesBackByTheKindsOwnUnitAndNeverForwardPastNow() {
        let day = Period(kind: .day, containing: now, calendar: cal)
        XCTAssertEqual(day.shifted(by: -1, calendar: cal).start, d(2026, 10, 24))
        XCTAssertFalse(day.shifted(by: -1, calendar: cal).isCurrent(now: now, calendar: cal))
        XCTAssertTrue(day.isCurrent(now: now, calendar: cal), "next is disabled today")
        XCTAssertEqual(Period(kind: .week, containing: now, calendar: cal).shifted(by: -1, calendar: cal).start, d(2026, 10, 12))
        XCTAssertEqual(Period(containing: d(2026, 1, 15), calendar: cal).shifted(by: -1, calendar: cal).start, d(2025, 12, 1), "across a year")
        XCTAssertEqual(Period(kind: .year, containing: now, calendar: cal).shifted(by: -1, calendar: cal).start, d(2025, 1, 1))
        let custom = Period.custom(from: d(2026, 10, 3), to: d(2026, 10, 17), calendar: cal)
        XCTAssertFalse(custom.canStep)
        XCTAssertEqual(custom.shifted(by: -1, calendar: cal), custom)
        XCTAssertFalse(Period.allTime(now: now, calendar: cal).canStep)
    }

    func testTheLabelsAreTheWebsCharacterForCharacter() {
        func t(_ p: Period) -> String { p.title(calendar: cal, now: now) }
        let day = Period(kind: .day, containing: now, calendar: cal)
        XCTAssertEqual(t(day), "Today")
        XCTAssertEqual(t(day.shifted(by: -1, calendar: cal)), "Oct 24")
        XCTAssertEqual(t(day.shifted(by: -300, calendar: cal)), "Dec 29, 2025")
        let week = Period(kind: .week, containing: now, calendar: cal)
        XCTAssertEqual(t(week), "Oct 19–25")
        XCTAssertEqual(t(week.shifted(by: -4, calendar: cal)), "Sep 21–27")
        XCTAssertEqual(t(Period(kind: .week, containing: d(2026, 10, 1), calendar: cal)), "Sep 28–Oct 4", "across a month")
        XCTAssertEqual(t(Period(containing: now, calendar: cal)), "Oct 2026")
        XCTAssertEqual(t(Period(kind: .year, containing: now, calendar: cal)), "2026")
        XCTAssertEqual(t(Period.allTime(now: now, calendar: cal)), "All time")
        XCTAssertEqual(t(Period.custom(from: d(2026, 10, 3), to: d(2026, 10, 17), calendar: cal)), "Oct 3 – Oct 17, 2026")
        XCTAssertEqual(t(Period.custom(from: d(2025, 12, 20), to: d(2026, 1, 5), calendar: cal)), "Dec 20, 2025 – Jan 5, 2026")
    }

    func testTheSpokenWordsFitASentence() {
        func s(_ p: Period) -> String { p.inPhrase(calendar: cal, now: now) }
        XCTAssertEqual(s(Period(kind: .day, containing: now, calendar: cal)), "today")
        XCTAssertEqual(s(Period(kind: .day, containing: now, calendar: cal).shifted(by: -1, calendar: cal)), "on October 24")
        XCTAssertEqual(s(Period(containing: now, calendar: cal)), "in October 2026")
        XCTAssertEqual(s(Period(kind: .week, containing: now, calendar: cal)), "in the week of October 19")
        XCTAssertEqual(s(Period(kind: .year, containing: now, calendar: cal)), "in 2026")
        XCTAssertEqual(s(Period.allTime(now: now, calendar: cal)), "so far")
        XCTAssertEqual(s(Period.custom(from: d(2026, 10, 3), to: d(2026, 10, 17), calendar: cal)), "from October 3 to October 17, 2026")
    }

    func testADayAndAMonthThatStartTogetherAreDifferentReads() {
        let first = d(2026, 10, 1, 9)
        XCTAssertEqual(Period(kind: .day, containing: first, calendar: cal).start, Period(containing: first, calendar: cal).start)
        XCTAssertNotEqual(Period(kind: .day, containing: first, calendar: cal).key, Period(containing: first, calendar: cal).key, "caches key on this")
        XCTAssertNotEqual(Period(kind: .week, containing: d(2026, 9, 28), calendar: cal).key, Period(kind: .day, containing: d(2026, 9, 28), calendar: cal).key)
    }

    func testTheDaysAndAxisLabelsFollowTheWebsRules() {
        let week = Period(kind: .week, containing: now, calendar: cal)
        XCTAssertEqual(week.axisDays(calendar: cal).count, 7, "every day of a week")
        XCTAssertEqual(Period(kind: .day, containing: now, calendar: cal).axisDays(calendar: cal), [d(2026, 10, 25)])
        let oct = Period(containing: now, calendar: cal)
        XCTAssertEqual(oct.axisDays(calendar: cal).map { cal.component(.day, from: $0) }, [1, 11, 21, 31])
        XCTAssertEqual(oct.days(now: now, calendar: cal).count, 25, "a current month's data stops at today")
        XCTAssertEqual(oct.allDays(calendar: cal).count, 31)
        XCTAssertEqual(Period(kind: .year, containing: now, calendar: cal).axisDays(calendar: cal).count, 4, "longer spans: four evenly spaced days")
        XCTAssertEqual(Period(kind: .day, containing: now, calendar: cal).days(now: now, calendar: cal), [d(2026, 10, 25)])
    }

    func testTheChoiceIsRememberedAsKindAndOffsetSoTodayStaysToday() throws {
        let yesterday = Period(kind: .day, containing: now, calendar: cal).shifted(by: -1, calendar: cal)
        let saved = yesterday.stored(now: now, calendar: cal)
        XCTAssertEqual(saved, Period.Stored(kind: .day, offset: -1))
        // Two days later, "yesterday" is still the day before that day.
        XCTAssertEqual(Period(saved, now: d(2026, 10, 27, 9), calendar: cal).start, d(2026, 10, 26))
        let data = try JSONEncoder().encode(saved)
        XCTAssertEqual(try JSONDecoder().decode(Period.Stored.self, from: data), saved)
        let custom = Period.custom(from: d(2026, 10, 3), to: d(2026, 10, 17), calendar: cal)
        XCTAssertEqual(Period(custom.stored(now: now, calendar: cal), now: now, calendar: cal), custom)
        let all = Period.allTime(now: now, calendar: cal)
        XCTAssertEqual(Period(all.stored(now: now, calendar: cal), now: d(2026, 11, 2), calendar: cal).kind, .all)
        XCTAssertEqual(Period(Period.Stored(kind: .custom, from: "nonsense", to: nil), now: now, calendar: cal).kind, .month, "a saved choice that can't be read falls back to this month")
        XCTAssertEqual(Period(Period.Stored(kind: .month, offset: -2), now: now, calendar: cal).start, d(2026, 8, 1))
        XCTAssertEqual(Period(Period.Stored(kind: .year), now: now, calendar: cal).kind, .year, "a saved This year works")
    }

    func testTheSheetFilesYearAndAllUnderCustomAndPicksTheCurrentOne() {
        let year = Period(kind: .year, containing: now, calendar: cal)
        XCTAssertEqual(PeriodSheetLogic.choice(year), .custom)
        XCTAssertEqual(PeriodSheetLogic.choice(Period.allTime(now: now, calendar: cal)), .custom)
        XCTAssertEqual(PeriodSheetLogic.choice(Period(kind: .day, containing: now, calendar: cal)), .day)
        let month = Period(containing: now, calendar: cal)
        XCTAssertEqual(PeriodSheetLogic.pick(.day, from: month, now: now, calendar: cal).kind, .day)
        XCTAssertEqual(PeriodSheetLogic.pick(.week, from: month, now: now, calendar: cal).kind, .week)
        let fromMonth = PeriodSheetLogic.pick(.custom, from: month, now: now, calendar: cal)
        XCTAssertEqual(fromMonth.kind, .custom)
        XCTAssertEqual(fromMonth.title(calendar: cal, now: now), "Sep 26 – Oct 25, 2026", "the last 30 days, ending today")
        XCTAssertEqual(PeriodSheetLogic.pick(.custom, from: year, now: now, calendar: cal), year, "Custom keeps This year when it is in use")
        XCTAssertEqual(PeriodSheetLogic.noun(month), "month")
        XCTAssertEqual(PeriodSheetLogic.noun(Period(kind: .day, containing: now, calendar: cal)), "day")
    }

    // MARK: The rolling last 30 days (the Activity default)

    func testTheLast30DaysAreTodayAndTheTwentyNineBeforeIt() {
        let p = Period.last30(now: now, calendar: cal)
        XCTAssertEqual(p.start, d(2026, 9, 26)); XCTAssertEqual(p.end, d(2026, 10, 26))
        XCTAssertEqual(p.allDays(calendar: cal).count, 30)
        // Early in a month it still has a month's worth: the 1st of November.
        let first = Period.last30(now: d(2026, 11, 1, 9), calendar: cal)
        XCTAssertEqual(first.start, d(2026, 10, 3))
        XCTAssertEqual(p.title(calendar: cal, now: now), "Last 30 days")
        XCTAssertEqual(p.spokenTitle(calendar: cal, now: now), "the last 30 days")
        XCTAssertEqual(p.inPhrase(calendar: cal, now: now), "in the last 30 days")
    }

    func testTheLast30DaysDoNotStepAndAreRememberedAsThemselves() {
        let p = Period.last30(now: now, calendar: cal)
        XCTAssertFalse(p.canStep)
        XCTAssertEqual(p.shifted(by: -1, calendar: cal), p)
        let stored = p.stored(now: now, calendar: cal)
        XCTAssertEqual(stored, Period.Stored(kind: .last30))
        XCTAssertEqual(Period(stored, now: d(2026, 10, 27, 8), calendar: cal).start, d(2026, 9, 28), "it follows the calendar, like Today")
    }

    func testTheSheetOffersThirtyDaysBetweenMonthAndCustom() {
        XCTAssertEqual(PeriodSheetLogic.Choice.allCases.map(\.rawValue), ["Day", "Week", "Month", "30 days", "Custom"])
        let p = Period.last30(now: now, calendar: cal)
        XCTAssertEqual(PeriodSheetLogic.choice(p), .last30)
        XCTAssertEqual(PeriodSheetLogic.pick(.last30, from: Period(containing: now, calendar: cal), now: now, calendar: cal), p)
    }

    @MainActor func testActivityOpensOnTheLast30DaysUntilAChoiceIsSaved() {
        let suite = "rivolt.test.period.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let state = AppState(defaults: defaults)
        XCTAssertFalse(state.hasSavedPeriod)
        XCTAssertEqual(state.activityPeriod.kind, .last30, "a first visit: the feed shows what is recent")
        XCTAssertEqual(state.period.kind, .month, "Now and Truck keep this month")
        state.setPeriod(Period(containing: .now, calendar: .current))
        XCTAssertTrue(state.hasSavedPeriod)
        XCTAssertEqual(state.activityPeriod.kind, .month, "a saved choice wins, Month included")
        let again = AppState(defaults: defaults)
        XCTAssertTrue(again.hasSavedPeriod)
        XCTAssertEqual(again.activityPeriod.kind, .month)
    }
}
