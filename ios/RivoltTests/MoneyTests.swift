import XCTest
@testable import Rivolt

private func chicago() -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "America/Chicago")!
    c.locale = Locale(identifier: "en_US")
    return c
}

private func at(_ s: String) -> Date { JSON.parseDate(s)! }

/// A fixture decoded after an edit to its JSON (a field the shared fixture doesn't cover).
private func edited<T: Decodable>(_ type: T.Type, _ name: String, _ edit: (inout [String: Any]) -> Void) throws -> T {
    var obj = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data(name)) as? [String: Any])
    edit(&obj)
    return try JSON.decoder().decode(T.self, from: JSONSerialization.data(withJSONObject: obj))
}

/// One receipt of the shared month with some fields replaced (in the receipt and in each of its sessions).
private func receipt(_ index: Int = 0, _ fields: [String: Any], session: [String: Any] = [:]) throws -> LedgerReceipt {
    let m = try edited(LedgerMonth.self, "ledger_monthly") { o in
        var rs = o["receipts"] as! [[String: Any]]
        var r = rs[index]
        for (k, v) in fields { r[k] = v }
        var ss = r["sessions"] as! [[String: Any]]
        for i in ss.indices { for (k, v) in session { ss[i][k] = v } }
        r["sessions"] = ss
        rs[index] = r
        o["receipts"] = rs
    }
    return try XCTUnwrap(m.receipts?[index])
}

final class MoneyLogicTests: XCTestCase {
    private func month() throws -> LedgerMonth { try Fixture.decode(LedgerMonth.self, "ledger_monthly") }
    private func summary() throws -> LedgerSummary { try Fixture.decode(LedgerSummary.self, "ledger_summary") }

    func testTheSharedFixturesDecode() throws {
        let m = try month()
        XCTAssertEqual(m.period, "2026-09")
        XCTAssertEqual(m.receipts?.count, 4)
        XCTAssertEqual(m.energy.byPlaceKind?.away.dcfc.cost, 23.41)
        let s = try summary()
        XCTAssertEqual(s.months.map(\.period), ["2026-08", "2026-09", "2026-10"])
        XCTAssertEqual(s.toConfirmCount, 0)
        XCTAssertNil(s.months[0].receipts)
        XCTAssertEqual(try Fixture.decode(LedgerSavings.self, "ledger_savings").couldHaveSaved, 15.44)
        XCTAssertEqual(try Fixture.decode(LedgerSavings.self, "ledger_savings_needs").needs, "more_home_sessions")
    }

    func testAPastMonthReadsAsTheWebsStatement() throws {
        let st = MoneyStatement(month: try month(), months: try summary().months, previous: nil, toConfirm: 0, now: at("2026-10-12T17:00:00Z"), calendar: chicago())
        XCTAssertEqual(st.headline, 35.43)
        XCTAssertFalse(st.isCurrentMonth)
        XCTAssertEqual(st.subline, "spent on charging · 180 kWh · 274 mi · $0.129/mi")
        // August spent nothing: no "vs" figure against $0.
        XCTAssertNil(st.delta)
        XCTAssertNil(st.zero)
        XCTAssertNil(st.estimatedPercent)
        XCTAssertEqual(st.issues, [MoneyStatement.Issue(lead: nil, text: "Not included: 1 unpriced session (16.2 kWh)", amber: false)])
        XCTAssertEqual(st.notes, [
            "1 session has no price and isn't in the total.",
            "1 older session's price source was worked out from your home rate rather than recorded.",
        ])
        XCTAssertFalse(st.empty)
        XCTAssertFalse(st.showObservedNotice)
    }

