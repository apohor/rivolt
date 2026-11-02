import XCTest
@testable import Rivolt

private func chicago() -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "America/Chicago")!
    c.locale = Locale(identifier: "en_US")
    return c
}

private func at(_ s: String) -> Date { JSON.parseDate(s)! }

final class TruckLogicTests: XCTestCase {
    private func drain() throws -> DrainSnapshot { try Fixture.decode(DrainSnapshot.self, "drain") }

    func testTheSharedFixturesDecode() throws {
        let d = try drain()
        XCTAssertEqual(d.windows.week.drainPctPerDay, 0.6)
        XCTAssertEqual(d.windows.month.split?.reason, "not_enough_data")
        XCTAssertEqual(d.daily.count, 30)
        XCTAssertEqual(try Fixture.decode(PackHealth.self, "pack_health").samples?.count, 5)
        XCTAssertEqual(try Fixture.decode(TruckTrends.self, "trends").capacity?.medianKwh, 122.4)
        XCTAssertEqual(try Fixture.decode(ColdRange.self, "cold_range").available, [4.4, 15.6, 26.7])
        XCTAssertEqual(try Fixture.decode(LeaseStatus.self, "lease_status").daysLeft, 588)
        XCTAssertEqual(try Fixture.decode([Vehicle].self, "vehicles")[0].ota?.current?.version, "2026.34.1")
    }

    func testTheStateIsDecodedWithoutItsCoordinates() throws {
        let s = try Fixture.decode(VehicleState.self, "state")
        let fields = Mirror(reflecting: s).children.compactMap(\.label).map { $0.lowercased() }
        XCTAssertFalse(fields.contains { $0.contains("lat") || $0.contains("lon") || $0.contains("heading") || $0.contains("altitude") })
        XCTAssertEqual(s.otaCurrentVersion, "2026.34.1")
    }

    func testParkedDrainReadsAsTheWebs() throws {
        let d = try drain()
        let v = TruckLogic.drainView(d.windows.week, .week)
        XCTAssertEqual(v, .ok(pctPerDay: 0.6, kwhPerDay: 0.8, sleeps: 22, quickRewakes: 0, medianSleepSeconds: 12240, daysUsed: 7, daysExcluded: 3))
        XCTAssertEqual(TruckLogic.drainSleeps(.week, sleeps: 22, quickRewakes: 0, medianSleepSeconds: 12240),
                       "Over 7 days: 22 sleeps, median 3.4 h. Time spent awake while parked is in the Sleep card below.")
        XCTAssertEqual(TruckLogic.drainBasis(daysUsed: 7, daysExcluded: 3, other: .month, otherView: TruckLogic.drainView(d.windows.month, .month)),
                       "Based on 7 days (3 left out: too little parked data). 30 days: 1.5% / day.")
        XCTAssertEqual(TruckLogic.split(d, .week),
                       .ok(text: "Of the 3% your truck lost while parked this week, about 0% was while it was awake (6.0 h).", awakeShare: 0.06, awake: true, asleep: true))
        // 30 days is too thin for the split, so the week's is shown, said as such.
        if case .ok(let text, _, _, _)? = TruckLogic.split(d, .month) { XCTAssertTrue(text.contains("this week")) } else { XCTFail("expected the week's split") }
    }

    func testThinDrainDataNeverGivesANumber() throws {
        let thin = DrainWindow(drainPctPerDay: 2, drainKwhPerDay: nil, sleeps: 1, quickRewakes: 0, medianSleepSeconds: nil, parkedHours: 9.5, daysWithData: 1,
                               daysExcluded: nil, sufficient: false, split: nil)
        XCTAssertEqual(TruckLogic.drainView(thin, .month), .insufficient(haveHours: 9.5, needHours: 72))
        XCTAssertEqual(TruckLogic.hours(9.5), "9.5 h")
        XCTAssertEqual(TruckLogic.hours(0.5), "30 min")
    }