    func testAMonthInProgressIsComparedWithTheSameDaysOfTheLastOne() throws {
        let cur = try edited(LedgerMonth.self, "ledger_monthly") { o in
            o["period"] = "2026-10"
            o["is_current_month"] = true
            o["range"] = ["start": "2026-10-01T05:00:00Z", "end": "2026-11-01T05:00:00Z"]
            o["first_activity_at"] = "2026-10-01T06:00:00Z"
        }
        // September's receipts on the 3rd ($6.81) and 11th ($23.41) are before the 12th; the 17th and 24th are not.
        let st = MoneyStatement(month: cur, months: try summary().months, previous: try month(), toConfirm: nil,
                                now: at("2026-10-12T17:00:00Z"), calendar: chicago())
        XCTAssertTrue(st.isCurrentMonth)
        XCTAssertEqual(try XCTUnwrap(st.delta), (35.43 - 30.22) / 30.22, accuracy: 1e-9)
        XCTAssertEqual(st.deltaLabel, "September 1–12")
        // Without the previous month in full there is nothing to compare with yet.
        let none = MoneyStatement(month: cur, months: try summary().months, previous: nil, toConfirm: nil, now: at("2026-10-12T17:00:00Z"), calendar: chicago())
        XCTAssertNil(none.delta)
    }

    func testAPreviousMonthWithPartOfItsDaysIsNotCompared() throws {
        // September's data starts on the 4th: a past month against it says nothing true.
        let oct = try edited(LedgerMonth.self, "ledger_monthly") { o in o["period"] = "2026-10" }
        let st = MoneyStatement(month: oct, months: try summary().months, previous: nil, toConfirm: nil, now: at("2026-11-12T17:00:00Z"), calendar: chicago())
        XCTAssertNil(st.delta)
        XCTAssertEqual(st.deltaLabel, "September")
    }

    func testTheFirstMonthWithDataIsMarkedPartial() throws {
        let m = try month()
        let months = try summary().months
        let st = MoneyStatement(month: m, months: months, previous: nil, toConfirm: nil, now: .now, calendar: chicago())
        XCTAssertEqual(st.partialFrom, at("2026-09-04T02:00:00Z"))
        XCTAssertEqual(MoneyLogic.shortDay(try XCTUnwrap(st.partialFrom), calendar: chicago()), "Sep 3")
    }

    func testZeroSpendSaysWhyAndPointsAtTheHomeRate() throws {
        let m = try edited(LedgerMonth.self, "ledger_monthly") { o in
            var s = o["spend"] as! [String: Any]
            s["total"] = 0
            s["priced_sessions"] = 0
            s["unpriced_home_rate_estimate"] = NSNull()
            o["spend"] = s
        }
        let st = MoneyStatement(month: m, months: [], previous: nil, toConfirm: nil, now: .now, calendar: chicago())
        XCTAssertEqual(st.zero?.sessions, 1)
        XCTAssertEqual(st.zero?.noRate, true)
        // The "Not included" line is left out: the $0 notice already says it.
        XCTAssertTrue(st.issues.isEmpty)
        // No $/mi on a month with nothing priced.
        XCTAssertEqual(st.subline, "spent on charging · 180 kWh · 274 mi")
    }

    func testBilledAndEstimatedSplitTheHeadline() throws {
        let m = try edited(LedgerMonth.self, "ledger_monthly") { o in
            var s = o["spend"] as! [String: Any]
            s["billed_total"] = 23.41
            s["estimated_total"] = 12.5
            s["estimated_share"] = 0.35
            o["spend"] = s
            o["billed_sessions"] = 1
            o["estimated_sessions"] = 2
        }
        let st = MoneyStatement(month: m, months: [], previous: nil, toConfirm: nil, now: .now, calendar: chicago())
        XCTAssertEqual(st.headline, 23.41 + 12.5, accuracy: 1e-9)
        XCTAssertEqual(st.billed?.billedSessions, 1)
        // The estimated share line is the old wording: not shown next to the billed split.
        XCTAssertNil(st.estimatedPercent)
    }

    func testReceiptLinesSayWhereEachPriceCameFrom() throws {
        let rs = try XCTUnwrap(try month().receipts)
        let away = MoneyReceiptLine(rs[0], currency: "USD")
        XCTAssertEqual(away.value, .dash)
        XCTAssertEqual(away.label, .plain(prefix: false, text: "Price unknown", amber: false))
        XCTAssertEqual(away.action?.label, "Add price →")
        XCTAssertEqual(away.action?.to, .charge("chg-away-1", pricing: false))
        XCTAssertEqual(away.chip.label, "Away")

        let home = MoneyReceiptLine(rs[1], currency: "USD")
        XCTAssertEqual(home.value, .recorded("$5.21"))
        XCTAssertEqual(home.label, .plain(prefix: false, text: "Home rate · $0.13/kWh", amber: false))
        XCTAssertNil(home.action)
        XCTAssertEqual(home.notes, ["2 days"])
        XCTAssertEqual(home.chip.label, "Home")

        let fast = MoneyReceiptLine(rs[2], currency: "USD")
        XCTAssertEqual(fast.value, .recorded("$23.41"))
        XCTAssertEqual(fast.label, .plain(prefix: false, text: "Billed · $0.33/kWh", amber: false))
        XCTAssertEqual(fast.chip.label, "Fast")
    }

    func testEstimatesAndLikelyWrongPricesAreMarked() throws {
        let open = try receipt(2, ["cost_source": "network_estimate", "network": "Electrify America", "network_state": "estimated", "rate": 0.36],
                               session: ["cost_source": "network_estimate", "network_state": "estimated"])
        let o = MoneyReceiptLine(open, currency: "USD")
        XCTAssertEqual(o.value, .estimated("$23.41"))
        XCTAssertEqual(o.label, .plain(prefix: false, text: "Estimated, confirm · Electrify America · $0.36/kWh", amber: false))
        XCTAssertEqual(o.action?.label, "Confirm →")
        XCTAssertEqual(o.action?.to, .charge("chg-fast-1", pricing: true))

        let confirmed = try receipt(2, ["cost_source": "network_estimate", "network": "Electrify America", "network_state": "confirmed"],
                                    session: ["cost_source": "network_estimate", "network_state": "confirmed"])
        XCTAssertEqual(MoneyReceiptLine(confirmed, currency: "USD").value, .recorded("$23.41"))
        XCTAssertTrue(MoneyLogic.sourceLabel(confirmed, currency: "USD").hasPrefix("Confirmed · Electrify America"))

        let observed = try receipt(2, ["cost_source": "observed_estimate", "estimate_month": "2026-10"], session: ["cost_source": "observed_estimate"])
        let ob = MoneyReceiptLine(observed, currency: "USD")
        XCTAssertEqual(ob.value, .estimated("$23.41"))
        XCTAssertEqual(MoneyLogic.sourceLabel(observed, currency: "USD"), "Estimate: typical price seen by DCFC Tracker, Oct 2026 · $0.33/kWh")
        XCTAssertEqual(ob.rateAction, "Set your rate →")
        let undated = try receipt(2, ["cost_source": "observed_estimate", "estimate_month": "2026-13"])
        XCTAssertEqual(MoneyLogic.sourceLabel(undated, currency: "USD"), "Estimate (typical observed price, date unknown) · $0.33/kWh")

        let wrong = try receipt(1, ["place": "away", "flags": ["home_rate_away"]], session: ["flags": ["home_rate_away"]])
        let w = MoneyReceiptLine(wrong, currency: "USD")
        XCTAssertEqual(w.value, .suspect("$5.21"))
        XCTAssertEqual(w.label, .plain(prefix: false, text: "Home rate, but away from home · $0.13/kWh · likely wrong", amber: true))
        XCTAssertEqual(w.action?.label, "Fix price →")
        XCTAssertEqual(w.action?.to, .charge("chg-home-2", pricing: false))

        let pending = try receipt(2, ["cost_source": "home_rate_misapplied", "flags": ["estimate_pending"]], session: ["cost_source": "home_rate_misapplied"])
        let p = MoneyReceiptLine(pending, currency: "USD")
        XCTAssertEqual(p.value, .pending)
        XCTAssertEqual(p.action?.label, "Add price →")

        let free = try receipt(1, ["cost_source": "user", "cost": 0])
        XCTAssertEqual(MoneyReceiptLine(free, currency: "USD").value, .free)
        XCTAssertEqual(MoneyLogic.sourceLabel(free, currency: "USD"), "Free · entered by you")

        let needsRate = try receipt(0, ["needs_rate": true, "rate_slug": "ionna"], session: ["network": "IONNA", "needs_rate": true])
        let n = MoneyReceiptLine(needsRate, currency: "USD")
        XCTAssertEqual(n.rateAction, "Set your rate for IONNA →")
        XCTAssertNil(n.action)
        XCTAssertEqual(MoneyLogic.sourceLabel(needsRate, currency: "USD"), "Needs a price")
    }