    func testDrainBarsKeepLeftOutDaysAndNeverFakeAZero() throws {
        let (bars, top) = TruckLogic.drainBars(try drain().daily)
        XCTAssertEqual(top, 3.9)
        XCTAssertEqual(bars.count, 30)
        XCTAssertNil(bars[2].value)
        XCTAssertEqual(bars[2].excluded, "insufficient")
        XCTAssertEqual(bars[14].height, 1)
        let rose = try XCTUnwrap(bars.first { $0.gained != nil })
        XCTAssertEqual(rose.gained, 0.3)
        XCTAssertEqual(rose.value, 0)
    }

    func testPackCapacityPrefersTheTrucksOwnFigureAndHidesAGrossSpec() throws {
        let v = TruckLogic.pack(try Fixture.decode(PackHealth.self, "pack_health"))
        XCTAssertEqual(v.head, .reported(kwh: 123, spec: 135))
        XCTAssertEqual(v.trend.count, 5)
        XCTAssertEqual(v.note, "Median of the last 5 clean sessions · 5 total")
        XCTAssertEqual(TruckLogic.specKwh(180), 0)
    }

    /// The line at the top of Truck says exactly what the Battery capacity headline and the Parked drain (7 days) card
    /// say: the same figures, rounding and words, each dropped when its card has none.
    func testTheSummaryLineEqualsTheCardsItSummarises() throws {
        let pack = try Fixture.decode(PackHealth.self, "pack_health")
        let trends = try Fixture.decode(TruckTrends.self, "trends")
        let drain = try drain()
        guard case let .reported(kwh)? = TruckLogic.battery(pack: pack, trends: trends).head,
              case let .ok(pct, _, _, _, _, _, _) = TruckLogic.drainView(TruckLogic.window(drain, .week), .week) else {
            return XCTFail("the fixtures should give both cards a figure")
        }
        XCTAssertEqual(TruckLogic.summary(pack: pack, trends: trends, drain: drain),
                       "Pack \(TruckLogic.fixed(kwh, 1)) kWh usable, as your truck reports it · parked drain \(TruckLogic.pct(pct))% a day (last 7 days)")
        XCTAssertEqual(TruckLogic.summary(pack: pack, trends: trends, drain: drain), "Pack 123.0 kWh usable, as your truck reports it · parked drain 0.6% a day (last 7 days)")
        // The halves name their cards, for the tap that shows them; each reads on its own (one per row at large text).
        XCTAssertEqual(TruckLogic.summaryParts(pack: pack, trends: trends, drain: drain).map(\.id), ["pack", "drain"])
        XCTAssertEqual(TruckLogic.summaryParts(pack: pack, trends: trends, drain: drain).last?.text, "Parked drain 0.6% a day (last 7 days)")
        // No figure from the truck: our estimate (the trends median, the card's headline) is called one.
        let none = try JSON.decoder().decode(PackHealth.self, from: Data(#"{"samples":[],"headline":{"effective_kwh":0,"sample_count":0,"window":0}}"#.utf8))
        guard case let .estimate(e)? = TruckLogic.battery(pack: none, trends: trends).head else { return XCTFail("the card should lead with our estimate") }
        XCTAssertEqual(TruckLogic.summary(pack: none, trends: trends, drain: nil), "Pack about \(TruckLogic.fixed(e, 0)) kWh usable (estimate)")
        XCTAssertEqual(TruckLogic.summary(pack: none, trends: trends, drain: nil), "Pack about 122 kWh usable (estimate)")
        // Only the drain; then nothing at all (no figure from the truck and no estimate yet).
        XCTAssertEqual(TruckLogic.summary(pack: nil, drain: drain), "Parked drain 0.6% a day (last 7 days)")
        XCTAssertNil(TruckLogic.summary(pack: nil, drain: nil))
        XCTAssertNil(TruckLogic.summary(pack: none, trends: nil, drain: nil))
        // No score, comparison or loss words.
        let line = try XCTUnwrap(TruckLogic.summary(pack: pack, trends: trends, drain: drain)).lowercased()
        for word in ["health", "degrad", "loss", "lost", "% of", "spec"] { XCTAssertFalse(line.contains(word), word) }
    }

    /// A truck with no qualifying charge yet: a newer server sends `samples: []`, an older one `null`. Both read as
    /// "none yet", with the truck's own figure still shown.
    func testPackHealthWithNoSamplesDecodesFromAnEmptyListOrNull() throws {
        for samples in ["[]", "null"] {
            let json = """
            {"samples":\(samples),"headline":{"effective_kwh":0,"nameplate_kwh":131,"pct_of_nameplate":0,"documented_kwh":135,"reported_kwh":131,
             "reported_pct_of_documented":97,"sample_count":0,"window":0}}
            """
            let p = try JSON.decoder().decode(PackHealth.self, from: Data(json.utf8))
            XCTAssertEqual(p.samples ?? [], [], samples)
            let v = TruckLogic.pack(p)
            XCTAssertEqual(v.head, .reported(kwh: 131, spec: 135), samples)
            XCTAssertEqual(v.trend, [], samples)
            XCTAssertEqual(v.note, "Effective-capacity trend needs a charge session spanning 30%+ SoC.", samples)
        }
    }

    /// One Battery capacity card: the truck's own figure leads, our estimate (the trends median, one source) is on one
    /// labelled line under it; with no figure from the truck, our estimate leads, called one.
    func testTheBatteryCapacityCardShowsOneHeadlineAndTheOtherFigureLabelled() throws {
        let pack = try Fixture.decode(PackHealth.self, "pack_health")
        let t = try Fixture.decode(TruckTrends.self, "trends")
        let n = t.capacity?.samples.count ?? 0
        let b = TruckLogic.battery(pack: pack, trends: t)
        XCTAssertEqual(b.head, .reported(kwh: 123))
        XCTAssertEqual(b.estimateLine, "Our estimate from your charges: about 122 kWh, from \(n) charges.")
        XCTAssertEqual(b.spec, 135)
        let none = try JSON.decoder().decode(PackHealth.self, from: Data(#"{"samples":[],"headline":{"effective_kwh":0,"documented_kwh":135,"sample_count":0,"window":0}}"#.utf8))
        let e = TruckLogic.battery(pack: none, trends: t)
        XCTAssertEqual(e.head, .estimate(kwh: 122.4))
        XCTAssertNil(e.estimateLine, "the estimate is the headline: no second line")
        // R2 (no capacity block): the truck's figure if any, and the plain "can't be tracked".
        var raw = try XCTUnwrap(try JSONSerialization.jsonObject(with: try Fixture.data("trends")) as? [String: Any])
        raw["capacity"] = NSNull()
        let untracked = try JSON.decoder().decode(TruckTrends.self, from: try JSONSerialization.data(withJSONObject: raw))
        let r2 = TruckLogic.battery(pack: pack, trends: untracked)
        XCTAssertTrue(r2.untracked)
        XCTAssertNil(r2.estimateLine)
        for line in [b.estimateLine ?? "", b.progress ?? ""] {
            for w in ["health", "degrad", "loss", "lost", "% of"] { XCTAssertFalse(line.lowercased().contains(w), w) }
        }
    }

    func testTrendsSayHowFarAlongTheyAre() throws {
        let t = try Fixture.decode(TruckTrends.self, "trends")
        XCTAssertEqual(TruckLogic.capacity(t), .ok(median: 122.4, spec: 135, need: 30))
        XCTAssertEqual(TruckLogic.monthLabel("2026-08"), "Aug 26")
    }

    func testRangeChipsAndBands() throws {
        let d = try Fixture.decode(ColdRange.self, "cold_range")
        XCTAssertEqual(TruckLogic.grid(fahrenheit: true).map { TruckLogic.chip($0, fahrenheit: true) }, ["0 °F", "20 °F", "40 °F", "60 °F", "80 °F"])
        XCTAssertEqual(TruckLogic.grid(fahrenheit: false).map { TruckLogic.chip($0, fahrenheit: false) }, ["-20 °C", "-10 °C", "0 °C", "10 °C", "20 °C"])
        XCTAssertTrue(TruckLogic.chipOff(d, -17.8))
        XCTAssertFalse(TruckLogic.chipOff(d, 15.6))
        XCTAssertEqual(TruckLogic.band(283, 283, digits: 0, unit: "mi"), "283 mi")
        XCTAssertEqual(TruckLogic.band(2.25, 2.41, digits: 1, unit: "mi/kWh"), "2.3–2.4 mi/kWh")
        // Halves round up as the web's toFixed does, not to even.
        XCTAssertEqual(TruckLogic.fixed(0.125, 2), "0.13")
        XCTAssertEqual(MoneyLogic.money(0.125, "USD"), "$0.13")
        XCTAssertEqual(TruckLogic.temperature(11.7, fahrenheit: true), "53 °F")
    }

    func testTheLeaseBarAndStanding() throws {
        let st = try Fixture.decode(LeaseStatus.self, "lease_status")
        let b = TruckLogic.leaseBar(st, allowedTotal: 36000)
        XCTAssertEqual(b.used, 14520.2 / 36000, accuracy: 1e-9)
        XCTAssertEqual(b.plan, 15880.3 / 36000, accuracy: 1e-9)
        XCTAssertEqual(b.allowed, 1)
        XCTAssertEqual(b.tone, .ok)
        XCTAssertEqual(TruckLogic.planStanding(st, metric: false)?.text, "1,360 mi under plan")
        XCTAssertEqual(TruckLogic.planStanding(st, metric: true)?.text, "2,189 km under plan")
        XCTAssertEqual(TruckLogic.distance(36000, metric: false), "36,000 mi")
    }

    func testTheSleepWindowIsTheWebs() {
        let cal = chicago()
        let oct = PeriodMonth(containing: at("2026-10-15T17:00:00Z"), calendar: cal)
        let w = TruckLogic.sleepWindow(oct, now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertEqual(w.since, at("2026-10-01T00:00:00Z"))
        XCTAssertEqual(w.until, at("2026-10-23T00:00:00Z"))
        XCTAssertFalse(w.capped)
    }

    func testSleepTroubleNeedsThreeFailedSleepsInTwoDays() {
        func day(_ s: String, failed: Int, sleeps: Int) -> DayActivity { DayActivity(day: at(s), asleepH: 10, idleAwakeH: 1.5, sleeps: sleeps, failedSleeps: failed) }
        let cal = chicago()
        let ok = TruckLogic.sleepSummary([day("2026-10-20T00:00:00Z", failed: 1, sleeps: 4), day("2026-10-21T00:00:00Z", failed: 1, sleeps: 5)], now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertNil(ok.trouble)
        XCTAssertEqual(ok.averageDays, 2)
        XCTAssertEqual(ok.average, 90)
        let bad = TruckLogic.sleepSummary([day("2026-10-20T00:00:00Z", failed: 2, sleeps: 4), day("2026-10-21T00:00:00Z", failed: 2, sleeps: 5)], now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertEqual(bad.trouble?.failed, 4)
        XCTAssertEqual(bad.trouble?.sleeps, 9)
        XCTAssertEqual(TruckLogic.minutes(395), "6 h 35 min")
        XCTAssertEqual(TruckLogic.minutesShort(1022), "17h 2m")
        XCTAssertEqual(TruckLogic.minutesShort(0), "0m")
    }

    private func state(_ edit: (inout [String: Any]) -> Void) throws -> VehicleState {
        var o = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("state")) as? [String: Any])
        edit(&o)
        return try JSON.decoder().decode(VehicleState.self, from: JSONSerialization.data(withJSONObject: o))
    }

    // MARK: Sleep health

    func testSleepHealthDecodesTheServersAnswerAndReadsInTheWebsWords() throws {
        let h = try Fixture.decode(SleepHealth.self, "sleep_health")
        XCTAssertEqual(h.state, .more)
        XCTAssertEqual(h.thisWeek.failedSleeps, 9)
        XCTAssertEqual(h.moreBy, 4)
        let l = TruckLogic.sleepHealthLines(h)
        XCTAssertEqual(l.headline, "Woke 4 more times than usual this week.")
        XCTAssertEqual(l.detail, "9 short sleeps (under a minute) this week; about 5 in a usual week.")
        XCTAssertEqual(l.basis, "Compared with this truck's own last 30 days.")
    }

    func testSleepHealthNormalAndNotEnoughHistory() throws {
        func make(_ state: String, failed: Int = 1, usual: Double? = 0.2) throws -> SleepHealth {
            var o = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("sleep_health")) as? [String: Any])
            o["state"] = state
            o["this_week"] = ["days": 7, "sleeps": 20, "failed_sleeps": failed]
            if let usual { o["usual_failed_sleeps"] = usual } else { o.removeValue(forKey: "usual_failed_sleeps") }
            o["more_by"] = NSNull()
            return try JSON.decoder().decode(SleepHealth.self, from: JSONSerialization.data(withJSONObject: o))
        }
        let normal = TruckLogic.sleepHealthLines(try make("normal"))
        XCTAssertEqual(normal.headline, "Nothing unusual this week.")
        XCTAssertEqual(normal.detail, "1 short sleep (under a minute) this week; almost none in a usual week.")
        let none = TruckLogic.sleepHealthLines(try make("not_enough_data", failed: 3, usual: nil))
        XCTAssertTrue(none.headline.hasPrefix("Not enough history"))
        XCTAssertNil(none.detail, "no numbers without enough history")
    }

    func testSleepHealthNeverOffersACauseOrAVerdictOnTheTruck() throws {
        for state in ["more", "normal", "not_enough_data"] {
            var o = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("sleep_health")) as? [String: Any])
            o["state"] = state
            let h = try JSON.decoder().decode(SleepHealth.self, from: JSONSerialization.data(withJSONObject: o))
            let l = TruckLogic.sleepHealthLines(h)
            let text = [l.headline, l.detail ?? "", l.basis].joined(separator: " ")
            XCTAssertNil(text.range(of: #"slept normally|because|caused|key|app|fault|fine|healthy|problem"#, options: [.regularExpression, .caseInsensitive]), text)
        }
    }

    @MainActor
    func testTheSleepHealthCardLoadsApartAndAnOlderServerHasNone() async throws {
        let src = FakeTruck()
        let m = TruckModel()
        await m.load(source: src, vehicleID: "v1", metric: false)
        XCTAssertEqual(m.sleepHealth?.value?.state, .more)
        // A failed read keeps nothing (nothing to keep) and fails only its own block.
        let fail = FakeTruck()
        fail.fail = 500
        let m2 = TruckModel()
        await m2.load(source: fail, vehicleID: "v1", metric: false)
        XCTAssertEqual(m2.sleepHealth, .failed)
    }

    func testTheTiresCardListsFourWheelsAndMarksLowOnlyByTheFloorOrRivian() throws {
        let w = try XCTUnwrap(TruckLogic.wheels(try state { $0["tire_pressure_rr_bar"] = 2.8 }))
        XCTAssertEqual(w.map(\.label), ["Front left", "Front right", "Rear left", "Rear right"])
        XCTAssertEqual(TruckLogic.fixed(try XCTUnwrap(w[0].psi), 0), "42")
        XCTAssertTrue(w.allSatisfy { TruckLogic.wheelFlag($0) == nil }, "above the floor and with no Rivian word, only the number")
        let low = try XCTUnwrap(TruckLogic.wheels(try state { $0["tire_pressure_fl_bar"] = 2.1; $0["tire_pressure_rr_bar"] = 2.7; $0["tire_pressure_status_rr"] = "Warning_Soft" }))
        XCTAssertEqual(low.map { TruckLogic.wheelFlag($0) }, ["Low", nil, nil, "Low (Rivian)"])
        // 38 psi is under a placard but above the floor: no claim either way, and never "OK" or "Normal".
        let mid = try XCTUnwrap(TruckLogic.wheels(try state { $0["tire_pressure_fl_bar"] = 2.62; $0["tire_pressure_status_fl"] = "Normal" }))
        XCTAssertNil(TruckLogic.wheelFlag(mid[0]))
        for w in low + mid { XCTAssertFalse((TruckLogic.wheelFlag(w) ?? "").contains("OK") || (TruckLogic.wheelFlag(w) ?? "").contains("Normal")) }
    }

    func testAWheelWithNoReadingHasNoNumberAndNoNumberAtAllHidesTheCard() throws {
        let w = try XCTUnwrap(TruckLogic.wheels(try Fixture.decode(VehicleState.self, "state")))
        XCTAssertNil(w[3].bar, "the fixture's rear right sent 0")
        XCTAssertNil(w[3].psi)
        XCTAssertFalse(w[3].low)
        XCTAssertNil(TruckLogic.wheels(try state { for k in ["fl", "fr", "rl", "rr"] { $0["tire_pressure_\(k)_bar"] = 0 } }))
    }

    func testOnlyAFixedListOfRivianLowWordsCounts() {
        for v in ["warning_soft", "Warning_Soft", " WARNING_HARD ", "warning puncture", "warning-hard", "WARNING_SOFT", "TIRE_PRESSURE_STATUS_WARNING_SOFT", "tire_pressure_status_warning_hard"] { XCTAssertTrue(TruckLogic.rivianSaysLow(v), v) }
        for v in ["", "Normal", "unknown", "undefined", "high", "lowish", "UNSPECIFIED", "unspecified", "NORMAL", "low", "critical", "very low", "TIRE_PRESSURE_STATUS_NORMAL", "TIRE_PRESSURE_STATUS_UNSPECIFIED"] { XCTAssertFalse(TruckLogic.rivianSaysLow(v), v) }
        XCTAssertFalse(TruckLogic.rivianSaysLow(nil))
        XCTAssertTrue(TruckLogic.tiresNote.contains("32 psi"))
        XCTAssertFalse(TruckLogic.tiresNote.lowercased().contains("unsafe"))
    }

    func testTiresSoftwareAndTheNotesLink() throws {
        let s = try Fixture.decode(VehicleState.self, "state")
        let tire = try XCTUnwrap(TruckLogic.lowestTire(s))
        XCTAssertEqual(tire.bar, 2.9)
        XCTAssertEqual(TruckLogic.fixed(tire.psi, 0), "42")
        XCTAssertFalse(tire.low)
        let sw = try XCTUnwrap(TruckLogic.software(s))
        XCTAssertEqual(sw.current, "2026.34.1")
        XCTAssertNil(sw.available)
        XCTAssertNotNil(TruckLogic.notesURL("https://rivian.com/support/notes"))
        XCTAssertNil(TruckLogic.notesURL("http://rivian.com/support/notes"))
        XCTAssertNil(TruckLogic.notesURL("https://user:pw@rivian.com/notes"))
        XCTAssertNil(TruckLogic.notesURL("javascript:alert(1)"))
        XCTAssertTrue(TruckLogic.isR2(" r2 "))
    }
}

/// A fake server: answers from the fixtures, counts the calls, or fails with the status given.
/// The model reads its cards concurrently (`async let`), so the fake's methods run at once on the cooperative pool:
/// everything they touch goes through one lock. (Unguarded, concurrent appends lost calls and crashed the test.)
private final class FakeTruck: TruckSource, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [String] = []
    private var _fail: Int?
    private var _failMessage = "no"
    private var _lease = true

    var calls: [String] {
        get { lock.withLock { _calls } }
        set { lock.withLock { _calls = newValue } }
    }
    var fail: Int? {
        get { lock.withLock { _fail } }
        set { lock.withLock { _fail = newValue } }
    }
    /// The error text of a failed read (the server's error code for a 402).
    var failMessage: String {
        get { lock.withLock { _failMessage } }
        set { lock.withLock { _failMessage = newValue } }
    }
    var lease: Bool {
        get { lock.withLock { _lease } }
        set { lock.withLock { _lease = newValue } }
    }

    private func check(_ name: String) throws {
        let (f, msg): (Int?, String) = lock.withLock {
            _calls.append(name)
            return (_fail, _failMessage)
        }
        if let f { throw APIError.server(status: f, message: msg) }
    }

    func vehicleState(id: String) async throws -> VehicleState { try check("state"); return try Fixture.decode(VehicleState.self, "state") }
    func drain(vehicleID: String) async throws -> DrainSnapshot? { try check("drain"); return try Fixture.decode(DrainSnapshot.self, "drain") }
    func packHealth(vehicleID: String) async throws -> PackHealth { try check("pack"); return try Fixture.decode(PackHealth.self, "pack_health") }
    func trends(vehicleID: String, metric: Bool) async throws -> TruckTrends? { try check("trends"); return nil }
    func coldRange(vehicleID: String, tempC: Double, grid: [Double], metric: Bool) async throws -> ColdRange? {
        try check("cold \(tempC)"); return try Fixture.decode(ColdRange.self, "cold_range")
    }
    func leaseTerms(vehicleID: String) async throws -> LeaseTerms? { try check("lease"); return lease ? try Fixture.decode(LeaseTerms.self, "lease_terms") : nil }
    func leaseStatus(vehicleID: String) async throws -> LeaseStatus? { try check("leaseStatus"); return try Fixture.decode(LeaseStatus.self, "lease_status") }
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity] {
        try check("sleep"); return try Fixture.decode([DayActivity].self, "sleep_activity")
    }
    func sleepHealth(vehicleID: String) async throws -> SleepHealth? {
        try check("sleepHealth"); return try Fixture.decode(SleepHealth.self, "sleep_health")
    }
    func chargePlaces() async throws -> [ChargePlace] {
        try check("places")
        return [ChargePlace(label: "Home", lat: 32.7767, lon: -96.797, sessions: 40, energyKwh: 1500, radiusM: 30)]
    }
}

@MainActor
final class TruckModelTests: XCTestCase {
    private let now = at("2026-10-22T17:00:00Z")