    func testARivianReceiptWinsAndSaysSo() throws {
        let billed = try receipt(2, ["billed_cost": 24.10, "billed_currency": "USD", "billed_sessions": 1, "estimated_sessions": 0])
        let b = MoneyReceiptLine(billed, currency: "USD")
        XCTAssertEqual(b.value, .billed("$24.10"))
        XCTAssertEqual(b.label, .billed)

        let estimated = try receipt(1, ["billed_sessions": 0, "estimated_sessions": 1])
        XCTAssertEqual(MoneyReceiptLine(estimated, currency: "USD").label, .plain(prefix: true, text: "Home rate · $0.13/kWh", amber: false))
    }

    func testTheSplitIsOneChargingBarFromTheServersCells() throws {
        let bars = MoneyLogic.splitBars(try month())
        XCTAssertEqual(bars.map(\.label), ["Charging"])
        XCTAssertEqual(bars[0].segments.map(\.label), ["Home", "Away", "Fast"])
        XCTAssertEqual(bars[0].segments.map(\.totals.cost), [12.02, 0, 23.41])
        XCTAssertEqual(bars[0].segments.map(\.totals.sessions), [2, 1, 1])
        // An older server without the cells: its two bars.
        let old = try edited(LedgerMonth.self, "ledger_monthly") { o in
            var e = o["energy"] as! [String: Any]
            e["by_place_kind"] = nil
            o["energy"] = e
        }
        XCTAssertEqual(MoneyLogic.splitBars(old).map(\.label), ["Where", "How"])
    }

    func testFiltersMatchTheWebs() throws {
        let rs = try XCTUnwrap(try month().receipts)
        XCTAssertEqual(rs.filter { MoneyLogic.Filter.home.matches($0) }.map(\.receiptId), ["rcpt_chg-home-2", "rcpt_chg-home-1"])
        XCTAssertEqual(rs.filter { MoneyLogic.Filter.away.matches($0) }.map(\.receiptId), ["rcpt_chg-away-1"])
        XCTAssertEqual(rs.filter { MoneyLogic.Filter.dcfc.matches($0) }.map(\.receiptId), ["rcpt_chg-fast-1"])
        XCTAssertTrue(rs.allSatisfy { !MoneyLogic.Filter.confirm.matches($0) })
        XCTAssertEqual(MoneyLogic.Filter.shown(current: .all, toConfirm: 0), [.all, .home, .away, .dcfc])
        XCTAssertEqual(MoneyLogic.Filter.shown(current: .all, toConfirm: 2), MoneyLogic.Filter.allCases)
    }

    func testDistanceFromHomeIsRoundedSoItNeverGivesAPositionAway() {
        XCTAssertEqual(MoneyLogic.distance(km: 118.19, metric: false), "75 mi")
        XCTAssertEqual(MoneyLogic.distance(km: 118.19, metric: true), "120 km")
        XCTAssertEqual(MoneyLogic.distance(km: 30, metric: false), "19 mi")
        XCTAssertEqual(MoneyLogic.distance(km: 30, metric: true), "30 km")
    }

    func testSavingsReadAsAnEstimate() throws {
        guard case let .saved(saved, moved, leftOut, asCharged, ifMoved, _, considered, basis) = MoneyLogic.savingsView(try Fixture.decode(LedgerSavings.self, "ledger_savings")) else {
            return XCTFail("expected a figure")
        }
        XCTAssertEqual(MoneyLogic.savedHeadline(moved: moved, money: MoneyLogic.money(saved, "USD")),
                       "Moving 8 home charges into your off-peak windows could have saved about $15.44 (estimate).")
        XCTAssertEqual(leftOut, 0)
        XCTAssertEqual(asCharged, 33.47)
        XCTAssertEqual(ifMoved, 18.03)
        XCTAssertEqual(considered, 8)
        XCTAssertFalse(basis.isEmpty)
        XCTAssertEqual(MoneyLogic.savingsView(try Fixture.decode(LedgerSavings.self, "ledger_savings_needs")), .needs(.sessions))
        let schedule = try edited(LedgerSavings.self, "ledger_savings") { $0["needs"] = "offpeak_schedule" }
        XCTAssertEqual(MoneyLogic.savingsView(schedule), .needs(.schedule))
        let left = try edited(LedgerSavings.self, "ledger_savings") { o in
            o["sessions_left_out"] = 3
            o["left_out_reasons"] = ["plugged_in_too_briefly": 2, "some_new_reason": 1]
        }
        guard case let .saved(_, _, n, _, _, reasons, _, _) = MoneyLogic.savingsView(left) else { return XCTFail("expected a figure") }
        XCTAssertEqual(n, 3)
        XCTAssertEqual(reasons, ["plugged in too briefly for the charge to fit in one stretch (2)", "some new reason (1)"])
        XCTAssertEqual(MoneyLogic.leftOutLine(1), "1 session could not be moved and is left out.")
    }

    func testLinksOpenTheWebPages() {
        XCTAssertEqual(MoneyLink.charge("chg/1 x", pricing: true).path, "/charges/chg%2F1%20x#pricing")
        XCTAssertEqual(MoneyLink.homeRate.path, "/settings?tab=charging#cost")
    }

    func testToConfirmChargesComeFromTheChargeList() throws {
        let json = """
        [{"ID":"c1","StartedAt":"2026-10-02T10:00:00Z","Lat":30.2672,"Lon":-97.7431,"PricePerKWh":0.48,"Cost":18.2,"Currency":"USD","network_state":"needs_confirmation",
          "network_evidence":{"candidates":[{"network":"Electrify America"}]}},
         {"ID":"c2","StartedAt":"2026-10-03T10:00:00Z","PricePerKWh":0,"Cost":0,"Currency":"","network_state":"confirmed"}]
        """
        let rows = try JSON.decoder().decode([ConfirmCharge].self, from: Data(json.utf8))
        // The coordinates in the list are dropped while decoding: the type has nothing that could hold a place.
        let fields = Mirror(reflecting: rows[0]).children.compactMap(\.label).map { $0.lowercased() }
        XCTAssertEqual(fields, ["id", "startedat", "priceperkwh", "cost", "currency", "networkstate", "networkevidence"])
        XCTAssertFalse(fields.contains { $0.contains("lat") || $0.contains("lon") || $0.contains("place") || $0.contains("address") })
        let evidence = Mirror(reflecting: try XCTUnwrap(rows[0].networkEvidence?.candidates?.first)).children.compactMap(\.label)
        XCTAssertEqual(evidence, ["network"])
        XCTAssertEqual(rows[0].networkEvidence?.candidates?.first?.network, "Electrify America")
        XCTAssertEqual(rows.filter { MoneyLogic.isOpenNetworkState($0.networkState) }.map(\.id), ["c1"])
    }
}

/// A fake ledger: answers from the fixtures, or fails with the status given.
private final class FakeMoney: MoneySource, @unchecked Sendable {
    var monthCalls: [(String, String, String?)] = []
    var summaryCalls = 0
    var fail: Int?

    func ledgerMonth(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonth {
        monthCalls.append((month, timeZone, vehicleID))
        if let fail { throw APIError.server(status: fail, message: "no") }
        return try edited(LedgerMonth.self, "ledger_monthly") { o in
            o["period"] = month
            o["is_current_month"] = month == "2026-10"
        }
    }

    func ledgerSummary(months: Int, timeZone: String, vehicleID: String?) async throws -> LedgerSummary {
        summaryCalls += 1
        if let fail { throw APIError.server(status: fail, message: "no") }
        return try Fixture.decode(LedgerSummary.self, "ledger_summary")
    }

    func ledgerSavings(month: String) async throws -> LedgerSavings {
        if let fail { throw APIError.server(status: fail, message: "no") }
        return try Fixture.decode(LedgerSavings.self, "ledger_savings")
    }

    func chargesToConfirm() async throws -> [ConfirmCharge] { [] }
    func chargingRate() async throws -> ChargingRate { ChargingRate(homePricePerKwh: 0.13) }
}

@MainActor
final class MoneyModelTests: XCTestCase {
    private let cal = chicago()
    private let now = at("2026-10-12T17:00:00Z")