    func testCardsAreReadOnceIn15MinutesAndA429KeepsThem() async {
        let src = FakeTruck()
        let model = TruckModel()
        await model.load(source: src, vehicleID: "v1", metric: false, now: now)
        XCTAssertEqual(Set(src.calls), ["state", "drain", "pack", "trends", "lease", "leaseStatus", "places", "sleepHealth"])
        XCTAssertNotNil(model.drain?.value)
        // No trends on this server: the card is left out, not an error.
        XCTAssertEqual(model.trends, .missing)
        XCTAssertNotNil(model.lease?.value)

        src.calls = []
        await model.load(source: src, vehicleID: "v1", metric: false, now: now.addingTimeInterval(14 * 60))
        XCTAssertEqual(src.calls, [])

        src.fail = 429
        await model.load(source: src, vehicleID: "v1", metric: false, force: true, now: now.addingTimeInterval(20 * 60))
        XCTAssertNotNil(model.drain?.value)
        XCTAssertNotNil(model.pack?.value)
        XCTAssertNotNil(model.state)

        // Another truck never shows the first one's figures.
        await model.load(source: src, vehicleID: "v2", metric: false, now: now.addingTimeInterval(21 * 60))
        XCTAssertEqual(model.drain, .failed)
        XCTAssertNil(model.state)
    }

    func testAFreeAccountsProCardsAreLimitedNotFailed() async {
        let src = FakeTruck()
        src.fail = 402
        src.failMessage = "pro_required"
        let model = TruckModel()
        await model.load(source: src, vehicleID: "v1", metric: false, now: now)
        XCTAssertEqual(model.pack, .limited)
        XCTAssertEqual(model.trends, .limited)
        // A card that failed some other way stays failed; a 402 whose code is not ours is not a limit.
        src.failMessage = "no"
        await model.load(source: src, vehicleID: "v2", metric: false, now: now)
        XCTAssertEqual(model.pack, .failed)
    }

    func testTheLeaseStatusIsOnlyAskedWithTerms() async {
        let src = FakeTruck()
        src.lease = false
        let model = TruckModel()
        await model.load(source: src, vehicleID: "v1", metric: false, now: now)
        XCTAssertFalse(src.calls.contains("leaseStatus"))
        XCTAssertEqual(model.lease, .missing)
        XCTAssertNil(model.leaseTerms)
    }

    func testSleepAndRangeFollowTheMonthAndTemperature() async {
        let src = FakeTruck()
        let model = TruckModel()
        let cal = chicago()
        await model.loadSleep(source: src, vehicleID: "v1", month: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal)
        XCTAssertNotNil(model.sleep?.value)
        await model.loadCold(source: src, vehicleID: "v1", tempC: 4.4, fahrenheit: true, metric: false, now: now)
        await model.loadCold(source: src, vehicleID: "v1", tempC: 4.4, fahrenheit: true, metric: false, now: now)
        XCTAssertEqual(src.calls.filter { $0.hasPrefix("cold") }, ["cold 4.4"])
        await model.loadCold(source: src, vehicleID: "v1", tempC: 15.6, fahrenheit: true, metric: false, now: now)
        XCTAssertEqual(src.calls.filter { $0.hasPrefix("cold") }, ["cold 4.4", "cold 15.6"])
    }
}

/// Answers the client in place of the server, by path.
private final class TruckStub: URLProtocol {
    nonisolated(unsafe) static var paths: [String] = []
    nonisolated(unsafe) static var respond: (String) -> (Int, Data) = { _ in (500, Data()) }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url.map { $0.path + ($0.query.map { "?" + $0 } ?? "") } ?? ""
        Self.paths.append(path)
        let (status, data) = Self.respond(path)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct TruckNoTokens: TokenStore {
    func token(for server: URL) throws -> String? { nil }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

final class TruckClientTests: XCTestCase {
    private func client() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [TruckStub.self]
        return APIClient(baseURL: URL(string: "https://rivolt.example")!, tokens: TruckNoTokens(), session: URLSession(configuration: config))
    }

    override func setUp() {
        super.setUp()
        TruckStub.paths = []
    }

    func testTheReadsAreTheWebsAndA404LeavesTheCardOut() async throws {
        TruckStub.respond = { path in
            if path.hasSuffix("/drain") { return (200, (try? Fixture.data("drain")) ?? Data()) }
            if path.contains("/cold-range") { return (200, (try? Fixture.data("cold_range")) ?? Data()) }
            if path.contains("/pack-health") { return (200, (try? Fixture.data("pack_health")) ?? Data()) }
            if path == "/api/charges/clusters" { return (200, (try? Fixture.data("charge_clusters")) ?? Data()) }
            return (404, Data("404 page not found".utf8))
        }
        let c = client()
        let d = try await c.drain(vehicleID: "veh-fixture")
        XCTAssertEqual(d?.windows.week.sleeps, 22)
        let r = try await c.coldRange(vehicleID: "veh-fixture", tempC: 15.6, grid: [-17.8, -6.7, 4.4, 15.6, 26.7], metric: false)
        XCTAssertEqual(r?.rangeLow, 283)
        let t = try await c.trends(vehicleID: "veh-fixture", metric: true)
        XCTAssertNil(t)
        let l = try await c.leaseTerms(vehicleID: "veh-fixture")
        XCTAssertNil(l)
        let p = try await c.packHealth(vehicleID: "veh-fixture")
        XCTAssertEqual(p.samples?.count, 5)
        let places = try await c.chargePlaces()
        XCTAssertEqual(places.count, 4)
        XCTAssertEqual(TruckStub.paths, [
            "/api/vehicles/veh-fixture/drain",
            "/api/vehicles/veh-fixture/cold-range?temp_c=15.6&grid=-17.8,-6.7,4.4,15.6,26.7&units=imperial",
            "/api/vehicles/veh-fixture/trends?units=metric",
            "/api/vehicles/veh-fixture/lease",
            "/api/vehicles/veh-fixture/pack-health?limit=30",
            "/api/charges/clusters",
        ])
    }

    func testARateLimitIsAnErrorNotAMissingCard() async {
        TruckStub.respond = { _ in (429, Data("{\"error\":\"slow down\"}".utf8)) }
        do {
            _ = try await client().drain(vehicleID: "v")
            XCTFail("expected the 429")
        } catch APIError.server(let status, _) {
            XCTAssertEqual(status, 429)
        } catch {
            XCTFail("unexpected \(error)")
        }
    }
}