    func testAMonthIsReadOnceIn15MinutesAndA429KeepsWhatIsShown() async throws {
        let src = FakeMoney()
        let model = MoneyModel()
        let sep = PeriodMonth(containing: at("2026-09-15T17:00:00Z"), calendar: cal)
        await model.load(source: src, vehicleID: nil, period: sep, now: now, calendar: cal)
        XCTAssertEqual(model.month?.period, "2026-09")
        XCTAssertEqual(src.monthCalls.count, 1)
        XCTAssertEqual(src.monthCalls[0].0, "2026-09")
        XCTAssertEqual(src.monthCalls[0].1, "America/Chicago")
        XCTAssertNil(src.monthCalls[0].2)

        await model.load(source: src, vehicleID: nil, period: sep, now: now.addingTimeInterval(14 * 60), calendar: cal)
        XCTAssertEqual(src.monthCalls.count, 1)
        XCTAssertEqual(src.summaryCalls, 1)

        src.fail = 429
        await model.load(source: src, vehicleID: nil, period: sep, force: true, now: now.addingTimeInterval(20 * 60), calendar: cal)
        XCTAssertEqual(src.monthCalls.count, 2)
        XCTAssertEqual(model.month?.period, "2026-09")
        XCTAssertNotNil(model.summary)
        XCTAssertNil(model.error)

        // Another month never shows September's figures.
        let aug = sep.shifted(by: -1, calendar: cal)
        await model.load(source: src, vehicleID: nil, period: aug, now: now.addingTimeInterval(21 * 60), calendar: cal)
        XCTAssertNil(model.month)
        XCTAssertNotNil(model.error)
        XCTAssertFalse(model.unavailable)
    }

    func testTheTruckIsSentOnlyWhenTheAccountHasMoreThanOne() async {
        let src = FakeMoney()
        let model = MoneyModel()
        await model.load(source: src, vehicleID: "veh-2", period: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal)
        XCTAssertEqual(src.monthCalls.first?.2, "veh-2")
        // October is in progress: September is read in full for the same-days comparison.
        XCTAssertEqual(src.monthCalls.map(\.0), ["2026-10", "2026-09"])
        XCTAssertEqual(model.previous?.period, "2026-09")
    }

    func testNoLedgerOnTheServerSaysSo() async {
        let src = FakeMoney()
        src.fail = 404
        let model = MoneyModel()
        await model.load(source: src, vehicleID: nil, period: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal)
        XCTAssertTrue(model.unavailable)
        XCTAssertNil(model.month)
    }

    func testSavingsAreLastMonthByDefaultAndAFailureHidesTheCard() async {
        XCTAssertEqual(MoneyModel.savingsPeriod(.last, now: now, calendar: cal), "2026-09")
        XCTAssertEqual(MoneyModel.savingsPeriod(.now, now: now, calendar: cal), "2026-10")
        let src = FakeMoney()
        let model = MoneyModel()
        await model.loadSavings(source: src, which: .last, now: now, calendar: cal)
        XCTAssertEqual(model.savings?.couldHaveSaved, 15.44)
        src.fail = 429
        await model.loadSavings(source: src, which: .last, force: true, now: now, calendar: cal)
        XCTAssertEqual(model.savings?.couldHaveSaved, 15.44)
        XCTAssertFalse(model.savingsFailed)
        await model.loadSavings(source: src, which: .now, now: now, calendar: cal)
        XCTAssertNil(model.savings)
        XCTAssertTrue(model.savingsFailed)
    }
}
